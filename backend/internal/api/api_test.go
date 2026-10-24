package api

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
	"git.zefirlabs.net/zefir/nudge/backend/internal/store"
)

// newTestHandler returns the API over a fresh database with no users, and its auth
// service (cheap argon2 params, fixed clock).
func newTestHandler(t *testing.T) (http.Handler, *auth.Service) {
	t.Helper()
	h, svc, _ := newTestHandlerWith(t, func(environments.Environment) (docker.Client, error) {
		return &dockertest.Fake{}, nil
	})
	return h, svc
}

// newTestHandlerWith is newTestHandler with the Docker clients that connect returns,
// and the database, to add environments.
func newTestHandlerWith(t *testing.T, connect environments.ConnectFunc) (http.Handler, *auth.Service, *sql.DB) {
	t.Helper()
	return newTestHandlerOpts(t, connect, nil)
}

// newTestHandlerOpts is newTestHandlerWith with mod changing the options, if not nil.
func newTestHandlerOpts(t *testing.T, connect environments.ConnectFunc, mod func(*Options)) (http.Handler, *auth.Service, *sql.DB) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Unix(1_700_000_000, 0)
	svc := auth.New(auth.Options{
		DB:     st.DB(),
		Now:    func() time.Time { return now },
		Params: auth.Params{Memory: 64, Time: 1, Threads: 1},
	})
	web := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "web")
	})
	envs := environments.New(environments.Options{DB: st.DB(), Now: func() time.Time { return now }, Connect: connect})
	t.Cleanup(func() { _ = envs.Close() })
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	regs := registries.New(registries.Options{DB: st.DB(), Box: box, Now: func() time.Time { return now }})
	aud := audit.New(audit.Options{DB: st.DB(), Now: func() time.Time { return now }})
	opts := Options{Log: slog.New(slog.DiscardHandler), Version: "v1.0.0", Auth: svc, Envs: envs, Registries: regs, Audit: aud, Web: web}
	if mod != nil {
		mod(&opts)
	}
	return New(opts), svc, st.DB()
}

func TestRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
		wantBody string
		wantJSON bool
	}{
		{name: "health", method: http.MethodGet, path: "/api/v1/health", wantCode: 200, wantBody: `{"status":"ok","version":"v1.0.0"}`, wantJSON: true},
		// The /api/ catch-all answers wrong methods too, so clients always get JSON;
		// without a session that answer is 401, so route names don't leak.
		{name: "health wrong method", method: http.MethodPost, path: "/api/v1/health", wantCode: 401, wantBody: `{"error":"authentication required"}`, wantJSON: true},
		{name: "unknown api path", method: http.MethodGet, path: "/api/v1/nope", wantCode: 401, wantBody: `{"error":"authentication required"}`, wantJSON: true},
		{name: "me needs a session", method: http.MethodGet, path: "/api/v1/auth/me", wantCode: 401, wantBody: `{"error":"authentication required"}`, wantJSON: true},
		{name: "setup status", method: http.MethodGet, path: "/api/v1/setup", wantCode: 200, wantBody: `{"needed":true}`, wantJSON: true},
		{name: "web ui", method: http.MethodGet, path: "/containers", wantCode: 200, wantBody: "web"},
	}
	h, _ := newTestHandler(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := strings.TrimSpace(rec.Body.String()); tt.wantBody != "" && got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			if ct := rec.Header().Get("Content-Type"); tt.wantJSON && ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}
