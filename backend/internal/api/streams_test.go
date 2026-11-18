package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
)

// testRecheck is the stream recheck interval in these tests.
const testRecheck = 20 * time.Millisecond

func shortRecheck(o *Options) { o.StreamRecheck = testRecheck }

// dialWith opens a WebSocket on srv's path with header.
func dialWith(ctx context.Context, t *testing.T, srvURL, path string, header http.Header) *websocket.Conn {
	t.Helper()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srvURL, "http")+path, &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

// closedWith reads ws until the server closes it and returns the close status, or -1
// when ctx ends first.
func closedWith(ctx context.Context, ws *websocket.Conn) websocket.StatusCode {
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if ctx.Err() != nil {
				return -1
			}
			return websocket.CloseStatus(err)
		}
	}
}

// An exec session ends soon after its credential stops being valid or its user's role
// changes; it goes on while nothing changes.
func TestExecEndsWhenAccessIsRevoked(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// token opens the session with an API token instead of a session cookie.
		token bool
		// revoke is what the admin (or eve herself) does; nil changes nothing.
		revoke func(t *testing.T, xt execTest, eve *http.Cookie, eveID, tokenID int64)
	}{
		{name: "nothing changes"},
		{name: "disabled", revoke: func(t *testing.T, xt execTest, _ *http.Cookie, eveID, _ int64) {
			mustDo(t, xt.h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", eveID), cookie: xt.cookie, body: `{"disabled":true}`})
		}},
		{name: "demoted", revoke: func(t *testing.T, xt execTest, _ *http.Cookie, eveID, _ int64) {
			mustDo(t, xt.h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", eveID), cookie: xt.cookie, body: `{"role":"user"}`})
		}},
		{name: "deleted", revoke: func(t *testing.T, xt execTest, _ *http.Cookie, eveID, _ int64) {
			mustDo(t, xt.h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/users/%d", eveID), cookie: xt.cookie})
		}},
		{name: "logged out", revoke: func(t *testing.T, xt execTest, eve *http.Cookie, _, _ int64) {
			mustDo(t, xt.h, request{method: "POST", path: "/api/v1/auth/logout", cookie: eve})
		}},
		{name: "token deleted", token: true, revoke: func(t *testing.T, xt execTest, eve *http.Cookie, _, tokenID int64) {
			mustDo(t, xt.h, request{method: "DELETE", path: fmt.Sprintf("/api/v1/tokens/%d", tokenID), cookie: eve})
		}},
		{name: "token's user disabled", token: true, revoke: func(t *testing.T, xt execTest, _ *http.Cookie, eveID, _ int64) {
			mustDo(t, xt.h, request{method: "PATCH", path: fmt.Sprintf("/api/v1/users/%d", eveID), cookie: xt.cookie, body: `{"disabled":true}`})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			xt := newExecTestOpts(t, shortRecheck)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			eve := addUser(t, xt.h, xt.cookie, "eve", auth.RoleAdmin)
			var eveID int64
			for _, a := range listUsers(t, xt.h, xt.cookie) {
				if a.Username == "eve" {
					eveID = a.ID
				}
			}
			header := http.Header{}
			var tokenID int64
			if tt.token {
				tok := createToken(t, xt.h, eve, "ci")
				tokenID = tok.ID
				header.Set("Authorization", "Bearer "+tok.Token)
			} else {
				header.Set("Cookie", eve.String())
			}
			ws := dialWith(ctx, t, xt.srv.URL, xt.path("local", "web/exec"), header)
			var e *dockertest.Exec
			select {
			case e = <-xt.fake.Execs:
				t.Cleanup(func() { _ = e.Conn.Close() })
			case <-ctx.Done():
				t.Fatal("no exec attached")
			}

			if tt.revoke == nil {
				// Several rechecks pass and the session still pipes input.
				time.Sleep(10 * testRecheck)
				if err := ws.Write(ctx, websocket.MessageBinary, []byte("id\n")); err != nil {
					t.Fatal(err)
				}
				buf := make([]byte, 8)
				if n, err := e.Conn.Read(buf); err != nil || string(buf[:n]) != "id\n" {
					t.Fatalf("process read %q %v, want %q", buf[:n], err, "id\n")
				}
				return
			}
			tt.revoke(t, xt, eve, eveID, tokenID)
			if got := closedWith(ctx, ws); got != websocket.StatusPolicyViolation {
				t.Errorf("close status = %v, want %v", got, websocket.StatusPolicyViolation)
			}
		})
	}
}

// A user's event stream ends once an admin takes the environment away.
func TestEventsEndWhenEnvironmentIsWithdrawn(t *testing.T) {
	t.Parallel()
	ot := newOwnTestOpts(t, shortRecheck)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	header.Set("Cookie", ot.bob.String())
	ws := dialWith(ctx, t, ot.srv.URL, ot.path(ot.local, "/events"), header)

	mustDo(t, ot.h, request{method: "PUT", path: fmt.Sprintf("/api/v1/users/%d/environments", ot.bobID), cookie: ot.admin,
		body: `{"environments":[]}`})
	if got := closedWith(ctx, ws); got != websocket.StatusPolicyViolation {
		t.Errorf("close status = %v, want %v", got, websocket.StatusPolicyViolation)
	}
}

// mustDo runs req and fails the test unless it succeeds (2xx).
func mustDo(t *testing.T, h http.Handler, req request) {
	t.Helper()
	if rec := do(t, h, req); rec.Code < 200 || rec.Code > 299 {
		t.Fatalf("%s %s = %d %s", req.method, req.path, rec.Code, rec.Body)
	}
}
