package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"git.zefirlabs.net/zefir/nudge/backend/internal/audit"
	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc"
)

const (
	// oidcCookie binds a started SSO login to the browser that started it.
	oidcCookie     = "nudge_oidc"
	oidcCookiePath = "/api/v1/auth/oidc"
	// ssoFailedPath is where a failed SSO login lands; the login page shows a generic error.
	ssoFailedPath = "/login?error=sso"
)

type authMethodsResponse struct {
	Password bool            `json:"password"`
	OIDC     *oidcMethodBody `json:"oidc"`
}

type oidcMethodBody struct {
	Name string `json:"name"`
}

// authMethods tells the login page which ways to sign in it offers.
func (s *server) authMethods(w http.ResponseWriter, _ *http.Request) {
	resp := authMethodsResponse{Password: !s.noPasswordLogin}
	if s.oidc != nil {
		resp.OIDC = &oidcMethodBody{Name: s.oidcName}
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// oidcLogin starts an SSO login: it binds the login to this browser with a cookie and
// sends it to the provider.
func (s *server) oidcLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		s.notFound(w, r)
		return
	}
	authURL, binding, err := s.oidc.Begin(r.Context(), clientIP(r), safeRedirect(r.URL.Query().Get("redirect")))
	if err != nil {
		s.log.Warn("sso login start", "err", err, "ip", clientIP(r))
		http.Redirect(w, r, ssoFailedPath, http.StatusFound)
		return
	}
	// Lax, unlike the session cookie: it must come back on the provider's cross-site
	// redirect to the callback. SSO needs an https public URL, so it is always Secure.
	http.SetCookie(w, &http.Cookie{
		Name: oidcCookie, Value: binding, Path: oidcCookiePath, MaxAge: int(oidc.PendingTTL / time.Second),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	// #nosec G710 -- the provider's discovered authorize endpoint, not the request's input
	http.Redirect(w, r, authURL, http.StatusFound)
}

// oidcCallback finishes an SSO login and starts a nudge session. Every attempt counts
// against the client's login limit until it succeeds; failures go to the login page with
// a generic error and are logged with the detail.
func (s *server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		s.notFound(w, r)
		return
	}
	ip := clientIP(r)
	q := r.URL.Query()
	var binding string
	if c, err := r.Cookie(oidcCookie); err == nil {
		binding = c.Value
	}
	// One use: the cookie goes whatever happens.
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Path: oidcCookiePath, MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})

	succeeded, err := s.auth.Attempt(ip)
	if err != nil {
		s.log.Warn("sso login rate limited", "ip", ip)
		http.Redirect(w, r, ssoFailedPath, http.StatusFound)
		return
	}
	if e := q.Get("error"); e != "" {
		s.oidc.Cancel(q.Get("state"), binding)
		s.log.Warn("sso login failed at the provider", "error", clipLog(e), "ip", ip)
		http.Redirect(w, r, ssoFailedPath, http.StatusFound)
		return
	}
	id, err := s.oidc.Finish(r.Context(), q.Get("state"), binding, q.Get("code"))
	var refused *oidc.RefusedError
	switch {
	case errors.As(err, &refused):
		sub, name := clipLog(refused.Subject), clipLog(refused.Username)
		s.log.Warn("sso login refused", "reason", refused.Reason, "subject", sub, "username", name, "ip", ip)
		target := sub + ": " + refused.Reason
		if refused.Reason == oidc.ReasonNoRole {
			name, target = s.revokeSSO(r, id, name, target)
		}
		s.auditSSO(r, name, target, http.StatusForbidden)
		s.ssoRefusedPage(w, refused.Reason)
		return
	case errors.Is(err, context.Canceled):
		return
	case err != nil:
		s.log.Warn("sso login failed", "err", err, "ip", ip)
		http.Redirect(w, r, ssoFailedPath, http.StatusFound)
		return
	}
	role := auth.RoleUser
	if id.Admin {
		role = auth.RoleAdmin
	}
	sess, err := s.auth.SSOLogin(r.Context(), auth.Identity{
		Issuer: id.Issuer, Subject: id.Subject, Username: id.Username, DisplayName: id.DisplayName, Email: id.Email, Role: role,
	})
	if errors.Is(err, auth.ErrDisabled) {
		sub, name := clipLog(id.Subject), clipLog(id.Username)
		s.log.Warn("sso login refused", "reason", "disabled", "subject", sub, "username", name, "ip", ip)
		s.auditSSO(r, name, sub+": disabled", http.StatusForbidden)
		s.ssoRefusedPage(w, "disabled")
		return
	}
	if err != nil {
		s.log.Error("sso login", "err", err, "subject", clipLog(id.Subject))
		http.Redirect(w, r, ssoFailedPath, http.StatusFound)
		return
	}
	succeeded()
	s.log.Info("logged in with sso", "username", sess.User.Username, "subject", clipLog(id.Subject), "ip", ip)
	s.auditSSO(r, sess.User.Username, clipLog(id.Subject), http.StatusFound)
	// The session cookie stays Strict: the redirect below comes from the provider's
	// cross-site navigation, so the cookie isn't sent with it, but index.html needs none
	// and the app's own requests are same-site.
	s.setSessionCookie(w, r, sess.Token, sess.ExpiresAt, 0)
	http.Redirect(w, r, id.Redirect, http.StatusFound)
}

