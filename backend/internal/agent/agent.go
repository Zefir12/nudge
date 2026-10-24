// Package agent is `nudge agent`: it connects a remote Docker host to a server with only
// outbound traffic. It enrolls once with a join token, keeps the agent ID and secret in
// agent.json, dials the server's tunnel endpoint and pipes every Docker stream the server
// opens to the local Docker API, and serves control streams (stacks) with its control
// handler, reconnecting with backoff until its context ends.
package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

// Backoff bounds between connection attempts.
const (
	minBackoff = time.Second
	maxBackoff = time.Minute
)

// Clock is the time source for backoff and session lengths.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures an Agent; ServerURL, DataDir and Dial are required.
type Options struct {
	// ServerURL is the server's base URL (http:// or https://, optional path prefix).
	ServerURL *url.URL
	// JoinToken enrolls the agent when it has no credentials, or when the server refuses
	// the ones it has.
	JoinToken string
	// DataDir holds agent.json.
	DataDir string
	// Dial connects to the local Docker API.
	Dial docker.DialFunc
	// Control serves the server's control streams (HTTP, see stacks.NewControl); nil
	// refuses them.
	Control http.Handler
	// Version and Hostname go into the tunnel hello.
	Version  string
	Hostname string
	Log      *slog.Logger
	// HTTPClient makes the enroll request and the WebSocket dial (default: a client
	// with the default transport).
	HTTPClient *http.Client
	// Clock defaults to the real one; Jitter(n) returns a random number in [0, n)
	// (default math/rand/v2's Int64N).
	Clock  Clock
	Jitter func(n int64) int64
	// Tunnel tunes the session (tests shorten its timeouts).
	Tunnel tunnel.Config
}

// Agent connects to a server and serves Docker over the tunnel.
type Agent struct {
	opts Options
	log  *slog.Logger
	// control hands control streams to the control handler's http.Server while Run runs.
	control *streamListener
	creds   *Credentials
	// enrolled is set once the join token was used in this process, so a refused
	// credential doesn't spend it twice.
	enrolled bool
}

// New returns an agent for opts.
func New(opts Options) *Agent {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{}
	}
	if opts.Clock == nil {
		opts.Clock = realClock{}
	}
	if opts.Jitter == nil {
		opts.Jitter = rand.Int64N
	}
	if opts.Tunnel.Log == nil {
		opts.Tunnel.Log = opts.Log
	}
	return &Agent{opts: opts, log: opts.Log}
}

// FatalError is a failure retrying can't fix, such as a refused join token.
type FatalError struct{ Msg string }

func (e *FatalError) Error() string { return e.Msg }

// errUnauthorized is the server's 401 on enroll or connect.
var errUnauthorized = errors.New("unauthorized")

// Run connects and serves until ctx ends (it then returns nil) or a FatalError occurs.
// Each failed or ended connection is retried after an exponential backoff with jitter.
func (a *Agent) Run(ctx context.Context) error {
	if a.opts.Control != nil {
		a.control = newStreamListener()
		srv := &http.Server{
			Handler: a.opts.Control, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute,
			ErrorLog: slog.NewLogLogger(a.log.Handler(), slog.LevelWarn),
		}
		var wg sync.WaitGroup
		wg.Go(func() { _ = srv.Serve(a.control) })
		defer func() {
			// Run's sessions are closed by now, which ends their control requests.
			_ = srv.Close()
			wg.Wait()
		}()
	}
	attempt := 0
	for {
		up, err := a.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		var fatal *FatalError
		if errors.As(err, &fatal) {
			return err
		}
		// A session that stayed up as long as the longest backoff counts as a fresh
		// start; one dropped right after connecting keeps backing off, so a server that
		// keeps refusing (or a duplicate agent replacing this one) isn't hammered.
		if up >= maxBackoff {
			attempt = 0
		}
		delay := backoff(attempt, a.opts.Jitter)
		attempt++
		if err != nil {
			a.log.Warn("agent: connection failed", "err", err, "retry_in", delay.Round(time.Millisecond))
		} else {
			a.log.Warn("agent: disconnected", "after", up.Round(time.Second), "retry_in", delay.Round(time.Millisecond))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-a.opts.Clock.After(delay):
		}
	}
}

