package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel"
	"git.zefirlabs.net/zefir/nudge/backend/internal/tunnel/tunneltest"
)

// fakeDockerAPI answers the Docker API calls of the phase 1 endpoints with one object
// each, as a remote host's daemon would through an agent.
func fakeDockerAPI() http.Handler {
	versioned := regexp.MustCompile(`^/v[0-9.]+/`)
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
	reply("GET /version", system.VersionResponse{Version: "29.0.0", APIVersion: client.MaxAPIVersion})
	reply("GET /containers/json", []container.Summary{{ID: "abc123", Names: []string{"/web"}, State: container.StateRunning}})
	reply("GET /containers/web/json", container.InspectResponse{ID: "abc123", Name: "/web", State: &container.State{Status: container.StateRunning}})
	reply("GET /images/json", []image.Summary{{ID: "sha256:img1", RepoTags: []string{"nginx:1.29"}}})
	reply("GET /images/nginx/json", image.InspectResponse{ID: "sha256:img1", RepoTags: []string{"nginx:1.29"}})
	reply("GET /volumes", volume.ListResponse{Volumes: []volume.Volume{{Name: "data", Driver: "local"}}})
	reply("GET /volumes/data", volume.Volume{Name: "data", Driver: "local"})
	reply("GET /networks", []network.Summary{{Network: network.Network{ID: "n1", Name: "front"}}})
	reply("GET /networks/front", network.Inspect{Network: network.Network{ID: "n1", Name: "front"}})
	for _, pattern := range []string{"POST /containers/web/start", "POST /containers/web/stop", "POST /containers/web/restart", "POST /containers/web/kill", "DELETE /containers/web"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = versioned.ReplaceAllString(r.URL.Path, "/")
		mux.ServeHTTP(w, r)
	})
}

// agentTest is an envTest served over HTTP with one enrolled agent environment.
type agentTest struct {
	envTest
	url     string
	envID   int64
	agentID string
	header  http.Header
}

func newAgentTest(t *testing.T) agentTest {
	t.Helper()
	return newAgentTestOpts(t, nil)
}

// newAgentTestOpts is newAgentTest with mod changing the API's options, if not nil.
func newAgentTestOpts(t *testing.T, mod func(*Options)) agentTest {
	t.Helper()
	et := newEnvTestOpts(t, map[string]*dockertest.Fake{"local": {}}, mod)
	srv := httptest.NewServer(et.h)
	t.Cleanup(srv.Close)
	created := createAgentEnv(t, et, "nas")
	rec := enroll(t, et, created.JoinToken.Token)
	var creds enrollResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}
	return agentTest{envTest: et, url: srv.URL + "/api/v1/agents/connect", envID: created.ID, agentID: creds.AgentID, header: basicAuth(creds.AgentID, creds.AgentSecret)}
}

func basicAuth(user, password string) http.Header {
	return http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))}}
}

// connect starts a fake agent and waits until the server lists its environment online.
func (at agentTest) connect(t *testing.T) *tunneltest.Agent {
	t.Helper()
	a, err := tunneltest.Connect(t, at.url, at.header, tunnel.Hello{Version: "v9.9.9", Hostname: "nas"}, fakeDockerAPI())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	at.waitOnline(t, true)
	return a
}