// auditSSO records an SSO login; the callback is a GET, which serveAudited leaves out.
func (s *server) auditSSO(r *http.Request, username, target string, status int) {
	if s.audit == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	e := audit.Entry{Time: s.now(), Username: username, Action: r.Pattern, Target: target, Status: status}
	if err := s.audit.Record(ctx, e); err != nil {
		s.log.Error("audit", "err", err, "action", e.Action)
	}
}

// revokeSSO ends the sessions and deletes the API tokens of a user the provider no longer
// gives a role, so losing it at the provider ends their access at their next sign-in
// attempt instead of at session expiry. It returns the audit entry's username (the nudge
// user's, when there is one) and target (with what was revoked).
func (s *server) revokeSSO(r *http.Request, id oidc.Identity, name, target string) (string, string) {
	// Not the request's context: a client that goes away mustn't stop the revocation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditTimeout)
	defer cancel()
	rev, err := s.auth.RevokeSSO(ctx, id.Issuer, id.Subject)
	if err != nil {
		s.log.Error("revoke sso user", "err", err, "subject", clipLog(id.Subject))
		return name, target + "; revoking sessions and tokens failed"
	}
	if rev.Username == "" {
		return name, target
	}
	s.log.Warn("sso user without a role: sessions and tokens revoked", "username", rev.Username,
		"sessions", rev.Sessions, "tokens", rev.Tokens)
	return rev.Username, fmt.Sprintf("%s; revoked %d sessions, %d API tokens", target, rev.Sessions, rev.Tokens)
}

// ssoRefusedPage answers a sign-in the provider vouched for but nudge refuses.
func (s *server) ssoRefusedPage(w http.ResponseWriter, reason string) {
	msg := "Your account has no access to nudge. Ask an administrator for a nudge role."
	switch reason {
	case oidc.ReasonNoMFA:
		msg = "nudge needs a second factor. Sign in again with your authenticator code."
	case "disabled":
		msg = "Your nudge account is disabled. Ask an administrator."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Access denied · nudge</title></head>` +
		`<body><h1>Access denied</h1><p>` + html.EscapeString(msg) + `</p><p><a href="/login">Back to login</a></p></body></html>`))
}

// maxRedirectLen caps the path a pending SSO login keeps.
const maxRedirectLen = 512

// safeRedirect keeps a redirect only when it is a path on this site (as the web UI's
// safeRedirect does), so a crafted login link can't send the user elsewhere.
func safeRedirect(target string) string {
	if len(target) > maxRedirectLen || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") || strings.Contains(target, `\`) ||
		strings.ContainsFunc(target, unicode.IsControl) {
		return "/"
	}
	if u, err := url.Parse(target); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return target
}

// clipLog shortens a provider-supplied value before it is logged or audited.
func clipLog(s string) string {
	if len(s) > 100 {
		s = s[:100]
	}
	return strings.ToValidUTF8(s, "?")
}