// backoff is the delay before retry number attempt (from 0): minBackoff doubled per
// attempt up to maxBackoff, of which a random half is taken off ("equal jitter") so
// agents that lost the same server don't come back in lockstep.
func backoff(attempt int, jitter func(int64) int64) time.Duration {
	d := maxBackoff
	if attempt < 6 { // 1 s << 6 = 64 s, past the cap
		d = min(minBackoff<<attempt, maxBackoff)
	}
	half := int64(d / 2)
	return time.Duration(half + jitter(half+1))
}

// runOnce gets credentials, connects and serves one session. It returns how long the
// session was up (zero if it never came up) and why it ended; nil if the server closed it.
func (a *Agent) runOnce(ctx context.Context) (time.Duration, error) {
	if err := a.ensureCredentials(ctx); err != nil {
		return 0, err
	}
	sess, err := a.connect(ctx)
	if errors.Is(err, errUnauthorized) {
		if a.opts.JoinToken == "" || a.enrolled {
			return 0, &FatalError{Msg: "the server refused the agent credentials in " + a.credsPath() +
				" (revoked?): create a join token for this environment and start the agent with it"}
		}
		a.log.Warn("agent: credentials refused, enrolling again with the join token")
		a.creds = nil
		if err := a.enroll(ctx); err != nil {
			return 0, err
		}
		sess, err = a.connect(ctx)
	}
	if errors.Is(err, errUnauthorized) {
		return 0, &FatalError{Msg: "the server refused the agent credentials it just issued"}
	}
	if err != nil {
		return 0, err
	}
	start := a.opts.Clock.Now()
	a.log.Info("agent: connected", "server", a.opts.ServerURL.Redacted(), "server_version", sess.Peer().Version)
	a.serve(sess)
	return a.opts.Clock.Now().Sub(start), nil
}

// ensureCredentials loads agent.json, or enrolls if there is none.
func (a *Agent) ensureCredentials(ctx context.Context) error {
	if a.creds != nil {
		return nil
	}
	creds, err := loadCredentials(a.credsPath())
	if err != nil && !errors.Is(err, errNoCredentials) {
		return &FatalError{Msg: err.Error()}
	}
	if err == nil {
		a.creds = creds
		return nil
	}
	if a.opts.JoinToken == "" {
		return &FatalError{Msg: "no agent credentials in " + a.credsPath() +
			" and no join token: start the agent with the join token from \"Add environment\""}
	}
	return a.enroll(ctx)
}

func (a *Agent) enroll(ctx context.Context) error {
	creds, err := a.postEnroll(ctx)
	if errors.Is(err, errUnauthorized) {
		a.enrolled = true
		return &FatalError{Msg: "the server refused the join token (used, expired, replaced, or spent by an enroll whose answer was lost): create a new one for this environment"}
	}
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	a.enrolled = true
	if err := saveCredentials(a.credsPath(), creds); err != nil {
		// Enrolling again would invalidate these credentials' predecessors for nothing;
		// a data directory we can't write to needs fixing first.
		return &FatalError{Msg: fmt.Sprintf("enrolled, but %v", err)}
	}
	a.creds = creds
	a.log.Info("agent: enrolled", "environment", creds.EnvironmentID, "agent", creds.AgentID)
	return nil
}