// waitOnline polls the environment until its online status is want: the server registers
// a tunnel just after the agent's handshake returns, and notices a close a little later.
func (at agentTest) waitOnline(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := do(t, at.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d", at.envID), cookie: at.cookie})
		var env environmentResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("environment: %v (%s)", err, rec.Body)
		}
		if env.Online == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("environment online = %v, want %v", env.Online, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitDone(t *testing.T, a *tunneltest.Agent) {
	t.Helper()
	select {
	case <-a.Session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("agent tunnel still open")
	}
}

func TestAgentConnectAuth(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	tests := []struct {
		name   string
		header http.Header
	}{
		{"no credentials", nil},
		{"wrong secret", basicAuth(at.agentID, "nudge_agent_wrong")},
		{"unknown agent", basicAuth("agt_unknown", "nudge_agent_wrong")},
		{"bearer token", http.Header{"Authorization": {"Bearer nudge_agent_wrong"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tunneltest.Connect(t, at.url, tt.header, tunnel.Hello{Version: "v1"}, fakeDockerAPI())
			var se *tunneltest.StatusError
			if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
				t.Fatalf("err = %v, want a 401", err)
			}
		})
	}
}

func TestAgentEnvironmentDocker(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	env := fmt.Sprintf("/api/v1/environments/%d", at.envID)
	if rec := do(t, at.h, request{method: "GET", path: env + "/containers", cookie: at.cookie}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before connect: code = %d (%s), want 503", rec.Code, rec.Body)
	}
	at.connect(t)
	if got := agentOf(t, at.envTest, at.envID); got == nil || got.Version != "v9.9.9" {
		t.Errorf("agent = %+v, want version v9.9.9", got)
	}
	tests := []struct {
		path string
		want string
	}{
		{"", `"remote-host"`},
		{"/containers", `"abc123"`},
		{"/containers/web", `"abc123"`},
		{"/images", `"sha256:img1"`},
		{"/images/nginx", `"sha256:img1"`},
		{"/volumes", `"data"`},
		{"/volumes/data", `"data"`},
		{"/networks", `"front"`},
		{"/networks/front", `"front"`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			rec := do(t, at.h, request{method: "GET", path: env + tt.path, cookie: at.cookie})
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tt.want) {
				t.Errorf("code = %d, body %s, want 200 with %s", rec.Code, rec.Body, tt.want)
			}
		})
	}
	for _, action := range []request{
		{method: "POST", path: env + "/containers/web/start"},
		{method: "POST", path: env + "/containers/web/stop?timeout=1"},
		{method: "POST", path: env + "/containers/web/restart"},
		{method: "POST", path: env + "/containers/web/kill?signal=TERM"},
		{method: "DELETE", path: env + "/containers/web?force=true"},
	} {
		t.Run(action.method+" "+action.path, func(t *testing.T) {
			t.Parallel()
			action.cookie = at.cookie
			if rec := do(t, at.h, action); rec.Code != http.StatusNoContent {
				t.Errorf("code = %d (%s), want 204", rec.Code, rec.Body)
			}
		})
	}
}

func TestAgentDisconnect(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	a := at.connect(t)
	a.Close()
	at.waitOnline(t, false)
	rec := do(t, at.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d/containers", at.envID), cookie: at.cookie})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("after disconnect: code = %d (%s), want 503", rec.Code, rec.Body)
	}
	if got := agentOf(t, at.envTest, at.envID); got == nil || got.Version != "v9.9.9" {
		t.Errorf("agent = %+v, want the version kept", got)
	}
	// The agent comes back.
	at.connect(t)
}

func TestAgentReconnectReplaces(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	first := at.connect(t)
	at.connect(t)
	waitDone(t, first)
	// The second tunnel stays and serves.
	rec := do(t, at.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d/containers", at.envID), cookie: at.cookie})
	if rec.Code != http.StatusOK {
		t.Errorf("code = %d (%s), want 200 through the new tunnel", rec.Code, rec.Body)
	}
}

func TestAgentRevokeDisconnects(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	a := at.connect(t)
	rec := do(t, at.h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/environments/%d/credentials", at.envID), cookie: at.cookie})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: code = %d (%s)", rec.Code, rec.Body)
	}
	waitDone(t, a)
	at.waitOnline(t, false)
	_, err := tunneltest.Connect(t, at.url, at.header, tunnel.Hello{Version: "v9.9.9"}, fakeDockerAPI())
	var se *tunneltest.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusUnauthorized {
		t.Errorf("reconnect after revoke: err = %v, want a 401", err)
	}
}

