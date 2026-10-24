package api

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
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
}

func toUserResponse(u auth.User) userResponse {
	return userResponse{ID: u.ID, Username: u.Username, CreatedAt: u.CreatedAt.UTC()}
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type setupStatusResponse struct {
	Needed bool `json:"needed"`
}

func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		s.internalError(w, "setup status", err)
		return
	}
	s.writeJSON(w, http.StatusOK, setupStatusResponse{Needed: needed})
}

// setup creates the admin and logs them in.
func (s *server) setup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !s.decodeJSON(w, r, &c) {
		return
	}
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
	setSessionCookie(w, r, sess.Token, sess.ExpiresAt, 0)
	s.writeJSON(w, code, toUserResponse(sess.User))
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		s.internalError(w, "logout", err)
		return
	}
	setSessionCookie(w, r, "", time.Time{}, -1)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) me(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r.Context())
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	s.writeJSON(w, http.StatusOK, toUserResponse(u))
}

// setSessionCookie sets the session cookie; maxAge -1 deletes it.
func setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time, maxAge int) {
	// Secure only over HTTPS: nudge also serves plain HTTP (the default on a LAN), where
	// a Secure cookie would never come back.
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- see above
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   isTLS(r),
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

// isTLS reports whether the browser reached us over HTTPS, directly or through a
// TLS-terminating proxy. A forged X-Forwarded-Proto only sets Secure on a cookie the
// sender gets back, so it is safe to trust here.
func isTLS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// clientIP is the peer address, as its /64 for IPv6 (one client usually holds a whole
// /64). Forwarded headers are ignored: they are forgeable, so behind a reverse proxy
// every client shares the proxy's rate limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}
