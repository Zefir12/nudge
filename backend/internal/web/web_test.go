package web

import (
	"bytes"
	"compress/gzip"
	"io"
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

func TestHandlerGzip(t *testing.T) {
	t.Parallel()
	bigJS := strings.Repeat("console.log('nudge');\n", 200)
	bigHTML := "<!doctype html>" + strings.Repeat("<p>app</p>", 200)
	built := fstest.MapFS{
		"index.html":      {Data: []byte(bigHTML)},
		"assets/app-1.js": {Data: []byte(bigJS)},
		"assets/tiny.css": {Data: []byte("a{}")},
		"assets/logo.png": {Data: []byte(strings.Repeat("x", 4096))},
	}
	h := Handler(built)
	tests := []struct {
		name     string
		method   string
		path     string
		accept   string
		wantGzip bool
		wantVary bool
		wantType string
		wantBody string
	}{
		{name: "asset gzip", path: "/assets/app-1.js", accept: "gzip, deflate, br", wantGzip: true, wantVary: true, wantType: "text/javascript; charset=utf-8", wantBody: bigJS},
		{name: "asset no accept", path: "/assets/app-1.js", wantVary: true, wantBody: bigJS},
		{name: "asset gzip refused", path: "/assets/app-1.js", accept: "gzip;q=0, br", wantVary: true, wantBody: bigJS},
		{name: "asset star", path: "/assets/app-1.js", accept: "*", wantGzip: true, wantVary: true, wantBody: bigJS},
		{name: "star but gzip refused", path: "/assets/app-1.js", accept: "*, gzip;q=0", wantVary: true, wantBody: bigJS},
		{name: "asset q weight", path: "/assets/app-1.js", accept: "br;q=1.0, GZIP; q=0.5", wantGzip: true, wantVary: true, wantBody: bigJS},
		{name: "spa route gzip", path: "/containers/abc", accept: "gzip", wantGzip: true, wantVary: true, wantType: "text/html; charset=utf-8", wantBody: bigHTML},
		{name: "root plain", path: "/", wantVary: true, wantType: "text/html; charset=utf-8", wantBody: bigHTML},
		{name: "small file plain", path: "/assets/tiny.css", accept: "gzip", wantBody: "a{}"},
		{name: "image plain", path: "/assets/logo.png", accept: "gzip", wantBody: strings.Repeat("x", 4096)},
		{name: "head gzip", method: http.MethodHead, path: "/assets/app-1.js", accept: "gzip", wantGzip: true, wantVary: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, tt.path, nil)
			if tt.accept != "" {
				req.Header.Set("Accept-Encoding", tt.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200", rec.Code)
			}
			gotGzip := rec.Header().Get("Content-Encoding") == "gzip"
			if gotGzip != tt.wantGzip {
				t.Errorf("Content-Encoding = %q, want gzip %v", rec.Header().Get("Content-Encoding"), tt.wantGzip)
			}
			if got := rec.Header().Get("Vary") == "Accept-Encoding"; got != tt.wantVary {
				t.Errorf("Vary = %q, want Accept-Encoding %v", rec.Header().Get("Vary"), tt.wantVary)
			}
			if tt.wantType != "" && rec.Header().Get("Content-Type") != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", rec.Header().Get("Content-Type"), tt.wantType)
			}
			body := rec.Body.Bytes()
			if method == http.MethodHead {
				if len(body) != 0 {
					t.Errorf("HEAD body has %d bytes", len(body))
				}
				return
			}
			if gotGzip {
				zr, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatalf("gzip reader: %v", err)
				}
				if body, err = io.ReadAll(zr); err != nil {
					t.Fatalf("gunzip: %v", err)
				}
				if rec.Body.Len() >= len(body) {
					t.Errorf("compressed %d bytes, not smaller than %d", rec.Body.Len(), len(body))
				}
			}
			if string(body) != tt.wantBody {
				t.Errorf("body differs: got %d bytes, want %d", len(body), len(tt.wantBody))
			}
		})
	}
}

func TestAcceptsGzip(t *testing.T) {
	t.Parallel()
	tests := map[string]bool{
		"":                false,
		"identity":        false,
		"gzip":            true,
		"x-gzip":          true,
		"deflate, gzip":   true,
		"gzip;q=0":        false,
		"gzip; q=0.0":     false,
		"gzip;q=0.001":    true,
		"gzip;q=bad":      false,
		"*":               true,
		"*;q=0":           false,
		"*, gzip;q=0":     false,
		"gzip;q=0, *":     false,
		"br;q=1, *;q=0.1": true,
		"gzipped, br":     false,
	}
	for header, want := range tests {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}
