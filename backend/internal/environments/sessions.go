package environments

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

var (
	// ErrAgentOffline means the agent environment has no live tunnel.
	ErrAgentOffline = errors.New("agent not connected")
	// ErrClosed means the Service is shutting down and takes no more agent sessions.
	ErrClosed = errors.New("environments closed")
)

// pingInterval is how often each agent tunnel is pinged for its round trip time.
const pingInterval = 30 * time.Second

// agentSession is an agent's live tunnel and what is known about its link; rtt and
// pingedAt are guarded by Service.sessMu.
type agentSession struct {
	sess     *tunnel.Session
	addr     string
	rtt      time.Duration
	pingedAt time.Time
}

// Link is what the server knows about an agent's connection.
type Link struct {
	// Addr is the address the tunnel came from (the client IP, behind trusted proxies
	// the forwarded one).
	Addr string
	// RTT is the round trip of the last ping, taken at PingedAt; both are zero until the
	// first ping answers.
	RTT      time.Duration
	PingedAt time.Time
}

// Link reports the agent environment's connection, if it has a live tunnel.
func (s *Service) Link(envID int64) (Link, bool) {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	as := s.sessions[envID]
	if as == nil {
		return Link{}, false
	}
	return Link{Addr: as.addr, RTT: as.rtt, PingedAt: as.pingedAt}, true
}

// ServeAgent makes sess, authenticated as agentID and connected from addr, the tunnel of
// the agent environment env until the session closes, ctx ends or the Service closes,
// and returns then. A session the environment already had is closed: the newest
// connection wins. The agent's version and last_seen are recorded at connect and
// last_seen again at disconnect. The tunnel is pinged at connect and every pingInterval;
// Link answers the last round trip.
func (s *Service) ServeAgent(ctx context.Context, env Environment, agentID, addr string, sess *tunnel.Session) error {
	if env.Kind != KindAgent {
		_ = sess.Close()
		return ErrNotAgent
	}
	s.sessMu.Lock()
	if s.closed {
		s.sessMu.Unlock()
		_ = sess.Close()
		return ErrClosed
	}
	old := s.sessions[env.ID]
	as := &agentSession{sess: sess, addr: addr}
	s.sessions[env.ID] = as
	s.serving.Add(1)
	s.sessMu.Unlock()
	defer s.serving.Done()
	if old != nil {
		s.log.Info("agent reconnected, closing its previous tunnel", "env", env.ID)
		_ = old.sess.Close()
	}

	// A revoke or re-enroll between authentication and the registration above found no
	// session to drop; the credentials are checked again now that this one is visible.
	if !s.recordConnect(ctx, env.ID, agentID, sess.Peer().Version) {
		s.log.Warn("agent credentials changed while connecting, closing its tunnel", "env", env.ID)
		_ = sess.Close()
	}
	pinged := make(chan struct{})
	go func() {
		defer close(pinged)
		s.pingLoop(ctx, env.ID, as)
	}()
	select {
	case <-sess.Done():
	case <-ctx.Done():
	}
	_ = sess.Close()
	<-pinged

	s.sessMu.Lock()
	current := s.sessions[env.ID] == as
	if current {
		delete(s.sessions, env.ID)
	}
	s.sessMu.Unlock()
	if current {
		// The request context may be gone by now; the write must still happen.
		s.touch(context.WithoutCancel(ctx), env.ID)
	}
	return nil
}

// pingLoop pings as's tunnel now and every pingInterval until the tunnel closes or ctx
// ends, keeping the last round trip on as.
func (s *Service) pingLoop(ctx context.Context, envID int64, as *agentSession) {
	for {
		rtt, err := as.sess.Ping()
		if err == nil {
			now := s.now()
			s.sessMu.Lock()
			as.rtt, as.pingedAt = rtt, now
			s.sessMu.Unlock()
		} else {
			s.log.Debug("agent ping", "env", envID, "err", err)
		}
		select {
		case <-as.sess.Done():
			return
		case <-ctx.Done():
			return
		case <-s.after(pingInterval):
		}
	}
}

// session is the agent environment's live tunnel, nil if it has none.
func (s *Service) session(envID int64) *tunnel.Session {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	if as := s.sessions[envID]; as != nil {
		return as.sess
	}
	return nil
}

// dropSession closes the agent environment's tunnel, if it has one.
func (s *Service) dropSession(envID int64) {
	if sess := s.session(envID); sess != nil {
		_ = sess.Close()
	}
}

// agentDialer opens a tunnel stream to the agent environment's current session per
// connection, so a reconnected agent is used without a new client. Failures are
// *net.OpError, which docker.Unreachable reports as an offline environment.
func (s *Service) agentDialer(envID int64) docker.DialFunc {
	return func(ctx context.Context) (net.Conn, error) {
		sess := s.session(envID)
		if sess == nil {
			return nil, &net.OpError{Op: "dial", Net: "tunnel", Err: ErrAgentOffline}
		}
		conn, err := sess.Open(ctx)
		if err != nil {
			return nil, &net.OpError{Op: "dial", Net: "tunnel", Err: err}
		}
		return conn, nil
	}
}

// ControlDialer opens a stream to the agent environment's control service on its current
// session per connection. An offline agent fails with a *net.OpError (docker.Unreachable),
// one on tunnel protocol 1 with tunnel.ErrNoControl.
func (s *Service) ControlDialer(envID int64) func(ctx context.Context) (net.Conn, error) {
	return func(ctx context.Context) (net.Conn, error) {
		sess := s.session(envID)
		if sess == nil {
			return nil, &net.OpError{Op: "dial", Net: "tunnel", Err: ErrAgentOffline}
		}
		conn, err := sess.OpenControl(ctx)
		if errors.Is(err, tunnel.ErrNoControl) {
			return nil, err
		}
		if err != nil {
			return nil, &net.OpError{Op: "dial", Net: "tunnel", Err: err}
		}
		return conn, nil
	}
}

// recordConnect stores the agent's version and last_seen if agentID still holds the
// environment's credentials. It reports whether it does; a database failure counts as
// no, since then nothing vouches for the session.
func (s *Service) recordConnect(ctx context.Context, envID int64, agentID, version string) bool {
	res, err := s.db.ExecContext(ctx, `UPDATE environments SET agent_version = ?, last_seen_at = ? WHERE id = ? AND agent_id = ?`,
		version, s.now().Unix(), envID, agentID)
	var n int64
	if err == nil {
		n, err = res.RowsAffected()
	}
	if err != nil {
		s.log.Warn("record agent connect", "env", envID, "err", err)
	}
	return n > 0
}

func (s *Service) touch(ctx context.Context, envID int64) {
	if _, err := s.db.ExecContext(ctx, `UPDATE environments SET last_seen_at = ? WHERE id = ?`, s.now().Unix(), envID); err != nil {
		s.log.Warn("touch environment", "env", envID, "err", err)
	}
}

// closeSessions refuses new agent sessions, closes the live ones and waits until every
// ServeAgent call has returned.
func (s *Service) closeSessions() {
	s.sessMu.Lock()
	s.closed = true
	live := make([]*tunnel.Session, 0, len(s.sessions))
	for _, as := range s.sessions {
		live = append(live, as.sess)
	}
	s.sessMu.Unlock()
	for _, sess := range live {
		_ = sess.Close()
	}
	s.serving.Wait()
}

// connectAgent returns the Docker client of an agent environment.
func (s *Service) connectAgent(env Environment) (docker.Client, error) {
	c, err := docker.New(s.agentDialer(env.ID))
	if err != nil {
		return nil, fmt.Errorf("agent environment %d: %w", env.ID, err)
	}
	return c, nil
}
