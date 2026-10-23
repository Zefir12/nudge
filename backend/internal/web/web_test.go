package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	t.Parallel()
	built := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html>app")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/sub/.keep": {Data: nil},
	}
	tests := []struct {
		name      string
		fs        fstest.MapFS
		path      string
		wantCode  int
		wantBody  string
		wantCache string
	}{
		{name: "root", fs: built, path: "/", wantCode: 200, wantBody: "app", wantCache: "no-cache"},
		{name: "index.html", fs: built, path: "/index.html", wantCode: 200, wantBody: "app", wantCache: "no-cache"},
		{name: "spa route", fs: built, path: "/containers/abc", wantCode: 200, wantBody: "app", wantCache: "no-cache"},
		{name: "directory", fs: built, path: "/assets/sub", wantCode: 200, wantBody: "app", wantCache: "no-cache"},
		{name: "asset", fs: built, path: "/assets/app-1.js", wantCode: 200, wantBody: "console.log", wantCache: "public, max-age=31536000, immutable"},
		{name: "static file", fs: built, path: "/favicon.svg", wantCode: 200, wantBody: "<svg/>"},
		{name: "traversal stays inside", fs: built, path: "/../../etc/passwd", wantCode: 200, wantBody: "app", wantCache: "no-cache"},
		{name: "not built", fs: fstest.MapFS{".gitkeep": {}}, path: "/", wantCode: 503, wantBody: "make web-build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			Handler(tt.fs).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.wantCache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.wantCache)
			}
		})
	}
}

func TestDistEmbedded(t *testing.T) {
	t.Parallel()
	if _, err := Dist().Open("."); err != nil {
		t.Fatalf("open embedded dist: %v", err)
	}
}
