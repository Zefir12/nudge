// Package oidctest is a fake OpenID provider on httptest for tests: discovery, JWKS, and
// a token endpoint that checks client_secret_basic and PKCE, then signs an ID token with
// a test RSA key.
package oidctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The client the provider knows.
const (
	ClientID     = "nudge"
	ClientSecret = "s3cret"
	RedirectURL  = "https://nudge.example.net/api/v1/auth/oidc/callback"
)

// testKey is shared: generating RSA keys is slow under -race.
var testKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// Provider is the fake provider. Its fields are set before a login starts.
type Provider struct {
	// URL is the issuer; Client trusts the server's certificate.
	URL    string
	Client *http.Client
	now    func() time.Time

	mu sync.Mutex
	// issuer is what discovery reports.
	issuer    string
	key       *rsa.PrivateKey
	kid       string
	published map[string]*rsa.PrivateKey
	jwksCalls int
	// codes are the granted authorizations: code → challenge and nonce.
	codes map[string]grant
	edit  func(header, claims map[string]any)
	sign  func(input string) string
}

type grant struct{ challenge, nonce string }

// New starts a provider whose tokens are issued at now().
func New(t *testing.T, now func() time.Time) *Provider {
	t.Helper()
	p := &Provider{now: now, key: testKey(), kid: "k1", codes: map[string]grant{}}
	p.published = map[string]*rsa.PrivateKey{"k1": p.key}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", p.jwks)
	mux.HandleFunc("POST /token", p.token)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	p.URL, p.Client, p.issuer = srv.URL, srv.Client(), srv.URL
	return p
}

// Edit changes the ID token's header and claims before it is signed.
func (p *Provider) Edit(f func(header, claims map[string]any)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.edit = f
}

// Sign replaces RS256 signing (alg none, HS256): it returns the signature of input.
func (p *Provider) Sign(f func(input string) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sign = f
}

// SetIssuer changes the issuer discovery reports.
func (p *Provider) SetIssuer(iss string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.issuer = iss
}

// Rotate signs with the same key under a new kid; the JWKS lists only that one.
func (p *Provider) Rotate(kid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kid = kid
	p.published = map[string]*rsa.PrivateKey{kid: p.key}
}

// JWKSCalls is how often the JWKS was fetched.
func (p *Provider) JWKSCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.jwksCalls
}

// Authorize plays the browser and the provider's consent: it checks the authorize URL a
// login started with and grants a code for it.
func (p *Provider) Authorize(t *testing.T, authURL string) (state, code string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(authURL, p.URL+"/authorize?") || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != ClientID ||
		q.Get("redirect_uri") != RedirectURL || !strings.Contains(q.Get("scope"), "openid") || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorize URL = %s", authURL)
	}
	code = rand.Text()
	p.mu.Lock()
	p.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce")}
	p.mu.Unlock()
	return q.Get("state"), code
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	writeJSON(w, map[string]any{
		"issuer":                                p.issuer,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/jwks",
		"end_session_endpoint":                  p.URL + "/logout",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *Provider) jwks(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.jwksCalls++
	keys := []map[string]any{}
	for kid, k := range p.published {
		keys = append(keys, map[string]any{
			"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
			"n": B64(k.N.Bytes()), "e": B64(big.NewInt(int64(k.E)).Bytes()),
		})
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if !ok || id != ClientID || secret != ClientSecret {
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != RedirectURL {
		http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	g, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || B64(sum[:]) != g.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"access_token": "at", "token_type": "Bearer", "expires_in": 300, "id_token": p.idToken(g.nonce)})
}

func (p *Provider) idToken(nonce string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	header := map[string]any{"alg": "RS256", "kid": p.kid, "typ": "JWT"}
	claims := map[string]any{
		"iss": p.issuer, "sub": "user-1", "aud": ClientID, "nonce": nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"preferred_username": "alice", "name": "Alice", "email": "alice@example.net",
		"roles": []string{"admin"}, "amr": []string{"pwd"},
	}
	if p.edit != nil {
		p.edit(header, claims)
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := B64(h) + "." + B64(c)
	if p.sign != nil {
		return input + "." + p.sign(input)
	}
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return input + "." + B64(sig)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// B64 is base64url without padding, as JWTs use.
func B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
