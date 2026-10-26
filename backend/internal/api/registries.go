package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/registries"
)

// registryResponse never includes the password.
type registryResponse struct {
	ID        int64           `json:"id"`
	Name      string          `json:"name"`
	Address   string          `json:"address"`
	Username  string          `json:"username"`
	Kind      registries.Kind `json:"kind"`
	Owners    []string        `json:"owners"`
	PlainHTTP bool            `json:"plain_http"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// registryRequest creates or updates a registry; on update a missing or null password
// keeps the stored one. A missing kind is generic.
type registryRequest struct {
	Name      string          `json:"name"`
	Address   string          `json:"address"`
	Username  string          `json:"username"`
	Password  *string         `json:"password"`
	Kind      registries.Kind `json:"kind"`
	Owners    []string        `json:"owners"`
	PlainHTTP bool            `json:"plain_http"`
}

func toRegistryResponse(r registries.Registry) registryResponse {
	return registryResponse{
		ID: r.ID, Name: r.Name, Address: r.Address, Username: r.Username,
		Kind: r.Kind, Owners: r.Owners, PlainHTTP: r.PlainHTTP,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (s *server) listRegistries(w http.ResponseWriter, r *http.Request) {
	list, err := s.registries.List(r.Context())
	if err != nil {
		s.internalError(w, "list registries", err)
		return
	}
	resp := make([]registryResponse, len(list))
	for i, reg := range list {
		resp[i] = toRegistryResponse(reg)
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) getRegistry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.registryID(w, r)
	if !ok {
		return
	}
	reg, err := s.registries.Get(r.Context(), id)
	if s.registryError(w, "get registry", err) {
		return
	}
	s.writeJSON(w, http.StatusOK, toRegistryResponse(reg))
}

func (s *server) createRegistry(w http.ResponseWriter, r *http.Request) {
	var req registryRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	reg, err := s.registries.Create(r.Context(), registries.Input(req))
	if s.registryError(w, "create registry", err) {
		return
	}
	s.log.Info("registry created", "registry", reg.ID, "address", reg.Address, "username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, toRegistryResponse(reg))
}

func (s *server) updateRegistry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.registryID(w, r)
	if !ok {
		return
	}
	var req registryRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	reg, err := s.registries.Update(r.Context(), id, registries.Input(req))
	if s.registryError(w, "update registry", err) {
		return
	}
	s.log.Info("registry updated", "registry", reg.ID, "address", reg.Address, "password_changed", req.Password != nil,
		"username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, toRegistryResponse(reg))
}

func (s *server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	id, ok := s.registryID(w, r)
	if !ok {
		return
	}
	if s.registryError(w, "delete registry", s.registries.Delete(r.Context(), id)) {
		return
	}
	s.log.Info("registry deleted", "registry", id, "username", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) registryID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "registry not found")
		return 0, false
	}
	return id, true
}

// registryError answers err if there is one and reports whether it did.
func (s *server) registryError(w http.ResponseWriter, msg string, err error) bool {
	var inputErr *registries.InputError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, registries.ErrNotFound):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, registries.ErrTaken):
		s.writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, msg, err)
	}
	return true
}

// username names the request's user in logs.
func (s *server) username(r *http.Request) string {
	u, _ := userFrom(r.Context())
	return u.Username
}
