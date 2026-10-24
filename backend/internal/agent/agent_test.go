package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
)

const joinToken = "nudge_join_valid"

// fakeClock fires every After at once and moves its time by the delay, which it records.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	delays []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delays = append(c.delays, d)
	c.t = c.t.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.t
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) Delays() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.delays...)
}

// fakeServer is the server side: enrollment with one join token, and the tunnel endpoint,
// which hands each session to the test.
type fakeServer struct {
	t        *testing.T
	srv      *httptest.Server
	sessions chan *tunnel.Session

	mu         sync.Mutex
	tokenUsed  bool
	creds      *Credentials // the valid ones
	enrolls    int
	failNext   int // connects to answer 503 before accepting
	connects   int
	lastHello  tunnel.Hello
	issueCount int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{t: t, sessions: make(chan *tunnel.Session, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /prefix/api/v1/agents/enroll", fs.enroll)
	mux.HandleFunc("GET /prefix/api/v1/agents/connect", fs.connect)
	fs.srv = httptest.NewServer(mux)
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeServer) url() *url.URL {
	u, err := url.Parse(fs.srv.URL + "/prefix")
	if err != nil {
		fs.t.Fatal(err)
	}
	return u
}

func (fs *fakeServer) enroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JoinToken string `json:"join_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.enrolls++
	if req.JoinToken != joinToken || fs.tokenUsed {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	fs.tokenUsed = true
	fs.issueCount++
	fs.creds = &Credentials{EnvironmentID: 2, AgentID: "agt_" + strings.Repeat("x", fs.issueCount), Secret: "nudge_agent_secret"}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(fs.creds)
}

func (fs *fakeServer) connect(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	fs.mu.Lock()
	fs.connects++
	valid := ok && fs.creds != nil && id == fs.creds.AgentID && secret == fs.creds.Secret
	fail := fs.failNext > 0
	if fail && valid {
		fs.failNext--
	}
	fs.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	sess, err := tunnel.Accept(r.Context(), ws, tunnel.Hello{Version: "server-test"}, tunnel.Config{})
	if err != nil {
		fs.t.Errorf("tunnel accept: %v", err)
		return
	}
	fs.mu.Lock()
	fs.lastHello = sess.Peer()
	fs.mu.Unlock()
	fs.sessions <- sess
	<-sess.Done()
}

func (fs *fakeServer) session(t *testing.T) *tunnel.Session {
	t.Helper()
	select {
	case s := <-fs.sessions:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not connect")
		return nil
	}
}

func (fs *fakeServer) setCreds(c *Credentials) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.creds = c
}

func (fs *fakeServer) counts() (enrolls, connects int) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.enrolls, fs.connects
}

// fakeDocker is the agent's local Docker API.
func fakeDocker(t *testing.T) func(ctx context.Context) (net.Conn, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "docker:"+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	var d net.Dialer
	return func(ctx context.Context) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
}

type running struct {
	clock *fakeClock
	dir   string
	done  chan error
	stop  context.CancelFunc
}

// start runs an agent against fs until the test ends; opts fields left zero get test
// defaults.
func start(t *testing.T, fs *fakeServer, opts Options) *running {
	t.Helper()
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	if opts.DataDir == "" {
		opts.DataDir = t.TempDir()
	}
	opts.ServerURL = fs.url()
	opts.Dial = fakeDocker(t)
	opts.Version = "agent-test"
	opts.Hostname = "nas"
	opts.Clock = clock
	opts.Jitter = func(n int64) int64 { return n - 1 } // the full delay
	opts.Log = slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{clock: clock, dir: opts.DataDir, done: make(chan error, 1), stop: cancel}
	go func() { r.done <- New(opts).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.done:
			// Tests that expect a FatalError checked it with wait.
			var fatal *FatalError
			if err != nil && !errors.As(err, &fatal) {
				t.Errorf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancel")
		}
	})
	return r
}

func (r *running) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err // for the cleanup
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// get sends one HTTP request over a new stream and returns the body.
func get(t *testing.T, sess *tunnel.Session, path string) string {
	t.Helper()
	return getOn(t, sess.Open, path)
}

// getOn is get over a stream from open (sess.Open or sess.OpenControl).
func getOn(t *testing.T, open func(ctx context.Context) (net.Conn, error), path string) string {
	t.Helper()
	conn, err := open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: docker\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestControlStreams(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	control := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "control:"+r.URL.Path)
	})
	start(t, fs, Options{JoinToken: joinToken, Control: control})
	sess := fs.session(t)
	// Streams of both kinds, interleaved, each reach their own side.
	for range 3 {
		if got := getOn(t, sess.OpenControl, "/v1/stacks/web"); got != "control:/v1/stacks/web" {
			t.Errorf("control stream answered %q", got)
		}
		if got := get(t, sess, "/_ping"); got != "docker:/_ping" {
			t.Errorf("Docker stream answered %q", got)
		}
	}
}

func TestControlStreamsRefusedWithoutHandler(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	start(t, fs, Options{JoinToken: joinToken})
	sess := fs.session(t)
	conn, err := sess.OpenControl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, "GET /v1/stacks HTTP/1.1\r\nHost: agent\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if n, err := conn.Read(make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("read = %d, %v; want the stream closed", n, err)
	}
	// Docker still works.
	if got := get(t, sess, "/_ping"); got != "docker:/_ping" {
		t.Errorf("Docker stream answered %q", got)
	}
}

func TestFirstConnectEnrollsAndServesDocker(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	r := start(t, fs, Options{JoinToken: joinToken})
	sess := fs.session(t)

	if got := get(t, sess, "/_ping"); got != "docker:/_ping" {
		t.Errorf("body = %q, want the fake Docker API's answer", got)
	}
	// Streams run concurrently.
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Go(func() {
			path := "/v1/containers/" + strings.Repeat("c", i+1)
			if got := get(t, sess, path); got != "docker:"+path {
				t.Errorf("body = %q", got)
			}
		})
	}
	wg.Wait()

	fs.mu.Lock()
	hello := fs.lastHello
	fs.mu.Unlock()
	if hello.Version != "agent-test" || hello.Hostname != "nas" {
		t.Errorf("hello = %+v", hello)
	}
	path := filepath.Join(r.dir, "agent.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("agent.json mode = %o, want 600", perm)
	}
	saved, err := loadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AgentID != "agt_x" || saved.Secret != "nudge_agent_secret" || saved.EnvironmentID != 2 {
		t.Errorf("saved = %+v", saved)
	}

	r.stop()
	if err := r.wait(t); err != nil {
		t.Errorf("Run = %v, want nil after cancel", err)
	}
	select {
	case <-sess.Done():
	case <-time.After(5 * time.Second):
		t.Error("session still open after the agent stopped")
	}
}

func TestSavedCredentialsSkipEnroll(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	creds := &Credentials{EnvironmentID: 3, AgentID: "agt_saved", Secret: "nudge_agent_saved"}
	fs.setCreds(creds)
	dir := t.TempDir()
	if err := saveCredentials(filepath.Join(dir, "agent.json"), creds); err != nil {
		t.Fatal(err)
	}
	// The used join token stays in the agent's environment after the first start.
	start(t, fs, Options{DataDir: dir, JoinToken: joinToken})
	sess := fs.session(t)
	if got := get(t, sess, "/info"); got != "docker:/info" {
		t.Errorf("body = %q", got)
	}
	if enrolls, _ := fs.counts(); enrolls != 0 {
		t.Errorf("enrolls = %d, want 0 with saved credentials", enrolls)
	}
}

func TestRefusedCredentials(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		joinToken string
		usedToken bool
		wantErr   string // empty: the agent enrolls again and connects
	}{
		{name: "join token enrolls again", joinToken: joinToken},
		{name: "no join token", wantErr: "refused the agent credentials"},
		{name: "used join token", joinToken: joinToken, usedToken: true, wantErr: "refused the join token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fs := newFakeServer(t)
			fs.tokenUsed = tt.usedToken
			dir := t.TempDir()
			stale := &Credentials{EnvironmentID: 2, AgentID: "agt_revoked", Secret: "nudge_agent_revoked"}
			if err := saveCredentials(filepath.Join(dir, "agent.json"), stale); err != nil {
				t.Fatal(err)
			}
			r := start(t, fs, Options{DataDir: dir, JoinToken: tt.joinToken})
			if tt.wantErr == "" {
				fs.session(t)
				saved, err := loadCredentials(filepath.Join(dir, "agent.json"))
				if err != nil || saved.AgentID != "agt_x" {
					t.Errorf("saved = %+v, %v; want the new credentials", saved, err)
				}
				return
			}
			err := r.wait(t)
			var fatal *FatalError
			if !errors.As(err, &fatal) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Run = %v, want a FatalError containing %q", err, tt.wantErr)
			}
			if delays := r.clock.Delays(); len(delays) != 0 {
				t.Errorf("retried %d times, want none", len(delays))
			}
		})
	}
}

func TestEnrollFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		joinToken string
		file      string // agent.json content, if any
		wantErr   string
	}{
		{name: "no credentials, no join token", wantErr: "no join token"},
		{name: "bad join token", joinToken: "nudge_join_wrong", wantErr: "refused the join token"},
		{name: "corrupt agent.json", joinToken: joinToken, file: "{not json", wantErr: "agent.json"},
		{name: "incomplete agent.json", joinToken: joinToken, file: `{"agent_id": "agt_x"}`, wantErr: "missing agent_id or agent_secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fs := newFakeServer(t)
			dir := t.TempDir()
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			r := start(t, fs, Options{DataDir: dir, JoinToken: tt.joinToken})
			err := r.wait(t)
			var fatal *FatalError
			if !errors.As(err, &fatal) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Run = %v, want a FatalError containing %q", err, tt.wantErr)
			}
			if tt.file == "" {
				if _, err := os.Stat(filepath.Join(dir, "agent.json")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("agent.json exists after a failed enroll (err %v)", err)
				}
			}
		})
	}
}

func TestReconnectBackoff(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	fs.failNext = 8
	r := start(t, fs, Options{JoinToken: joinToken})

	// Eight refused connects, then a session the server drops at once: still backing off.
	_ = fs.session(t).Close()
	// A session that stays up longer than the longest backoff starts over at 1 s.
	sess := fs.session(t)
	get(t, sess, "/_ping") // the agent is serving, so it noted when the session started
	r.clock.Advance(2 * time.Minute)
	_ = sess.Close()
	fs.session(t)

	s := time.Second
	want := []time.Duration{s, 2 * s, 4 * s, 8 * s, 16 * s, 32 * s, 60 * s, 60 * s, 60 * s, s}
	got := r.clock.Delays()
	if len(got) != len(want) {
		t.Fatalf("delays = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("delays = %v, want %v", got, want)
			break
		}
	}
	if enrolls, _ := fs.counts(); enrolls != 1 {
		t.Errorf("enrolls = %d, want 1: reconnects reuse the credentials", enrolls)
	}
}

func TestBackoffJitter(t *testing.T) {
	t.Parallel()
	low := func(int64) int64 { return 0 }
	high := func(n int64) int64 { return n - 1 }
	tests := []struct {
		attempt int
		lo, hi  time.Duration
	}{
		{0, 500 * time.Millisecond, time.Second},
		{1, time.Second, 2 * time.Second},
		{5, 16 * time.Second, 32 * time.Second},
		{6, 30 * time.Second, time.Minute},
		{100, 30 * time.Second, time.Minute},
	}
	for _, tt := range tests {
		if got := backoff(tt.attempt, low); got != tt.lo {
			t.Errorf("backoff(%d) low = %v, want %v", tt.attempt, got, tt.lo)
		}
		if got := backoff(tt.attempt, high); got != tt.hi {
			t.Errorf("backoff(%d) high = %v, want %v", tt.attempt, got, tt.hi)
		}
	}
}

func TestPipeClosesBothSides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// closeServer closes the server's end of the stream; otherwise Docker's end.
		closeServer bool
	}{
		{name: "server closes", closeServer: true},
		{name: "docker closes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stream, server := net.Pipe()
			dockerConn, dockerd := net.Pipe()
			done := make(chan struct{})
			go func() {
				pipe(stream, dockerConn)
				close(done)
			}()

			// Data flows both ways.
			go func() { _, _ = server.Write([]byte("ping")) }()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(dockerd, buf); err != nil || string(buf) != "ping" {
				t.Fatalf("docker read %q, %v", buf, err)
			}
			go func() { _, _ = dockerd.Write([]byte("pong")) }()
			if _, err := io.ReadFull(server, buf); err != nil || string(buf) != "pong" {
				t.Fatalf("server read %q, %v", buf, err)
			}

			closed, other := server, dockerd
			if !tt.closeServer {
				closed, other = dockerd, server
			}
			_ = closed.Close()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("pipe did not return")
			}
			if _, err := other.Read(buf); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("other side read err = %v, want it closed", err)
			}
			_ = other.Close()
		})
	}
}

func TestDockerUnreachableClosesStream(t *testing.T) {
	t.Parallel()
	fs := newFakeServer(t)
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	a := New(Options{
		ServerURL: fs.url(), JoinToken: joinToken, DataDir: t.TempDir(), Clock: clock,
		Dial: func(context.Context) (net.Conn, error) { return nil, errors.New("no docker") },
	})
	go func() { done <- a.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	}()
	sess := fs.session(t)
	conn, err := sess.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.WriteString(conn, "GET /_ping HTTP/1.1\r\nHost: docker\r\n\r\n")
	if _, err := conn.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read err = %v, want EOF: the agent closes a stream it can't serve", err)
	}
	// The session survives a failed stream.
	select {
	case <-sess.Done():
		t.Error("session closed")
	default:
	}
}
