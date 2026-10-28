// Package oidc signs users in through an OpenID Connect provider: the authorization code
// flow with PKCE as a confidential client (client_secret_basic), with the ID token
// verified by go-oidc (RS256 only) and the nonce, azp, iat, roles and amr checked here.
// The logins in progress are kept server-side, bounded, keyed by their state.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	// PendingTTL is how long a started login may take at the provider.
	PendingTTL = 10 * time.Minute
	// maxPending bounds the logins in progress; the oldest is dropped beyond it.
	maxPending = 1000
	// maxPendingPerClient bounds one client's logins, so one client can't push out
	// everyone else's; its own oldest is dropped beyond it.
	maxPendingPerClient = 10
	// httpTimeout bounds every call to the provider.
	httpTimeout = 10 * time.Second
	// maxBody caps the provider's responses (discovery, JWKS, token).
	maxBody = 1 << 20
	// clockSkew is the leeway for an ID token issued "in the future".
	clockSkew = 60 * time.Second
	// rediscoverAfter spaces out discovery attempts while the provider is down.
	rediscoverAfter = 30 * time.Second
)

var (
	// ErrUnavailable means discovery failed: the provider is down or misconfigured.
	ErrUnavailable = errors.New("sso provider unavailable")
	// ErrState means the state is unknown, expired, used, or not this browser's.
	ErrState = errors.New("unknown or expired sso login")
)

// Reasons a RefusedError gives.
const (
	// ReasonNoRole: the roles claim has neither the admin nor the user role.
	ReasonNoRole = "no role"
	// ReasonNoMFA: a second factor is required and amr lacks mfa.
	ReasonNoMFA = "no second factor"
)

// RefusedError is a valid sign-in nudge refuses (no role, no second factor).
type RefusedError struct {
	Reason   string
	Subject  string
	Username string
}

func (e *RefusedError) Error() string { return "sso login refused: " + e.Reason }

// Options configure a Client.
type Options struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is <public URL>/api/v1/auth/oidc/callback.
	RedirectURL string
	// AdminRole is the roles claim value that makes a user an admin.
	AdminRole string
	// UserRole is the roles claim value that makes a user a user; empty means only
	// admins sign in. A user with both is an admin.
	UserRole string
	// RequireMFA refuses ID tokens whose amr lacks "mfa".
	RequireMFA bool
	// HTTPClient calls the provider; its transport is wrapped to cap bodies, and a zero
	// Timeout gets one. Nil means http.DefaultTransport.
	HTTPClient *http.Client
	// Now is the clock (time.Now by default).
	Now func() time.Time
	Log *slog.Logger
}

// Identity is a verified sign-in.
type Identity struct {
	Issuer      string
	Subject     string
	Username    string
	DisplayName string
	Email       string
	Roles       []string
	// Admin says the roles claim has AdminRole; otherwise it has UserRole.
	Admin bool
	// Redirect is the path the login was started with.
	Redirect string
}

// Client is the OIDC relying party.
type Client struct {
	opts Options
	http *http.Client
	now  func() time.Time
	log  *slog.Logger

	// discoverMu serializes discovery, which calls the provider; mu guards the fields
	// below and is never held across a call.
	discoverMu sync.Mutex
	mu         sync.Mutex
	provider   *provider
	lastTry    time.Time
	pending    map[string]pending
	// perClient counts each client's pending logins.
	perClient map[string]int
	// seq orders the pending logins, as several may start in one clock tick.
	seq uint64
}

// provider is what discovery found.
type provider struct {
	oauth      oauth2.Config
	verifier   *gooidc.IDTokenVerifier
	endSession string
}

type pending struct {
	binding  string
	nonce    string
	verifier string
	redirect string
	client   string
	created  time.Time
	seq      uint64
}

