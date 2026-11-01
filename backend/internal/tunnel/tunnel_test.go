package tunnel

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fast keeps keepalives quick so dead peers are noticed within a test.
var fast = Config{HandshakeTimeout: 2 * time.Second, KeepAliveInterval: 50 * time.Millisecond, KeepAliveTimeout: 200 * time.Millisecond}

// checkLeaks fails the test if goroutines started during it are still running once its
// cleanups ran. Tests using it must not be parallel.
func checkLeaks(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before {
			if time.Now().After(deadline) {
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				t.Errorf("%d goroutines leaked:\n%s", runtime.NumGoroutine()-before, buf[:n])
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

// wsPair is a server and an agent session over a WebSocket on an httptest server.
type wsPair struct {
	server, agent *Session
	// drop closes the TCP connections under the server, as a network failure would.
	drop func()
}

// recordingListener remembers accepted connections; httptest forgets hijacked ones.
type recordingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *recordingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *recordingListener) drop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
}

func newWSPair(ctx context.Context, t *testing.T, cfg Config) wsPair {
	t.Helper()
	accepted := make(chan *Session, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			close(accepted)
			return
		}
		s, err := Accept(ctx, ws, Hello{Version: "server-v1"}, cfg)
		if err != nil {
			t.Errorf("Accept: %v", err)
			close(accepted)
			return
		}
		accepted <- s
		<-s.Done()
	}))
	ln := &recordingListener{Listener: srv.Listener}
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	ws := dial(t, srv)
	agent, err := Connect(ctx, ws, Hello{Version: "agent-v1", Hostname: "host1"}, cfg)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	server := <-accepted
	if server == nil {
		t.FailNow()
	}
	t.Cleanup(func() {
		_ = agent.Close()
		_ = server.Close()
	})
	return wsPair{server: server, agent: agent, drop: ln.drop}
}

func dial(t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	ws, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp.Body != nil {
		_ = resp.Body.Close()
	}
	return ws
}

// newPipePair is a server and an agent session over net.Pipe, without WebSocket.
func newPipePair(ctx context.Context, t *testing.T, cfg Config) (server, agent *Session) {
	t.Helper()
	a, b := net.Pipe()
	server, err := newSession(ctx, a, true, Hello{Hostname: "host1"}, ProtocolVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent, err = newSession(ctx, b, false, Hello{}, ProtocolVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.Close()
		_ = server.Close()
	})
	return server, agent
}

// echo answers every stream the agent accepts with what it reads after the stream kind
// (on protocol 2), until the session ends.
func echo(t *testing.T, agent *Session) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := agent.Accept()
			if err != nil {
				if !errors.Is(err, ErrClosed) {
					t.Errorf("Accept: %v", err)
				}
				return
			}
			wg.Go(func() {
				defer func() { _ = c.Close() }()
				if agent.Protocol() >= 2 {
					if _, err := ReadKind(c, 5*time.Second); err != nil {
						t.Errorf("ReadKind: %v", err)
						return
					}
				}
				_, _ = io.Copy(c, c)
			})
		}
	})
	t.Cleanup(func() {
		_ = agent.Close()
		wg.Wait()
	})
}

func roundTrip(ctx context.Context, s *Session, payload []byte) error {
	c, err := s.Open(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	errc := make(chan error, 1)
	go func() {
		_, err := c.Write(payload)
		errc <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if err := <-errc; err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return errors.New("echo differs from what was sent")
	}
	return nil
}

func waitDone(t *testing.T, name string, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%s session still open", name)
	}
}

func TestHandshake(t *testing.T) {
	checkLeaks(t)
	p := newWSPair(t.Context(), t, fast)
	if got := p.server.Peer(); got != (Hello{Version: "agent-v1", Hostname: "host1"}) {
		t.Errorf("server sees peer %+v", got)
	}
	if got := p.agent.Peer(); got != (Hello{Version: "server-v1"}) {
		t.Errorf("agent sees peer %+v", got)
	}
	echo(t, p.agent)
	if err := roundTrip(t.Context(), p.server, []byte("ping")); err != nil {
		t.Fatal(err)
	}
}

