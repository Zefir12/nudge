package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

// accountResponse is a user in the user management.
type accountResponse struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	Role        auth.Role `json:"role"`
	Disabled    bool      `json:"disabled"`
	SSO         bool      `json:"sso"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
}

func toAccountResponse(a auth.Account) accountResponse {
	return accountResponse{
		ID: a.ID, Username: a.Username, Role: a.Role, Disabled: a.Disabled, SSO: a.SSO,
		DisplayName: a.DisplayName, Email: a.Email, CreatedAt: a.CreatedAt.UTC(),
	}
}

func (s *server) userError(w http.ResponseWriter, msg string, err error) {
	var inputErr *auth.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, auth.ErrUserNotFound):
		s.writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, auth.ErrUserExists), errors.Is(err, auth.ErrLastAdmin), errors.Is(err, auth.ErrBreakGlass):
		s.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrBusy):
		s.writeError(w, http.StatusServiceUnavailable, "too many password checks, try again")
	default:
		s.internalError(w, msg, err)
	}
}

func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		s.internalError(w, "list users", err)
		return
	}
	resp := make([]accountResponse, 0, len(users))
	for _, a := range users {
		resp = append(resp, toAccountResponse(a))
	}
	s.writeJSON(w, http.StatusOK, resp)
}

type createUserRequest struct {
	Username string    `json:"username"`
	Password string    `json:"password"`
	Role     auth.Role `json:"role"`
}

// createUser creates a local user with a password.
func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Username)
	a, err := s.auth.CreateUser(r.Context(), req.Username, req.Password, req.Role)
	if err != nil {
		s.userError(w, "create user", err)
		return
	}
	s.log.Info("user created", "username", a.Username, "role", a.Role, "by", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, toAccountResponse(a))
}

type changeUserRequest struct {
	Role     *auth.Role `json:"role"`
	Disabled *bool      `json:"disabled"`
	Password *string    `json:"password"`
}

// changeUser changes a user's role, disables or enables them, or resets a local user's
// password.
func (s *server) changeUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	var req changeUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	a, err := s.auth.UpdateUser(r.Context(), a.ID, auth.Change{Role: req.Role, Disabled: req.Disabled, Password: req.Password})
	if err != nil {
		s.userError(w, "change user", err)
		return
	}
	s.log.Info("user changed", "username", a.Username, "role", a.Role, "disabled", a.Disabled,
		"password_reset", req.Password != nil, "by", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, toAccountResponse(a))
}

// deleteUser deletes a user with their sessions and tokens.
func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	if err := s.auth.DeleteUser(r.Context(), a.ID); err != nil {
		s.userError(w, "delete user", err)
		return
	}
	s.log.Info("user deleted", "username", a.Username, "by", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// pathUser returns the {id} user, naming them in the audit entry, or answers 404.
func (s *server) pathUser(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, http.StatusNotFound, "user not found")
		return auth.Account{}, false
	}
	a, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		s.userError(w, "user", err)
		return auth.Account{}, false
	}
	auditTarget(r, a.Username)
	return a, true
}

// userEnvironments are the environments a user may use, by id. Admins use every one.
type userEnvironments struct {
	Environments []int64 `json:"environments"`
}

func (s *server) getUserEnvironments(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	ids, err := s.envs.Access(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, "environment access", err)
		return
	}
	s.writeJSON(w, http.StatusOK, userEnvironments{Environments: ids})
}

// maxUserEnvironments bounds a request's environment list.
const maxUserEnvironments = 1000

// setUserEnvironments replaces the environments a user may use.
func (s *server) setUserEnvironments(w http.ResponseWriter, r *http.Request) {
	a, ok := s.pathUser(w, r)
	if !ok {
		return
	}
	var req userEnvironments
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Environments == nil || len(req.Environments) > maxUserEnvironments {
		s.writeError(w, http.StatusBadRequest, "environments must be a list of environment ids")
		return
	}
	before, err := s.envs.Access(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, "environment access", err)
		return
	}
	err = s.envs.SetAccess(r.Context(), a.ID, req.Environments)
	if errors.Is(err, environments.ErrNotFound) {
		s.writeError(w, http.StatusBadRequest, "no such environment")
		return
	}
	if err != nil {
		s.internalError(w, "set environment access", err)
		return
	}
	ids, err := s.envs.Access(r.Context(), a.ID)
	if err != nil {
		s.internalError(w, "environment access", err)
		return
	}
	s.log.Info("user environments changed", "username", a.Username, "environments", ids, "by", s.username(r), "ip", clientIP(r))
	s.ensureDefaultNetworks(r.Context(), a, addedIDs(before, ids))
	s.writeJSON(w, http.StatusOK, userEnvironments{Environments: ids})
}
