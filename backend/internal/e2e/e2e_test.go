// Package e2e_test runs a server and an agent together in one process: the real API,
// store, auth and environments on httptest, and a real agent whose Docker socket is a
// fake Docker API on a unix socket.
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/agent"
	"git.zefirlabs.net/zefir/nudge/backend/internal/api"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// fakeDocker serves the Docker API calls the test makes on a unix socket in dir and
// returns its unix:// URL.
func fakeDocker(t *testing.T, dir string) string {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(pattern string, v any) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		})
	}
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Api-Version", client.MaxAPIVersion)
		_, _ = w.Write([]byte("OK"))
	})
	reply("GET /info", system.Info{Name: "remote-host", ServerVersion: "29.0.0"})
	reply("GET /containers/json", []container.Summary{{ID: "abc123", Names: []string{"/web"}, State: container.StateRunning}})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The client prefixes paths with /v1.xx once it negotiated a version.
		if rest, ok := strings.CutPrefix(r.URL.Path, "/v"); ok {
			if _, after, found := strings.Cut(rest, "/"); found {
				r.URL.Path = "/" + after
			}
		}
		mux.ServeHTTP(w, r)
	})

	path := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return "unix://" + path
}

// newServer wires the server like `nudge serve` and serves it on httptest. Its local
// environment points at a socket nobody listens on, so only the agent environment exists.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	authSvc := auth.New(auth.Options{DB: st.DB()})
	connect, err := environments.LocalConnect("unix://" + filepath.Join(dir, "no-docker.sock"))
	if err != nil {
		t.Fatal(err)
	}
	envs := environments.New(environments.Options{DB: st.DB(), Connect: connect})
	// Registered before srv.Close below, so it runs after it (cleanups are LIFO).
	t.Cleanup(func() { _ = envs.Close() })
	if _, err := envs.EnsureLocal(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.New(api.Options{Log: slog.New(slog.DiscardHandler), Auth: authSvc, Envs: envs, Web: http.NotFoundHandler()}))
	t.Cleanup(srv.Close)
	return srv
}

// user is an API client logged in through setup.
type user struct {
	t    *testing.T
	base string
	http *http.Client
}

func setup(t *testing.T, base string) *user {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u := &user{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	u.do(http.MethodPost, "/api/v1/setup", map[string]string{"username": "admin", "password": "correct horse"}, http.StatusCreated, nil)
	return u
}

// do sends body as JSON, checks the status and decodes the answer into out (if not nil).
func (u *user) do(method, path string, body any, want int, out any) {
	u.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			u.t.Fatal(err)
		}
		r = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(u.t.Context(), method, u.base+path, r)
	if err != nil {
		u.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.http.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		u.t.Fatal(err)
	}
	if resp.StatusCode != want {
		u.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			u.t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

// status is the answer's status code, for polling.
func (u *user) status(path string) int {
	u.t.Helper()
	req, err := http.NewRequestWithContext(u.t.Context(), http.MethodGet, u.base+path, nil)
	if err != nil {
		u.t.Fatal(err)
	}
	resp, err := u.http.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// conns records the agent's connections to the server so the test can cut them.
type conns struct {
	mu     sync.Mutex
	dialed int
	open   []net.Conn
}

func (c *conns) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dialed++
	c.open = append(c.open, conn)
	return conn, nil
}

// drop closes every connection, as a network failure would.
func (c *conns) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.open {
		_ = conn.Close()
	}
	c.open = nil
}

func (c *conns) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dialed
}

// instantClock waits for no backoff, so reconnects take no real time.
type instantClock struct{}

func (instantClock) Now() time.Time { return time.Now() }

func (instantClock) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

// eventually polls cond until it holds or 5 s pass.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type environment struct {
	ID        int64 `json:"id"`
	Online    bool  `json:"online"`
	JoinToken struct {
		Token string `json:"token"`
	} `json:"join_token"`
	Agent *struct {
		Enrolled bool   `json:"enrolled"`
		Version  string `json:"version"`
	} `json:"agent"`
}

func TestServerAndAgent(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	admin := setup(t, srv.URL)

	var created environment
	admin.do(http.MethodPost, "/api/v1/environments", map[string]string{"name": "nas", "kind": "agent"}, http.StatusCreated, &created)
	if created.JoinToken.Token == "" {
		t.Fatal("no join token")
	}
	envPath := fmt.Sprintf("/api/v1/environments/%d", created.ID)
	if got := admin.status(envPath + "/containers"); got != http.StatusServiceUnavailable {
		t.Fatalf("containers before the agent connected: status %d, want 503", got)
	}

	agentDir := t.TempDir()
	dial, err := docker.LocalDialer(fakeDocker(t, agentDir))
	if err != nil {
		t.Fatal(err)
	}
	serverURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cs := &conns{}
	a := agent.New(agent.Options{
		ServerURL:  serverURL,
		JoinToken:  created.JoinToken.Token,
		DataDir:    agentDir,
		Dial:       dial,
		Version:    "v9.9.9",
		Hostname:   "nas",
		HTTPClient: &http.Client{Transport: &http.Transport{DialContext: cs.dial}},
		Clock:      instantClock{},
		Jitter:     func(int64) int64 { return 0 },
	})
	ctx, cancel := context.WithCancel(t.Context())
	// runErr is set before done closes.
	var runErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		runErr = a.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	})

	// The list, unlike the environment itself, answers while the environment is offline.
	get := func() environment {
		t.Helper()
		var list []environment
		admin.do(http.MethodGet, "/api/v1/environments", nil, http.StatusOK, &list)
		for _, env := range list {
			if env.ID == created.ID {
				return env
			}
		}
		t.Fatalf("environment %d not listed", created.ID)
		return environment{}
	}
	online := func() bool { return get().Online }
	listContainers := func() {
		t.Helper()
		var list []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		admin.do(http.MethodGet, envPath+"/containers", nil, http.StatusOK, &list)
		if len(list) != 1 || list[0].ID != "abc123" {
			t.Fatalf("containers = %+v, want the fake daemon's one", list)
		}
	}

	t.Log("enroll and connect")
	eventually(t, "the agent to come online", online)
	env := get()
	if env.Agent == nil || !env.Agent.Enrolled || env.Agent.Version != "v9.9.9" {
		t.Fatalf("agent = %+v, want enrolled with v9.9.9", env.Agent)
	}
	listContainers()

	t.Log("reconnect after the connection drops")
	before := cs.count()
	cs.drop()
	eventually(t, "the agent to reconnect", func() bool {
		return cs.count() > before && online() && admin.status(envPath+"/containers") == http.StatusOK
	})
	listContainers()

	t.Log("revoke")
	admin.do(http.MethodDelete, envPath+"/credentials", nil, http.StatusNoContent, nil)
	select {
	case <-done:
		var fatal *agent.FatalError
		if !errors.As(runErr, &fatal) {
			t.Fatalf("agent after revoke: %v, want a FatalError", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent kept running after its credentials were revoked")
	}
	eventually(t, "the environment to go offline", func() bool { return !online() })
	if got := admin.status(envPath + "/containers"); got != http.StatusServiceUnavailable {
		t.Fatalf("containers after revoke: status %d, want 503", got)
	}
}
