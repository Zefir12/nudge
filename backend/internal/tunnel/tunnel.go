// Package tunnel carries many streams between the server and an agent over one
// WebSocket. After a JSON hello in each direction the WebSocket becomes a net.Conn under
// a yamux session in which the server is the yamux client: it opens a stream per Docker
// connection and the agent accepts them. Authentication happens before Accept and Connect,
// on the HTTP request that opens the WebSocket.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// ProtocolVersion is the version of the hello and the stream protocol after it. The agent
// speaks it; the server also accepts agents back to MinProtocolVersion and answers with
// the agent's version. From version 2 every stream starts with a Kind byte.
const ProtocolVersion = 2

// MinProtocolVersion is the oldest agent protocol the server accepts: version 1 streams
// carry Docker only.
const MinProtocolVersion = 1

// Kind is the first byte of a stream (protocol 2): what the agent connects it to.
type Kind byte

const (
	// KindDocker streams go to the agent's Docker API.
	KindDocker Kind = 0
	// KindControl streams carry HTTP to the agent's control service.
	KindControl Kind = 1
)

// ErrNoControl is returned by OpenControl when the agent speaks protocol 1.
var ErrNoControl = errors.New("the agent has no control service (protocol 1): update it")

// Hello is what each side tells the other at connect.
type Hello struct {
	// Version is the sender's nudge version.
	Version string `json:"version"`
	// Hostname is the agent's host name; the server sends none.
	Hostname string `json:"hostname,omitempty"`
}

// Config tunes a session; zero fields take the defaults.
type Config struct {
	// HandshakeTimeout bounds the hello exchange (default 10 s).
	HandshakeTimeout time.Duration
	// KeepAliveInterval is how often each side pings the other (default 15 s).
	KeepAliveInterval time.Duration
	// KeepAliveTimeout is how long a ping or write may wait before the session is
	// considered dead and closed (default 10 s).
	KeepAliveTimeout time.Duration
	Log              *slog.Logger
	// Protocol is the version the agent side speaks (default ProtocolVersion); tests set
	// it to act as an older agent.
	Protocol int
}

func (c Config) withDefaults() Config {
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = 10 * time.Second
	}
	if c.KeepAliveInterval <= 0 {
		c.KeepAliveInterval = 15 * time.Second
	}
	if c.KeepAliveTimeout <= 0 {
		c.KeepAliveTimeout = 10 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return c
}

// ErrClosed is returned by Open and Accept once the session is closed.
var ErrClosed = errors.New("tunnel closed")

// Session is one live tunnel. It closes when either side closes it, the connection
// breaks, keepalives fail or the context given to Accept or Connect ends; then every
// stream on it fails.
type Session struct {
	peer     Hello
	protocol int
	mux      *yamux.Session
	conn     net.Conn
	// stop cancels the connection's context; done is closed once everything is torn down.
	stop context.CancelFunc
	done chan struct{}

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// newSession starts yamux on conn. The server side (client=true) opens streams; the agent
// side accepts them. conn is closed when ctx ends or the session closes.
func newSession(ctx context.Context, conn net.Conn, client bool, peer Hello, protocol int, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	ymCfg := yamux.DefaultConfig()
	ymCfg.EnableKeepAlive = true
	ymCfg.KeepAliveInterval = cfg.KeepAliveInterval
	ymCfg.ConnectionWriteTimeout = cfg.KeepAliveTimeout
	ymCfg.LogOutput = nil
	ymCfg.Logger = slog.NewLogLogger(cfg.Log.Handler(), slog.LevelWarn)

	var mux *yamux.Session
	var err error
	if client {
		mux, err = yamux.Client(conn, ymCfg)
	} else {
		mux, err = yamux.Server(conn, ymCfg)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("start yamux: %w", err)
	}
	ctx, stop := context.WithCancel(ctx)
	s := &Session{peer: peer, protocol: protocol, mux: mux, conn: conn, stop: stop, done: make(chan struct{})}
	s.wg.Go(func() {
		select {
		case <-ctx.Done():
		case <-mux.CloseChan():
		}
		_ = s.mux.Close()
		_ = s.conn.Close()
		stop()
	})
	if client {
		// The agent has no business opening streams to the server; refuse them so they
		// can't pile up in the accept backlog.
		s.wg.Go(func() {
			refused := 0
			for {
				st, err := mux.AcceptStream()
				if err != nil {
					if refused > 0 {
						cfg.Log.Warn("tunnel: refused streams opened by the agent", "agent", peer.Hostname, "count", refused)
					}
					return
				}
				if refused == 0 {
					cfg.Log.Warn("tunnel: refusing streams opened by the agent", "agent", peer.Hostname)
				}
				refused++
				_ = st.Close()
			}
		})
	}
	go func() {
		s.wg.Wait()
		close(s.done)
	}()
	return s, nil
}

// Peer is the other side's hello.
func (s *Session) Peer() Hello { return s.peer }

// Protocol is the protocol version the session speaks.
func (s *Session) Protocol() int { return s.protocol }

// Open opens a stream to the agent's Docker API; only the server side calls it.
func (s *Session) Open(ctx context.Context) (net.Conn, error) {
	return s.open(ctx, KindDocker)
}

// OpenControl opens a stream to the agent's control service; ErrNoControl if the agent
// speaks protocol 1. Only the server side calls it.
func (s *Session) OpenControl(ctx context.Context) (net.Conn, error) {
	if s.protocol < 2 {
		return nil, ErrNoControl
	}
	return s.open(ctx, KindControl)
}

func (s *Session) open(ctx context.Context, kind Kind) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	st, err := s.mux.OpenStream()
	if err != nil {
		return nil, s.streamErr("open stream", err)
	}
	if s.protocol >= 2 {
		// yamux buffers the write; it doesn't wait for the agent.
		if _, err := st.Write([]byte{byte(kind)}); err != nil {
			_ = st.Close()
			return nil, s.streamErr("open stream", err)
		}
	}
	return st, nil
}

// ReadKind reads the Kind byte a protocol 2 stream starts with, waiting at most timeout;
// the agent side calls it on each accepted stream.
func ReadKind(conn net.Conn, timeout time.Duration) (Kind, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, fmt.Errorf("read stream kind: %w", err)
	}
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return 0, fmt.Errorf("read stream kind: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return 0, fmt.Errorf("read stream kind: %w", err)
	}
	k := Kind(b[0])
	if k != KindDocker && k != KindControl {
		return 0, fmt.Errorf("read stream kind: unknown kind %d", k)
	}
	return k, nil
}

// Accept waits for the server's next stream; only the agent side calls it. On protocol 2
// the stream starts with its Kind (see ReadKind).
func (s *Session) Accept() (net.Conn, error) {
	st, err := s.mux.AcceptStream()
	if err != nil {
		return nil, s.streamErr("accept stream", err)
	}
	return st, nil
}

// Close closes the session and every stream on it, and waits until its goroutines exit.
func (s *Session) Close() error {
	s.closeOnce.Do(s.stop)
	<-s.done
	return nil
}

// Done is closed once the session is closed, for whatever reason.
func (s *Session) Done() <-chan struct{} { return s.done }

// streamErr reports any failure of a closed session as ErrClosed; yamux passes on
// whatever broke the connection.
func (s *Session) streamErr(op string, err error) error {
	if s.mux.IsClosed() || errors.Is(err, yamux.ErrSessionShutdown) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("%s: %w", op, ErrClosed)
	}
	return fmt.Errorf("%s: %w", op, err)
}