// connect dials the tunnel endpoint and runs the handshake.
func (a *Agent) connect(ctx context.Context) (*tunnel.Session, error) {
	u := a.endpoint("/api/v1/agents/connect")
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	default:
		u.Scheme = "ws"
	}
	header := http.Header{}
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(a.creds.AgentID+":"+a.creds.Secret)))

	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ws, resp, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPClient: a.opts.HTTPClient, HTTPHeader: header})
	cancel()
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return nil, errUnauthorized
		}
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("connect: %s answered 404: is the server URL (and its path) right?", u.Redacted())
		}
		if resp != nil {
			return nil, fmt.Errorf("connect: server answered %s", resp.Status)
		}
		return nil, fmt.Errorf("connect: %w", err)
	}
	sess, err := tunnel.Connect(ctx, ws, tunnel.Hello{Version: a.opts.Version, Hostname: a.opts.Hostname}, a.opts.Tunnel)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return sess, nil
}

// serve pipes each stream the server opens to the Docker API until the session ends,
// then waits for the open streams to finish.
func (a *Agent) serve(sess *tunnel.Session) {
	var wg sync.WaitGroup
	defer wg.Wait()
	defer func() { _ = sess.Close() }()
	for {
		stream, err := sess.Accept()
		if err != nil {
			if !errors.Is(err, tunnel.ErrClosed) {
				a.log.Warn("agent: accept stream", "err", err)
			}
			return
		}
		wg.Go(func() { a.handle(sess, stream) })
	}
}

// handle pipes one Docker stream to a new Docker connection, or hands a control stream to
// the control handler. The streams are closed when the session closes, which unblocks the
// copies.
func (a *Agent) handle(sess *tunnel.Session, stream net.Conn) {
	if sess.Protocol() >= 2 {
		kind, err := tunnel.ReadKind(stream, 10*time.Second)
		if err != nil {
			a.log.Warn("agent: stream", "err", err)
			_ = stream.Close()
			return
		}
		if kind == tunnel.KindControl {
			a.serveControl(sess, stream)
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	go func() {
		select {
		case <-sess.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	conn, err := a.opts.Dial(ctx)
	cancel()
	if err != nil {
		a.log.Warn("agent: dial Docker", "err", err)
		_ = stream.Close()
		return
	}
	pipe(stream, conn)
}

// serveControl hands a control stream to the control handler and waits until the
// handler's server closed it, so serve's wait covers it; a closed session closes it.
func (a *Agent) serveControl(sess *tunnel.Session, stream net.Conn) {
	if a.control == nil {
		a.log.Warn("agent: refusing a control stream: no control service")
		_ = stream.Close()
		return
	}
	c := &notifyConn{Conn: stream, closed: make(chan struct{})}
	if !a.control.hand(c, sess.Done()) {
		_ = stream.Close()
		return
	}
	select {
	case <-c.closed:
	case <-sess.Done():
		_ = stream.Close()
	}
}

// streamListener is a net.Listener fed with streams by hand.
type streamListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newStreamListener() *streamListener {
	return &streamListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

// hand passes c to Accept; false if the listener closed or stop fired first.
func (l *streamListener) hand(c net.Conn, stop <-chan struct{}) bool {
	select {
	case l.conns <- c:
		return true
	case <-l.done:
	case <-stop:
	}
	return false
}

func (l *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *streamListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *streamListener) Addr() net.Addr { return streamAddr{} }

type streamAddr struct{}

func (streamAddr) Network() string { return "tunnel" }
func (streamAddr) String() string  { return "tunnel" }

// notifyConn closes closed when the connection is closed.
type notifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

// pipe copies both ways between a and b until either side ends, then closes both. The
// server never half-closes a stream (HTTP clients close the whole connection), and a
// Docker connection that ends is done, so the first end is the end of both.
func pipe(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		_, _ = io.Copy(b, a)
		closeBoth()
	})
	wg.Go(func() {
		_, _ = io.Copy(a, b)
		closeBoth()
	})
	wg.Wait()
}

// endpoint is path under the server URL (which may carry a path prefix).
func (a *Agent) endpoint(path string) *url.URL {
	u := *a.opts.ServerURL
	u.Path += path
	u.RawPath = ""
	return &u
}