// New returns a Client. Discovery happens on the first login, so nudge starts while the
// provider is down.
func New(opts Options) *Client {
	c := &Client{opts: opts, now: opts.Now, log: opts.Log, pending: map[string]pending{}, perClient: map[string]int{}}
	if c.now == nil {
		c.now = time.Now
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	hc := http.Client{}
	if opts.HTTPClient != nil {
		hc = *opts.HTTPClient
	}
	if hc.Timeout == 0 {
		hc.Timeout = httpTimeout
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = capTransport{base: base}
	c.http = &hc
	return c
}

// Begin starts a login for client (its IP): it returns the provider's authorize URL and
// the binding value the browser must bring back (in a cookie) with the state.
func (c *Client) Begin(ctx context.Context, client, redirect string) (authURL, binding string, err error) {
	p, err := c.discover(ctx)
	if err != nil {
		return "", "", err
	}
	state, nonce, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	binding = rand.Text()
	c.mu.Lock()
	c.addPending(state, pending{binding: binding, nonce: nonce, verifier: verifier, redirect: redirect, client: client, created: c.now()})
	c.mu.Unlock()
	return p.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), gooidc.Nonce(nonce)), binding, nil
}

// addPending stores a login. Beyond a client's own bound its oldest login is dropped;
// beyond the total bound expired logins go first, then the oldest of all.
func (c *Client) addPending(state string, p pending) {
	if c.perClient[p.client] >= maxPendingPerClient {
		c.dropOldest(func(v pending) bool { return v.client == p.client })
	}
	if len(c.pending) >= maxPending {
		now := c.now()
		for k, v := range c.pending {
			if now.Sub(v.created) >= PendingTTL {
				c.drop(k)
			}
		}
		if len(c.pending) >= maxPending {
			c.dropOldest(func(pending) bool { return true })
		}
	}
	c.seq++
	p.seq = c.seq
	c.pending[state] = p
	c.perClient[p.client]++
}

// dropOldest drops the oldest pending login that match selects.
func (c *Client) dropOldest(match func(pending) bool) {
	var oldest string
	for k, v := range c.pending {
		if match(v) && (oldest == "" || v.seq < c.pending[oldest].seq) {
			oldest = k
		}
	}
	if oldest != "" {
		c.drop(oldest)
	}
}

func (c *Client) drop(state string) {
	p, ok := c.pending[state]
	if !ok {
		return
	}
	delete(c.pending, state)
	if c.perClient[p.client]--; c.perClient[p.client] <= 0 {
		delete(c.perClient, p.client)
	}
}

// take removes and returns state's login. It is used up even when binding is wrong.
func (c *Client) take(state, binding string) (pending, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[state]
	if !ok {
		return pending{}, false
	}
	c.drop(state)
	if c.now().Sub(p.created) >= PendingTTL || subtle.ConstantTimeCompare([]byte(p.binding), []byte(binding)) != 1 {
		return pending{}, false
	}
	return p, true
}

// Cancel forgets state's login (the provider answered with an error) and returns the
// path it was started with.
func (c *Client) Cancel(state, binding string) string {
	p, _ := c.take(state, binding)
	return p.redirect
}

// idClaims are the ID token claims nudge reads beyond go-oidc's.
type idClaims struct {
	Azp               string   `json:"azp"`
	IssuedAt          int64    `json:"iat"`
	PreferredUsername string   `json:"preferred_username"`
	Name              string   `json:"name"`
	Email             string   `json:"email"`
	Roles             []string `json:"roles"`
	Amr               []string `json:"amr"`
}

