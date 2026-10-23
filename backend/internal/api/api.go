// Package api is nudge's HTTP API under /api/v1, the only interface the web UI uses.
// Everything else is handed to the web UI handler.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// Options are the API's dependencies.
type Options struct {
	Log     *slog.Logger
	Version string
	// Web serves every path outside /api/.
	Web http.Handler
}

// New returns the server's root handler: the API plus the web UI.
func New(opts Options) http.Handler {
	s := &server{log: opts.Log, version: opts.Version}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("/api/", s.notFound)
	mux.Handle("/", opts.Web)
	return mux
}

type server struct {
	log     *slog.Logger
	version string
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	s.writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: s.version})
}

func (s *server) notFound(w http.ResponseWriter, _ *http.Request) {
	s.writeError(w, http.StatusNotFound, "not found")
}

// errorResponse is the body of every API error.
type errorResponse struct {
	Error string `json:"error"`
}

func (s *server) writeError(w http.ResponseWriter, code int, msg string) {
	s.writeJSON(w, code, errorResponse{Error: msg})
}

func (s *server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.log.Warn("writing response", "err", err)
	}
}
