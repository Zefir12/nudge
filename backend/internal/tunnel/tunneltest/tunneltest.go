// Package tunneltest runs fake agents for tests: the agent side of a tunnel that serves
// an http.Handler (a fake Docker API) on every stream the server opens.
package tunneltest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

// Agent is a connected fake agent.
type Agent struct {
	Session *tunnel.Session
	srv     *http.Server
	wg      sync.WaitGroup
}

// Connect dials url (http:// or https://, turned into ws:// or wss://) with header, runs
// the agent handshake as hello and serves h on each stream, Docker and control alike,
// until the session closes. The
// agent is closed when t ends. A refused upgrade comes back as an error with the
// response's status code.
func Connect(t testing.TB, url string, header http.Header, hello tunnel.Hello, h http.Handler) (*Agent, error) {
	t.Helper()
	return ConnectConfig(t, url, header, hello, h, tunnel.Config{})
}

// ConnectConfig is Connect with a tunnel configuration, such as an older Protocol.
func ConnectConfig(t testing.TB, url string, header http.Header, hello tunnel.Hello, h http.Handler, cfg tunnel.Config) (*Agent, error) {
	t.Helper()
	ctx := context.Background()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		if resp != nil {
			return nil, &StatusError{Code: resp.StatusCode, Err: err}
		}
		return nil, err
	}
	sess, err := tunnel.Connect(ctx, ws, hello, cfg)
	if err != nil {
		return nil, err
	}
	a := &Agent{Session: sess, srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}}
	a.wg.Go(func() { _ = a.srv.Serve(listener{sess}) })
	t.Cleanup(a.Close)
	return a, nil
}

// Close closes the session and waits until the agent stopped serving.
func (a *Agent) Close() {
	_ = a.Session.Close()
	_ = a.srv.Close()
	a.wg.Wait()
}

// StatusError is a refused WebSocket upgrade.
type StatusError struct {
	Code int
	Err  error
}

func (e *StatusError) Error() string { return e.Err.Error() }
func (e *StatusError) Unwrap() error { return e.Err }

// listener hands the session's streams to an http.Server.
type listener struct{ sess *tunnel.Session }

func (l listener) Accept() (net.Conn, error) {
	c, err := l.sess.Accept()
	if errors.Is(err, tunnel.ErrClosed) {
		return nil, net.ErrClosed
	}
	if err != nil {
		return nil, err
	}
	if l.sess.Protocol() < 2 {
		return c, nil
	}
	return &kindConn{Conn: c}, nil
}

// kindConn drops the stream's kind byte before the first read, so Accept doesn't wait
// for it.
type kindConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *kindConn) Read(p []byte) (int, error) {
	c.once.Do(func() { _, c.err = tunnel.ReadKind(c.Conn, 10*time.Second) })
	if c.err != nil {
		return 0, c.err
	}
	return c.Conn.Read(p)
}

func (l listener) Close() error { return l.sess.Close() }

func (l listener) Addr() net.Addr { return addr{} }

type addr struct{}

func (addr) Network() string { return "tunnel" }
func (addr) String() string  { return "tunnel" }
