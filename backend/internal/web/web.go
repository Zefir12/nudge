// Package web serves the built frontend (frontend/, copied into dist/ by `make web-build`
// and the Dockerfile) as a single-page app: unknown paths get index.html so the Vue
// router can handle them.
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
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

// Handler serves files from dist, falling back to index.html for paths that aren't
// files. Without an index.html (frontend not built) it answers 503 with a hint.
func Handler(dist fs.FS) http.Handler {
	files := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != "index.html" && isFile(dist, name) {
			if strings.HasPrefix(name, "assets/") {
				// Vite puts a content hash in every asset's name.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(dist, "index.html")
		if errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "web UI not built: run `make web-build`", http.StatusServiceUnavailable)
			return
		}
		if err != nil {
			http.Error(w, "reading index.html", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}
