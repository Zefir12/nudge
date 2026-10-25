package registryclient

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// maxTokens bounds the cached Authorization headers.
	maxTokens = 256
	// defaultTokenTTL is what a token without expires_in lasts (the distribution spec's
	// default), maxTokenTTL the longest nudge keeps one, basicTTL how long a registry
	// that asked for Basic auth gets it without asking again.
	defaultTokenTTL = 60 * time.Second
	maxTokenTTL     = time.Hour
	basicTTL        = 10 * time.Minute
	// tokenMargin is how long before its expiry a token is no longer used.
	tokenMargin = 5 * time.Second
	// maxTokenBody caps a token server's answer.
	maxTokenBody = 64 << 10
)

// challenge is a parsed WWW-Authenticate header.
type challenge struct {
	scheme string // "bearer" or "basic"
	params map[string]string
}

// parseChallenges returns the Bearer or Basic challenge of resp, preferring Bearer.
func parseChallenges(h http.Header) (challenge, bool) {
	var basic *challenge
	for _, v := range h.Values("WWW-Authenticate") {
		c := parseChallenge(v)
		switch c.scheme {
		case "bearer":
			return c, true
		case "basic":
			basic = &c
		}
	}
	if basic != nil {
		return *basic, true
	}
	return challenge{}, false
}

// parseChallenge parses `Scheme key="value", key=value`.
func parseChallenge(v string) challenge {
	scheme, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
	c := challenge{scheme: strings.ToLower(scheme), params: map[string]string{}}
	for {
		rest = strings.TrimLeft(rest, " ,\t")
		key, after, ok := strings.Cut(rest, "=")
		if !ok || key == "" {
			return c
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var val strings.Builder
		if strings.HasPrefix(after, `"`) {
			i := 1
			for ; i < len(after) && after[i] != '"'; i++ {
				if after[i] == '\\' && i+1 < len(after) {
					i++
				}
				val.WriteByte(after[i])
			}
			rest = after[min(i+1, len(after)):]
		} else {
			end := strings.IndexByte(after, ',')
			if end < 0 {
				end = len(after)
			}
			val.WriteString(strings.TrimSpace(after[:end]))
			rest = after[end:]
		}
		c.params[key] = val.String()
	}
}

// tokenKey names a cached Authorization header: the registry, the credentials (the
// password only as a hash) and the scope nudge asked for.
type tokenKey struct {
	host     string
	username string
	password [sha256.Size]byte
	scope    string
}

type cachedToken struct {
	header  string
	expires time.Time
}

func (c *Client) keyFor(reg Registry, scope string) tokenKey {
	return tokenKey{host: reg.Address, username: reg.Username, password: sha256.Sum256([]byte(reg.Password)), scope: scope}
}

// cached returns the Authorization header stored for key, if it hasn't expired.
func (c *Client) cached(key tokenKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.tokens[key]
	if !ok {
		return "", false
	}
	if !c.now().Before(t.expires) {
		delete(c.tokens, key)
		return "", false
	}
	return t.header, true
}

// store caches header for key until expires; when the cache is full it drops expired
// entries, then the one that expires first.
func (c *Client) store(key tokenKey, header string, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.tokens[key]; !ok && len(c.tokens) >= maxTokens {
		now := c.now()
		var oldest tokenKey
		var oldestAt time.Time
		for k, t := range c.tokens {
			if !now.Before(t.expires) {
				delete(c.tokens, k)
				continue
			}
			if oldestAt.IsZero() || t.expires.Before(oldestAt) {
				oldest, oldestAt = k, t.expires
			}
		}
		if len(c.tokens) >= maxTokens {
			delete(c.tokens, oldest)
		}
	}
	c.tokens[key] = cachedToken{header: header, expires: expires}
}

// authorize answers a 401's challenge: a Bearer token from the realm, or Basic auth. It
// returns the Authorization header to retry with and caches it under key.
func (c *Client) authorize(ctx context.Context, reg Registry, base *url.URL, ch challenge, key tokenKey) (string, error) {
	switch ch.scheme {
	case "basic":
		header := "Basic " + basicAuth(reg.Username, reg.Password)
		c.store(key, header, c.now().Add(basicTTL))
		return header, nil
	case "bearer":
	default:
		return "", fmt.Errorf("%w: unsupported auth challenge", ErrUnreachable)
	}
	realm, err := url.Parse(ch.params["realm"])
	if err != nil || realm.Host == "" {
		return "", fmt.Errorf("%w: invalid token realm", ErrUnreachable)
	}
	if !realmAllowed(realm, base) {
		return "", fmt.Errorf("%w: realm %s://%s", ErrInsecureRealm, realm.Scheme, realm.Host)
	}
	q := realm.Query()
	if service := ch.params["service"]; service != "" {
		q.Set("service", service)
	}
	scopes := strings.Fields(ch.params["scope"])
	if len(scopes) == 0 && key.scope != "" {
		scopes = []string{key.scope}
	}
	for _, s := range scopes {
		q.Add("scope", s)
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: token request: %w", ErrUnreachable, err)
	}
	if reg.Username != "" {
		req.SetBasicAuth(reg.Username, reg.Password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: token: %w", ErrUnreachable, err)
	}
	defer drain(resp)
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "", fmt.Errorf("%w: token server answered %d", ErrUnauthorized, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: token server answered %d", ErrUnreachable, resp.StatusCode)
	}
	body, err := readAll(resp, maxTokenBody)
	if err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return "", fmt.Errorf("%w: token server sent invalid JSON", ErrUnreachable)
	}
	token := t.Token
	if token == "" {
		token = t.AccessToken
	}
	if token == "" || strings.ContainsAny(token, " \r\n") {
		return "", fmt.Errorf("%w: token server sent no token", ErrUnreachable)
	}
	ttl := defaultTokenTTL
	if t.ExpiresIn > 0 {
		ttl = min(time.Duration(t.ExpiresIn)*time.Second, maxTokenTTL)
	}
	header := "Bearer " + token
	c.store(key, header, c.now().Add(ttl-tokenMargin))
	return header, nil
}

// realmAllowed says whether the registry at base may have nudge send its credentials
// to realm: only over HTTPS, or to the registry's own host when the registry itself is
// plain HTTP.
func realmAllowed(realm, base *url.URL) bool {
	if realm.User != nil {
		return false
	}
	switch realm.Scheme {
	case "https":
		return true
	case "http":
		return base.Scheme == "http" && strings.EqualFold(realm.Host, base.Host)
	}
	return false
}
