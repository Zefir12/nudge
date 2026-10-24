// Package api is nudge's HTTP API under /api/v1, the only interface the web UI uses.
// Everything else is handed to the web UI handler.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

// Options are the API's dependencies.
type Options struct {
	Log     *slog.Logger
	Version string
	Auth    *auth.Service
	Envs    *environments.Service
	// Web serves every path outside /api/.
	Web http.Handler
}

// publicRoutes need no session; every other pattern, including the /api/ catch-all,
// does, so a route added without thought is protected.
var publicRoutes = map[string]bool{
	"GET /api/v1/health":      true,
	"GET /api/v1/setup":       true,
	"POST /api/v1/setup":      true,
	"POST /api/v1/auth/login": true,
	// Logout clears the cookie even when the session has already expired.
	"POST /api/v1/auth/logout": true,
}

// New returns the server's root handler: the API plus the web UI.
func New(opts Options) http.Handler {
	s := &server{log: opts.Log, version: opts.Version, auth: opts.Auth, envs: opts.Envs}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/health", s.health)
	api.HandleFunc("GET /api/v1/setup", s.setupStatus)
	api.HandleFunc("POST /api/v1/setup", s.setup)
	api.HandleFunc("POST /api/v1/auth/login", s.login)
	api.HandleFunc("POST /api/v1/auth/logout", s.logout)
	api.HandleFunc("GET /api/v1/auth/me", s.me)
	api.HandleFunc("GET /api/v1/environments", s.listEnvironments)
	api.HandleFunc("GET /api/v1/environments/{env}", s.getEnvironment)
	api.HandleFunc("/api/", s.notFound)

	mux := http.NewServeMux()
	mux.Handle("/api/", s.protect(api))
	mux.Handle("/", opts.Web)
	return mux
}

type server struct {
	log     *slog.Logger
	version string
	auth    *auth.Service
	envs    *environments.Service
}

// protect refuses cross-origin writes (Sec-Fetch-Site, else Origin against Host) and
// requires a session for every route outside publicRoutes.
func (s *server) protect(api *http.ServeMux) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.writeError(w, http.StatusForbidden, "cross-origin request refused")
	}))
	return cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := api.Handler(r); publicRoutes[pattern] {
			api.ServeHTTP(w, r)
			return
		}
		u, err := s.auth.SessionUser(r.Context(), sessionToken(r))
		if errors.Is(err, auth.ErrNoSession) {
			s.writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if err != nil {
			s.internalError(w, "session lookup", err)
			return
		}
		api.ServeHTTP(w, r.WithContext(withUser(r.Context(), u)))
	}))
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

// internalError logs err and answers 500 without details.
func (s *server) internalError(w http.ResponseWriter, msg string, err error) {
	s.log.Error(msg, "err", err)
	s.writeError(w, http.StatusInternalServerError, "internal error")
}

// maxBodyBytes bounds JSON request bodies.
const maxBodyBytes = 64 << 10

// decodeJSON reads r's body into v, answering 400 itself when it fails.
func (s *server) decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(v); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %s", jsonErrorKind(err)))
		return false
	}
	return true
}

// jsonErrorKind describes a decode error without echoing the body.
func jsonErrorKind(err error) string {
	var tooBig *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &tooBig):
		return "too large"
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
	default:
		return "malformed"
	}
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
