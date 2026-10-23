package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestHandler() http.Handler {
	web := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "web")
	})
	return New(Options{Log: slog.New(slog.DiscardHandler), Version: "v1.0.0", Web: web})
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
		// The /api/ catch-all answers wrong methods too, so clients always get JSON.
		{name: "health wrong method", method: http.MethodPost, path: "/api/v1/health", wantCode: 404, wantBody: `{"error":"not found"}`, wantJSON: true},
		{name: "unknown api path", method: http.MethodGet, path: "/api/v1/nope", wantCode: 404, wantBody: `{"error":"not found"}`, wantJSON: true},
		{name: "web ui", method: http.MethodGet, path: "/containers", wantCode: 200, wantBody: "web"},
	}
	h := newTestHandler()
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