func TestHandshakeOldAgent(t *testing.T) {
	t.Parallel()
	accepted := make(chan *Session, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("websocket accept: %v", err)
			close(accepted)
			return
		}
		s, err := Accept(t.Context(), ws, Hello{Version: "server-v2"}, fast)
		if err != nil {
			t.Errorf("Accept: %v", err)
			close(accepted)
			return
		}
		accepted <- s
		<-s.Done()
	}))
	defer srv.Close()
	agent, err := connect(t.Context(), dial(t, srv), Hello{Version: "agent-v1"}, 1, fast)
	if err != nil {
		t.Fatalf("a protocol 1 agent was refused: %v", err)
	}
	server := <-accepted
	if server == nil {
		t.FailNow()
	}
	t.Cleanup(func() {
		_ = agent.Close()
		_ = server.Close()
	})
	if server.Protocol() != 1 || agent.Protocol() != 1 {
		t.Errorf("protocols = %d (server), %d (agent), want 1", server.Protocol(), agent.Protocol())
	}
	if _, err := server.OpenControl(t.Context()); !errors.Is(err, ErrNoControl) {
		t.Errorf("OpenControl = %v, want ErrNoControl", err)
	}
	// Protocol 1 streams carry no kind byte.
	echo(t, agent)
	if err := roundTrip(t.Context(), server, []byte("ping")); err != nil {
		t.Fatal(err)
	}
}

func TestStreamKinds(t *testing.T) {
	t.Parallel()
	server, agent := newPipePair(t.Context(), t, fast)
	for _, want := range []Kind{KindDocker, KindControl} {
		open := server.Open
		if want == KindControl {
			open = server.OpenControl
		}
		c, err := open(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		a, err := agent.Accept()
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadKind(a, 5*time.Second)
		if err != nil || got != want {
			t.Errorf("ReadKind = %d, %v; want %d", got, err, want)
		}
		// The payload follows the kind, with the read deadline cleared.
		b := make([]byte, 1)
		if _, err := io.ReadFull(a, b); err != nil || string(b) != "x" {
			t.Errorf("payload = %q, %v", b, err)
		}
		_ = c.Close()
		_ = a.Close()
	}

	t.Run("unknown kind", func(t *testing.T) {
		a, b := net.Pipe()
		defer func() { _ = a.Close() }()
		go func() { _, _ = b.Write([]byte{7}) }()
		if _, err := ReadKind(a, 5*time.Second); err == nil || !strings.Contains(err.Error(), "unknown kind 7") {
			t.Errorf("ReadKind = %v, want unknown kind", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		a, b := net.Pipe()
		defer func() { _ = a.Close(); _ = b.Close() }()
		if _, err := ReadKind(a, 10*time.Millisecond); err == nil {
			t.Error("ReadKind on a silent stream returned no error")
		}
	})
}

func TestHandshakeRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		protocol   int
		hello      Hello
		wantServer string
	}{
		{name: "newer agent", protocol: ProtocolVersion + 1, hello: Hello{Version: "v9"}, wantServer: "tunnel protocol mismatch: peer speaks version 3, this side speaks 2"},
		{name: "no protocol", protocol: 0, hello: Hello{Version: "v1"}, wantServer: "protocol mismatch"},
		{name: "control character in hostname", protocol: ProtocolVersion, hello: Hello{Hostname: "host\n1"}, wantServer: "invalid hostname"},
		{name: "long version", protocol: ProtocolVersion, hello: Hello{Version: strings.Repeat("v", 65)}, wantServer: "invalid version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			serverErr := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				s, err := Accept(r.Context(), ws, Hello{Version: "server-v1"}, fast)
				if s != nil {
					_ = s.Close()
				}
				serverErr <- err
			}))
			defer srv.Close()
			s, err := connect(t.Context(), dial(t, srv), tt.hello, tt.protocol, fast)
			if s != nil {
				_ = s.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "server refused the agent: ") || !strings.Contains(err.Error(), tt.wantServer) {
				t.Errorf("agent error = %v, want the server's reason %q", err, tt.wantServer)
			}
			if err := <-serverErr; err == nil || !strings.Contains(err.Error(), tt.wantServer) {
				t.Errorf("server error = %v, want %q", err, tt.wantServer)
			}
		})
	}
}

