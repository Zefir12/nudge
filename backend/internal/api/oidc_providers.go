package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/oidcproviders"
)

// oidcProviderResponse never includes the client secret.
type oidcProviderResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Issuer            string `json:"issuer"`
	ClientID          string `json:"client_id"`
	AdminRole         string `json:"admin_role"`
	UserRole          string `json:"user_role"`
	RequireMFA        bool   `json:"require_mfa"`
	SessionTTLMinutes int64  `json:"session_ttl_minutes"`
	Enabled           bool   `json:"enabled"`
	ReadOnly          bool   `json:"read_only"`
	SecretUnreadable  bool   `json:"secret_unreadable"`
	// CallbackURL is the redirect URI to register at the provider.
	CallbackURL string    `json:"callback_url"`
	CreatedAt   time.Time `json:"created_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// oidcProvidersResponse has callback_prefix for the form's preview of a new provider's
// callback URL; it is empty without an https public URL, when none can be added.
type oidcProvidersResponse struct {
	CallbackPrefix string                 `json:"callback_prefix"`
	Providers      []oidcProviderResponse `json:"providers"`
}

// oidcProviderRequest creates or replaces a provider. client_secret is write-only: on
// update a missing or null one keeps the stored secret. id is read on create only.
type oidcProviderRequest struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Issuer            string  `json:"issuer"`
	ClientID          string  `json:"client_id"`
	ClientSecret      *string `json:"client_secret"` // #nosec G117 -- write-only input, never answered
	AdminRole         string  `json:"admin_role"`
	UserRole          string  `json:"user_role"`
	RequireMFA        bool    `json:"require_mfa"`
	SessionTTLMinutes int64   `json:"session_ttl_minutes"`
	Enabled           *bool   `json:"enabled"`
}

func (r oidcProviderRequest) input() oidcproviders.Input {
	in := oidcproviders.Input{
		ID: r.ID, Name: r.Name, Issuer: r.Issuer, ClientID: r.ClientID, ClientSecret: r.ClientSecret,
		AdminRole: r.AdminRole, UserRole: r.UserRole, RequireMFA: r.RequireMFA, Enabled: r.Enabled,
		// Out of range (and maybe too large to convert): refused by the service.
		SessionTTL: -1,
	}
	if r.SessionTTLMinutes >= 0 && r.SessionTTLMinutes <= int64(oidcproviders.MaxSessionTTL/time.Minute) {
		in.SessionTTL = time.Duration(r.SessionTTLMinutes) * time.Minute
	}
	return in
}

func toOIDCProviderResponse(p oidcproviders.Provider) oidcProviderResponse {
	return oidcProviderResponse{
		ID: p.ID, Name: p.Name, Issuer: p.Issuer, ClientID: p.ClientID, AdminRole: p.AdminRole, UserRole: p.UserRole,
		RequireMFA: p.RequireMFA, SessionTTLMinutes: int64(p.SessionTTL / time.Minute), Enabled: p.Enabled,
		ReadOnly: p.ReadOnly, SecretUnreadable: p.SecretUnreadable, CallbackURL: p.CallbackURL,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

func (s *server) listOIDCProviders(w http.ResponseWriter, r *http.Request) {
	list, err := s.sso.List(r.Context())
	if err != nil {
		s.internalError(w, "list sso providers", err)
		return
	}
	resp := oidcProvidersResponse{CallbackPrefix: s.sso.CallbackPrefix(), Providers: make([]oidcProviderResponse, len(list))}
	for i, p := range list {
		resp.Providers[i] = toOIDCProviderResponse(p)
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) getOIDCProvider(w http.ResponseWriter, r *http.Request) {
	p, err := s.sso.Get(r.Context(), r.PathValue("id"))
	if s.oidcProviderError(w, "get sso provider", err) {
		return
	}
	s.writeJSON(w, http.StatusOK, toOIDCProviderResponse(p))
}

func (s *server) createOIDCProvider(w http.ResponseWriter, r *http.Request) {
	var req oidcProviderRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	p, err := s.sso.Create(r.Context(), req.input())
	if s.oidcProviderError(w, "create sso provider", err) {
		return
	}
	auditTarget(r, p.ID)
	s.log.Info("sso provider created", "provider", p.ID, "issuer", p.Issuer, "username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, toOIDCProviderResponse(p))
}

func (s *server) updateOIDCProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	auditTarget(r, id)
	var req oidcProviderRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	c, err := s.sso.Update(r.Context(), id, req.input())
	if s.oidcProviderError(w, "update sso provider", err) {
		return
	}
	s.auditRevoked(r, c)
	s.log.Info("sso provider updated", "provider", id, "issuer", c.Provider.Issuer, "enabled", c.Provider.Enabled,
		"secret_changed", req.ClientSecret != nil, "sessions_revoked", c.Revoked.Sessions, "tokens_revoked", c.Revoked.Tokens,
		"username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, toOIDCProviderResponse(c.Provider))
}

func (s *server) deleteOIDCProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	auditTarget(r, id)
	c, err := s.sso.Delete(r.Context(), id)
	if s.oidcProviderError(w, "delete sso provider", err) {
		return
	}
	s.auditRevoked(r, c)
	s.log.Info("sso provider deleted", "provider", id, "issuer", c.Provider.Issuer, "sessions_revoked", c.Revoked.Sessions,
		"tokens_revoked", c.Revoked.Tokens, "username", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// auditRevoked names what taking a provider away cost its users in the audit entry.
func (s *server) auditRevoked(r *http.Request, c oidcproviders.Change) {
	if c.Revoked.Sessions > 0 || c.Revoked.Tokens > 0 {
		auditTarget(r, fmt.Sprintf("%s; revoked %d sessions, %d API tokens", c.Provider.ID, c.Revoked.Sessions, c.Revoked.Tokens))
	}
}

// oidcProviderError answers err if there is one and reports whether it did.
func (s *server) oidcProviderError(w http.ResponseWriter, msg string, err error) bool {
	var inputErr *oidcproviders.InputError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, oidcproviders.ErrNotFound):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, oidcproviders.ErrTaken), errors.Is(err, oidcproviders.ErrReadOnly),
		errors.Is(err, oidcproviders.ErrNoAdminLeft), errors.Is(err, oidcproviders.ErrLastProvider):
		s.writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, msg, err)
	}
	return true
}
