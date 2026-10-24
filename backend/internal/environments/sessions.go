package environments

import (
	"context"
	"errors"
	"fmt"
	"net"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

var (
	// ErrAgentOffline means the agent environment has no live tunnel.
	ErrAgentOffline = errors.New("agent not connected")
	// ErrClosed means the Service is shutting down and takes no more agent sessions.
	ErrClosed = errors.New("environments closed")
)

// ServeAgent makes sess, authenticated as agentID, the tunnel of the agent environment
// env until the session closes, ctx ends or the Service closes, and returns then. A
// session the environment already had is closed: the newest connection wins. The agent's
// version and last_seen are recorded at connect and last_seen again at disconnect.
func (s *Service) ServeAgent(ctx context.Context, env Environment, agentID string, sess *tunnel.Session) error {
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
	s.sessions[env.ID] = sess
	s.serving.Add(1)
	s.sessMu.Unlock()
	defer s.serving.Done()
	if old != nil {
		s.log.Info("agent reconnected, closing its previous tunnel", "env", env.ID)
		_ = old.Close()
	}

	// A revoke or re-enroll between authentication and the registration above found no
	// session to drop; the credentials are checked again now that this one is visible.
	if !s.recordConnect(ctx, env.ID, agentID, sess.Peer().Version) {
		s.log.Warn("agent credentials changed while connecting, closing its tunnel", "env", env.ID)
		_ = sess.Close()
	}
	select {
	case <-sess.Done():
	case <-ctx.Done():
	}
	_ = sess.Close()

	s.sessMu.Lock()
	current := s.sessions[env.ID] == sess
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

// dropSession closes the agent environment's tunnel, if it has one.
func (s *Service) dropSession(envID int64) {
	s.sessMu.Lock()
	sess := s.sessions[envID]
	s.sessMu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// agentDialer opens a tunnel stream to the agent environment's current session per
// connection, so a reconnected agent is used without a new client. Failures are
// *net.OpError, which docker.Unreachable reports as an offline environment.
func (s *Service) agentDialer(envID int64) docker.DialFunc {
	return func(ctx context.Context) (net.Conn, error) {
		s.sessMu.Lock()
		sess := s.sessions[envID]
		s.sessMu.Unlock()
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
		s.sessMu.Lock()
		sess := s.sessions[envID]
		s.sessMu.Unlock()
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
	for _, sess := range s.sessions {
		live = append(live, sess)
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