func TestHandshakeBadMessage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		typ  websocket.MessageType
		msg  string
		want string
	}{
		{name: "not JSON", typ: websocket.MessageText, msg: "hello?", want: "not a hello message"},
		{name: "binary", typ: websocket.MessageBinary, msg: `{"protocol":1}`, want: "not a text message"},
		{name: "too big", typ: websocket.MessageText, msg: `{"version":"` + strings.Repeat("v", maxHelloSize) + `"}`, want: "read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			serverErr := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				_, err = Accept(r.Context(), ws, Hello{}, fast)
				serverErr <- err
			}))
			defer srv.Close()
			ws := dial(t, srv)
			defer func() { _ = ws.CloseNow() }()
			if err := ws.Write(t.Context(), tt.typ, []byte(tt.msg)); err != nil {
				t.Fatal(err)
			}
			if err := <-serverErr; err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("server error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHandshakeTimeout(t *testing.T) {
	t.Parallel()
	serverErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		_, err = Accept(r.Context(), ws, Hello{}, Config{HandshakeTimeout: 50 * time.Millisecond})
		serverErr <- err
	}))
	defer srv.Close()
	ws := dial(t, srv) // and never says hello
	defer func() { _ = ws.CloseNow() }()
	select {
	case err := <-serverErr:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("server error = %v, want a deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Accept did not time out")
	}
}

func TestManyStreams(t *testing.T) {
	for _, transport := range []string{"pipe", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			checkLeaks(t)
			// Production timeouts: with fast's 200 ms write timeout, writes queued behind 100
			// streams on a slow runner under -race close the session.
			cfg := Config{HandshakeTimeout: fast.HandshakeTimeout}
			var server, agent *Session
			if transport == "pipe" {
				server, agent = newPipePair(t.Context(), t, cfg)
			} else {
				p := newWSPair(t.Context(), t, cfg)
				server, agent = p.server, p.agent
			}
			echo(t, agent)
			var wg sync.WaitGroup
			errs := make(chan error, 100)
			for i := range 100 {
				wg.Go(func() {
					payload := make([]byte, 1000+i*600) // up to ~60 KB, beyond one frame
					_, _ = rand.Read(payload)
					if err := roundTrip(t.Context(), server, payload); err != nil {
						errs <- fmt.Errorf("stream %d: %w", i, err)
					}
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Error(err)
			}
		})
	}
}

func TestClose(t *testing.T) {
	tests := []struct {
		name  string
		close func(p wsPair, cancel context.CancelFunc)
	}{
		{name: "server closes", close: func(p wsPair, _ context.CancelFunc) { _ = p.server.Close() }},
		{name: "agent closes", close: func(p wsPair, _ context.CancelFunc) { _ = p.agent.Close() }},
		{name: "context ends", close: func(_ wsPair, cancel context.CancelFunc) { cancel() }},
		{name: "connection drops", close: func(p wsPair, _ context.CancelFunc) { p.drop() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkLeaks(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := newWSPair(ctx, t, fast)
			// Streams the agent holds open without answering, as a slow Docker call would.
			var streams []net.Conn
			for range 3 {
				c, err := p.server.Open(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = c.Close() }()
				streams = append(streams, c)
			}
			for range 3 {
				c, err := p.agent.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = c.Close() }()
			}

			tt.close(p, cancel)
			waitDone(t, "server", p.server)
			waitDone(t, "agent", p.agent)
			for i, c := range streams {
				if _, err := c.Read(make([]byte, 1)); err == nil {
					t.Errorf("stream %d still readable", i)
				}
			}
			if _, err := p.server.Open(t.Context()); !errors.Is(err, ErrClosed) {
				t.Errorf("Open after close: %v, want ErrClosed", err)
			}
			if _, err := p.agent.Accept(); !errors.Is(err, ErrClosed) {
				t.Errorf("Accept after close: %v, want ErrClosed", err)
			}
		})
	}
}

func TestKeepAliveClosesDeadPeer(t *testing.T) {
	checkLeaks(t)
	a, b := net.Pipe()
	// The peer reads everything and never answers, like a host that froze.
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = io.Copy(io.Discard, b) })
	t.Cleanup(func() {
		_ = b.Close()
		wg.Wait()
	})
	s, err := newSession(t.Context(), a, true, Hello{}, ProtocolVersion, fast)
	if err != nil {
		t.Fatal(err)
	}
	waitDone(t, "server", s)
}

func TestServerRefusesAgentStreams(t *testing.T) {
	checkLeaks(t)
	_, agent := newPipePair(t.Context(), t, fast)
	c, err := agent.mux.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read = %v, want EOF from the server closing the stream", err)
	}
}

func TestPing(t *testing.T) {
	t.Parallel()
	server, agent := newPipePair(t.Context(), t, fast)
	rtt, err := server.Ping()
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if rtt <= 0 {
		t.Errorf("Ping rtt = %v, want > 0", rtt)
	}
	_ = agent.Close()
	waitDone(t, "server", server)
	if _, err := server.Ping(); !errors.Is(err, ErrClosed) {
		t.Errorf("Ping after close: %v, want ErrClosed", err)
	}
}