// Finish completes a login: it checks state and binding (one use), exchanges code with
// the PKCE verifier, verifies the ID token and applies the role and MFA rules.
func (c *Client) Finish(ctx context.Context, state, binding, code string) (Identity, error) {
	pend, ok := c.take(state, binding)
	if !ok {
		return Identity{}, ErrState
	}
	p, err := c.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c.http)
	tok, err := p.oauth.Exchange(ctx, code, oauth2.VerifierOption(pend.verifier))
	if err != nil {
		return Identity{}, fmt.Errorf("exchange code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, errors.New("token response has no id_token")
	}
	idt, err := p.verifier.Verify(gooidc.ClientContext(ctx, c.http), raw)
	if err != nil {
		return Identity{}, fmt.Errorf("verify id token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(pend.nonce)) != 1 {
		return Identity{}, errors.New("verify id token: nonce mismatch")
	}
	var cl idClaims
	if err := idt.Claims(&cl); err != nil {
		return Identity{}, fmt.Errorf("id token claims: %w", err)
	}
	if (len(idt.Audience) > 1 || cl.Azp != "") && cl.Azp != c.opts.ClientID {
		return Identity{}, fmt.Errorf("verify id token: azp %q is not this client", cl.Azp)
	}
	if cl.IssuedAt == 0 || time.Unix(cl.IssuedAt, 0).After(c.now().Add(clockSkew)) {
		return Identity{}, errors.New("verify id token: issued in the future")
	}
	id := Identity{
		Issuer: idt.Issuer, Subject: idt.Subject, Username: cl.PreferredUsername,
		DisplayName: cl.Name, Email: cl.Email, Roles: cl.Roles, Redirect: pend.redirect,
	}
	id.Admin = slices.Contains(cl.Roles, c.opts.AdminRole)
	if !id.Admin && (c.opts.UserRole == "" || !slices.Contains(cl.Roles, c.opts.UserRole)) {
		return id, &RefusedError{Reason: ReasonNoRole, Subject: id.Subject, Username: id.Username}
	}
	if c.opts.RequireMFA && !slices.Contains(cl.Amr, "mfa") {
		return id, &RefusedError{Reason: ReasonNoMFA, Subject: id.Subject, Username: id.Username}
	}
	return id, nil
}

// LogoutURL is the provider's end_session_endpoint with the client ID, or "" when it has
// none or discovery hasn't succeeded (logout never waits for the provider).
func (c *Client) LogoutURL() string {
	c.mu.Lock()
	p := c.provider
	c.mu.Unlock()
	if p == nil || p.endSession == "" {
		return ""
	}
	u, err := url.Parse(p.endSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", c.opts.ClientID)
	u.RawQuery = q.Encode()
	return u.String()
}

// discover returns the provider, running discovery once it succeeds and at most every
// rediscoverAfter while it fails. go-oidc refuses a discovered issuer other than the
// configured one.
func (c *Client) discover(ctx context.Context) (*provider, error) {
	c.mu.Lock()
	p := c.provider
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}
	// Concurrent logins wait for one attempt (bounded by the HTTP client's timeout)
	// instead of each calling the provider; the pending logins stay usable meanwhile.
	c.discoverMu.Lock()
	defer c.discoverMu.Unlock()
	c.mu.Lock()
	p, last := c.provider, c.lastTry
	now := c.now()
	if p == nil && (last.IsZero() || now.Sub(last) >= rediscoverAfter) {
		c.lastTry = now
	}
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}
	if !last.IsZero() && now.Sub(last) < rediscoverAfter {
		return nil, ErrUnavailable
	}
	// Not the request's context: a browser that gives up must not fail discovery for
	// everyone until the next attempt.
	ctx = context.WithoutCancel(ctx)
	gp, err := gooidc.NewProvider(gooidc.ClientContext(ctx, c.http), c.opts.Issuer)
	if err != nil {
		c.log.Warn("oidc discovery", "issuer", c.opts.Issuer, "err", err)
		return nil, ErrUnavailable
	}
	var extra struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := gp.Claims(&extra); err != nil {
		c.log.Warn("oidc discovery", "issuer", c.opts.Issuer, "err", err)
		return nil, ErrUnavailable
	}
	ep := gp.Endpoint()
	ep.AuthStyle = oauth2.AuthStyleInHeader
	p = &provider{
		oauth: oauth2.Config{
			ClientID: c.opts.ClientID, ClientSecret: c.opts.ClientSecret, Endpoint: ep,
			RedirectURL: c.opts.RedirectURL, Scopes: []string{gooidc.ScopeOpenID, "profile", "email"},
		},
		verifier: gp.Verifier(&gooidc.Config{
			ClientID: c.opts.ClientID, SupportedSigningAlgs: []string{gooidc.RS256}, Now: c.now,
		}),
		endSession: extra.EndSession,
	}
	c.mu.Lock()
	c.provider = p
	c.mu.Unlock()
	return p, nil
}

// capTransport fails reading a response body beyond maxBody.
type capTransport struct{ base http.RoundTripper }

func (t capTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	resp.Body = &cappedBody{rc: resp.Body, left: maxBody}
	return resp, nil
}

type cappedBody struct {
	rc   io.ReadCloser
	left int64
}

var errTooLarge = errors.New("oidc: provider response too large")

func (b *cappedBody) Read(p []byte) (int, error) {
	// Reading one byte past maxBody tells a body of exactly maxBody from a longer one.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, errTooLarge
	}
	return n, err
}

func (b *cappedBody) Close() error { return b.rc.Close() }
