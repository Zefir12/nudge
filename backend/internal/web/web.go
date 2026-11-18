// Package web serves the built frontend (frontend/, copied into dist/ by `make web-build`
// and the Dockerfile) as a single-page app: unknown paths get index.html so the Vue
// router can handle them. Text files are gzipped once when the handler is built and
// sent compressed to clients that accept gzip.
package web

import (
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Dist is the embedded frontend build. It holds only .gitkeep until the frontend is built.
func Dist() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// dist is embedded at compile time, so this can't happen.
		panic(err)
	}
	return sub
}

// minGzipSize is the smallest file worth compressing: below it the gzip header and the
// extra headers eat most of the saving.
const minGzipSize = 1024

// gzipTypes are the extensions compressed; images, fonts and archives already are.
var gzipTypes = map[string]bool{
	".html": true, ".js": true, ".mjs": true, ".css": true, ".svg": true,
	".json": true, ".map": true, ".txt": true, ".webmanifest": true, ".wasm": true,
}

// Handler serves files from dist, falling back to index.html for paths that aren't
// files. Without an index.html (frontend not built) it answers 503 with a hint.
func Handler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	gz := compressAll(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" && isFile(dist, name) {
			if strings.HasPrefix(name, "assets/") {
				// Vite puts a content hash in every asset's name.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			if serveGzip(w, r, gz, name) {
				return
			}
			files.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		if serveGzip(w, r, gz, "index.html") {
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if errors.Is(err, fs.ErrNotExist) {
			w.Header().Del("Cache-Control")
			http.Error(w, "web UI not built: run `make web-build`", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			w.Header().Del("Cache-Control")
			http.Error(w, "reading index.html", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
}

// serveGzip answers with the gzipped copy of name if there is one and the client takes
// gzip. It reports whether it answered. A file with a gzipped copy varies by
// Accept-Encoding either way, so caches keep both.
func serveGzip(w http.ResponseWriter, r *http.Request, gz map[string][]byte, name string) bool {
	data, ok := gz[name]
	// Without a known type the plain file server would sniff it, which needs the raw bytes.
	ctype := mime.TypeByExtension(path.Ext(name))
	if !ok || ctype == "" {
		return false
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return false
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Encoding", "gzip")
	// ServeContent handles HEAD, conditional and range requests; ranges then apply to the
	// gzipped bytes, which is what Content-Encoding means.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	return true
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip: listed with a
// non-zero q value (`*` counts unless gzip is refused by name).
func acceptsGzip(header string) bool {
	star := false
	for part := range strings.SplitSeq(header, ",") {
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.ToLower(strings.TrimSpace(coding))
		if coding != "gzip" && coding != "x-gzip" && coding != "*" {
			continue
		}
		allowed := qAllows(params)
		if coding == "*" {
			star = allowed
			continue
		}
		return allowed
	}
	return star
}

// qAllows reads the q parameter of one Accept-Encoding entry; anything but q=0 allows.
func qAllows(params string) bool {
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return err == nil && q > 0
	}
	return true
}

// compressAll gzips every text file in dist worth compressing, once, so requests spend
// no CPU on it. A file that can't be read or doesn't shrink is served as it is.
func compressAll(dist fs.FS) map[string][]byte {
	out := make(map[string][]byte)
	_ = fs.WalkDir(dist, ".", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && gzipTypes[strings.ToLower(path.Ext(name))] {
			if data, ok := compressFile(dist, name); ok {
				out[name] = data
			}
		}
		return nil // a directory that can't be read is just skipped
	})
	return out
}

// compressFile returns name gzipped, or false if it can't be read, is too small or
// doesn't shrink.
func compressFile(dist fs.FS, name string) ([]byte, bool) {
	data, err := fs.ReadFile(dist, name)
	if err != nil || len(data) < minGzipSize {
		return nil, false
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, false
	}
	if _, err := zw.Write(data); err != nil {
		return nil, false
	}
	if err := zw.Close(); err != nil || buf.Len() >= len(data) {
		return nil, false
	}
	return buf.Bytes(), true
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
