package api

import (
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t)
	tests := []struct {
		name, method, path string
	}{
		{"public API route", "GET", "/api/v1/health"},
		{"unauthenticated API route", "GET", "/api/v1/users"},
		{"unknown API route", "GET", "/api/v1/nope"},
		{"web UI", "GET", "/"},
		{"SPA route", "GET", "/containers"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := do(t, h, request{method: tt.method, path: tt.path}).Header()
			for name, want := range map[string]string{
				"Content-Security-Policy": contentSecurityPolicy,
				"X-Content-Type-Options":  "nosniff",
				"X-Frame-Options":         "DENY",
				"Referrer-Policy":         "same-origin",
			} {
				if got.Get(name) != want {
					t.Errorf("%s = %q, want %q", name, got.Get(name), want)
				}
			}
		})
	}
}

// The policy's point: no script but the app's own files, in no frame.
func TestContentSecurityPolicy(t *testing.T) {
	t.Parallel()
	directives := map[string]string{}
	for d := range strings.SplitSeq(contentSecurityPolicy, ";") {
		name, value, _ := strings.Cut(strings.TrimSpace(d), " ")
		directives[name] = value
	}
	for name, want := range map[string]string{
		"default-src":     "'none'",
		"script-src":      "'self'",
		"connect-src":     "'self'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
	} {
		if directives[name] != want {
			t.Errorf("%s = %q, want %q", name, directives[name], want)
		}
	}
}
