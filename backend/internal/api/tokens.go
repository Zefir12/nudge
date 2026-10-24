package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
)

type tokenResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// createdTokenResponse carries the secret, which is shown only here.
type createdTokenResponse struct {
	tokenResponse
	Token string `json:"token"`
}

func toTokenResponse(t auth.Token) tokenResponse {
	r := tokenResponse{ID: t.ID, Name: t.Name, Prefix: t.Prefix, CreatedAt: t.CreatedAt.UTC()}
	if t.LastUsedAt != nil {
		lu := t.LastUsedAt.UTC()
		r.LastUsedAt = &lu
	}
	return r
}

func (s *server) listTokens(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	tokens, err := s.auth.ListTokens(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, "list tokens", err)
		return
	}
	resp := make([]tokenResponse, 0, len(tokens))
	for _, t := range tokens {
		resp = append(resp, toTokenResponse(t))
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) createToken(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	t, secret, err := s.auth.CreateToken(r.Context(), u.ID, req.Name)
	var inputErr *auth.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
		return
	case errors.Is(err, auth.ErrTokenExists), errors.Is(err, auth.ErrTooManyTokens):
		s.writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		s.internalError(w, "create token", err)
		return
	}
	s.log.Info("token created", "username", u.Username, "token_id", t.ID, "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, createdTokenResponse{tokenResponse: toTokenResponse(t), Token: secret})
}

func (s *server) deleteToken(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "token not found")
		return
	}
	err = s.auth.DeleteToken(r.Context(), u.ID, id)
	switch {
	case errors.Is(err, auth.ErrTokenNotFound):
		s.writeError(w, http.StatusNotFound, "token not found")
		return
	case err != nil:
		s.internalError(w, "delete token", err)
		return
	}
	s.log.Info("token revoked", "username", u.Username, "token_id", id, "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}
