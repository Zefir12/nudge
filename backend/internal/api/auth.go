package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
)

// sessionCookie holds the browser's session token.
const sessionCookie = "nudge_session"

type userKey struct{}

func withUser(ctx context.Context, u auth.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// userFrom returns the user the auth middleware put in ctx.
func userFrom(ctx context.Context) (auth.User, bool) {
	u, ok := ctx.Value(userKey{}).(auth.User)
	return u, ok
}

type userResponse struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	// Role is "admin" or "user".
	Role auth.Role `json:"role"`
	// Admin says whether the user may do admin-only actions, so the UI can hide them.
	Admin bool `json:"admin"`
	// SSO says the user signs in through the SSO provider and has no password in nudge.
	SSO bool `json:"sso"`
}

func (s *server) toUserResponse(u auth.User) userResponse {
	return userResponse{ID: u.ID, Username: u.Username, CreatedAt: u.CreatedAt.UTC(), Role: u.Role, Admin: isAdmin(u), SSO: u.SSO}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupStatusResponse struct {
	Needed bool `json:"needed"`
}

func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	if s.oidc != nil {
		// The first admin is whoever the SSO provider gives the admin role.
		s.writeJSON(w, http.StatusOK, setupStatusResponse{})
		return
	}
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		s.internalError(w, "setup status", err)
		return
	}
	s.writeJSON(w, http.StatusOK, setupStatusResponse{Needed: needed})
}

// setup creates the admin and logs them in.
func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	if s.oidc != nil {
		s.writeError(w, http.StatusForbidden, "setup is off: users sign in with single sign-on")
		return
	}
	var c credentials
	if !s.decodeJSON(w, r, &c) {
		return
	}
	auditTarget(r, c.Username)
	_, err := s.auth.CreateFirstUser(r.Context(), c.Username, c.Password)
	var inputErr *auth.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
		return
	case errors.Is(err, auth.ErrSetupDone):
		s.writeError(w, http.StatusConflict, "setup is already done")
		return
	case err != nil:
		s.internalError(w, "setup", err)
		return
	}
	s.log.Info("admin created", "username", c.Username, "ip", clientIP(r))
	s.startSession(w, r, c, http.StatusCreated)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !s.decodeJSON(w, r, &c) {
		return
	}
	s.startSession(w, r, c, http.StatusOK)
}

func (s *server) startSession(w http.ResponseWriter, r *http.Request, c credentials, code int) {
	ip := clientIP(r)
	sess, err := s.auth.Login(r.Context(), ip, c.Username, c.Password)
	var limited *auth.RateLimitedError
	switch {
	case errors.As(err, &limited):
		s.log.Warn("login rate limited", "ip", ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds()))))
		s.writeError(w, http.StatusTooManyRequests, "too many failed login attempts, try again later")
		return
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "1")
		s.writeError(w, http.StatusServiceUnavailable, "too many concurrent logins, try again")
		return
	case errors.Is(err, context.Canceled):
		// The client went away; nothing to answer or log.
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.log.Warn("login failed", "ip", ip)
		s.writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	case err != nil:
		s.internalError(w, "login", err)
		return
	}
	s.log.Info("logged in", "username", sess.User.Username, "ip", ip)
	s.setSessionCookie(w, r, sess.Token, sess.ExpiresAt, 0)
	s.writeJSON(w, code, s.toUserResponse(sess.User))
}

// changePassword changes the logged-in user's own password and ends their other
// sessions; this one stays.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	ip := clientIP(r)
	err := s.auth.ChangePassword(r.Context(), ip, u.ID, sessionToken(r), req.Current, req.New)
	var limited *auth.RateLimitedError
	var inputErr *auth.InputError
	switch {
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
		return
	case errors.As(err, &limited):
		s.log.Warn("password change rate limited", "username", u.Username, "ip", ip)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(limited.RetryAfter.Seconds()))))
		s.writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	case errors.Is(err, auth.ErrBusy):
		w.Header().Set("Retry-After", "1")
		s.writeError(w, http.StatusServiceUnavailable, "too many password checks at once, try again")
		return
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, auth.ErrUserNotFound):
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	case errors.Is(err, auth.ErrPasswordChanged):
		s.writeError(w, http.StatusConflict, "your password was changed meanwhile; sign in again")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.log.Warn("password change: wrong current password", "username", u.Username, "ip", ip)
		s.writeError(w, http.StatusForbidden, "the current password is wrong")
		return
	case err != nil:
		s.internalError(w, "change password", err)
		return
	}
	s.log.Info("password changed", "username", u.Username, "ip", ip)
	w.WriteHeader(http.StatusNoContent)
}

type logoutResponse struct {
	// LogoutURL is the SSO provider's sign-out page, for the browser to visit next.
	LogoutURL string `json:"logout_url"`
}

// logout ends the session. An SSO user's answer names the provider's sign-out page.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	sso, err := s.auth.Logout(r.Context(), sessionToken(r))
	if err != nil {
		s.internalError(w, "logout", err)
		return
	}
	s.setSessionCookie(w, r, "", time.Time{}, -1)
	if sso && s.oidc != nil {
		if u := s.oidc.LogoutURL(); u != "" {
			s.writeJSON(w, http.StatusOK, logoutResponse{LogoutURL: u})
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	s.writeJSON(w, http.StatusOK, s.toUserResponse(u))
}

// setSessionCookie sets the session cookie; maxAge -1 deletes it.
func (s *server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time, maxAge int) {
	// Secure over HTTPS or when configured: nudge also serves plain HTTP (the default on
	// a LAN), where a Secure cookie would never come back.
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- see above
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.secureCookies || isTLS(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// bearerToken returns the token of an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