func TestAgentLink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		proxies  proxies
		header   http.Header
		wantAddr string
	}{
		{name: "direct", wantAddr: "127.0.0.1"},
		// The test server's peer is 127.0.0.1, a trusted proxy here.
		{name: "behind a trusted proxy", proxies: testProxies, header: http.Header{"X-Forwarded-For": {"203.0.113.9"}}, wantAddr: "203.0.113.9"},
		{name: "forwarded header not trusted", header: http.Header{"X-Forwarded-For": {"203.0.113.9"}}, wantAddr: "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			at := newAgentTestOpts(t, func(o *Options) { o.TrustedProxies = tt.proxies })
			if got := agentOf(t, at.envTest, at.envID); got.Address != "" || got.RTTMs != nil {
				t.Errorf("before connect: agent = %+v, want no address or round trip", got)
			}
			header := at.header.Clone()
			for k, v := range tt.header {
				header[k] = v
			}
			a, err := tunneltest.Connect(t, at.url, header, tunnel.Hello{Version: "v9.9.9", Hostname: "nas"}, fakeDockerAPI())
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			// The first ping goes out right after connect.
			deadline := time.Now().Add(5 * time.Second)
			got := agentOf(t, at.envTest, at.envID)
			for got.RTTMs == nil {
				if time.Now().After(deadline) {
					t.Fatalf("agent = %+v, want a round trip", got)
				}
				time.Sleep(10 * time.Millisecond)
				got = agentOf(t, at.envTest, at.envID)
			}
			if got.Address != tt.wantAddr || *got.RTTMs < 0 {
				t.Errorf("listed agent = %+v (rtt %v), want address %s", got, *got.RTTMs, tt.wantAddr)
			}
			rec := do(t, at.h, request{method: "GET", path: fmt.Sprintf("/api/v1/environments/%d", at.envID), cookie: at.cookie})
			var env environmentDetailResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("environment: %v (%s)", err, rec.Body)
			}
			if env.Agent == nil || env.Agent.Address != tt.wantAddr || env.Agent.RTTMs == nil {
				t.Errorf("environment agent = %+v, want address %s and a round trip", env.Agent, tt.wantAddr)
			}
			_ = a.Session.Close()
			at.waitOnline(t, false)
			if got := agentOf(t, at.envTest, at.envID); got.Address != "" || got.RTTMs != nil {
				t.Errorf("after disconnect: agent = %+v, want no address or round trip", got)
			}
		})
	}
}

// A user (not admin) sees the round trip but not the agent's address.
func TestAgentLinkUser(t *testing.T) {
	t.Parallel()
	at := newAgentTest(t)
	bob := addUser(t, at.h, at.cookie, "bob", auth.RoleUser)
	var bobID int64
	for _, u := range listUsers(t, at.h, at.cookie) {
		if u.Username == "bob" {
			bobID = u.ID
		}
	}
	rec := do(t, at.h, request{method: "PUT", path: fmt.Sprintf("/api/v1/users/%d/environments", bobID), cookie: at.cookie,
		body: fmt.Sprintf(`{"environments":[%d]}`, at.envID)})
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("grant = %d %s", rec.Code, rec.Body)
	}
	at.connect(t)
	deadline := time.Now().Add(5 * time.Second)
	for agentOf(t, at.envTest, at.envID).RTTMs == nil {
		if time.Now().After(deadline) {
			t.Fatal("no round trip")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, path := range []string{"/api/v1/environments", fmt.Sprintf("/api/v1/environments/%d", at.envID)} {
		rec := do(t, at.h, request{method: "GET", path: path, cookie: bob})
		body := rec.Body.String()
		if rec.Code != http.StatusOK || strings.Contains(body, `"address"`) || !strings.Contains(body, `"rtt_ms"`) {
			t.Errorf("GET %s as a user = %d %s, want rtt_ms without address", path, rec.Code, body)
		}
	}
}
