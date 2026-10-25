package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/docker"
	"git.zefirlabs.net/zefir/nudge/backend/internal/docker/dockertest"
	"git.zefirlabs.net/zefir/nudge/backend/internal/environments"
)

var testProxies = proxies{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("2001:db8:ffff::/48"),
	netip.MustParsePrefix("127.0.0.1/32"),
}

func TestProxiesClient(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		noProxies bool
		remote    string
		xff       []string // one X-Forwarded-For header line each
		proto     string
		tls       bool
		wantIP    string
		wantTLS   bool
	}{
		{name: "no proxies configured: headers ignored", noProxies: true, remote: "10.0.0.5:1234", xff: []string{"203.0.113.9"}, proto: "https", wantIP: "10.0.0.5"},
		{name: "untrusted peer: spoofed headers ignored", remote: "198.51.100.1:1234", xff: []string{"203.0.113.9"}, proto: "https", wantIP: "198.51.100.1"},
		{name: "untrusted peer naming a trusted hop", remote: "198.51.100.1:1234", xff: []string{"10.0.0.7"}, wantIP: "198.51.100.1"},
		{name: "untrusted IPv6 peer", remote: "[2001:db8:1:2::9]:1234", xff: []string{"203.0.113.9"}, wantIP: "2001:db8:1:2::/64"},
		{name: "untrusted peer over TLS", remote: "198.51.100.1:1234", tls: true, wantIP: "198.51.100.1", wantTLS: true},
		{name: "trusted peer without header", remote: "10.0.0.5:1234", wantIP: "10.0.0.5"},
		{name: "trusted peer", remote: "10.0.0.5:1234", xff: []string{"203.0.113.9"}, wantIP: "203.0.113.9"},
		{name: "client-supplied entries left of the real client", remote: "10.0.0.5:1234", xff: []string{"1.2.3.4, 10.0.0.9, 203.0.113.9"}, wantIP: "203.0.113.9"},
		{name: "chain of trusted proxies", remote: "10.0.0.5:1234", xff: []string{"1.2.3.4, 203.0.113.9, 10.0.0.7, 127.0.0.1"}, wantIP: "203.0.113.9"},
		{name: "several header lines", remote: "10.0.0.5:1234", xff: []string{"1.2.3.4", "203.0.113.9, 10.0.0.7"}, wantIP: "203.0.113.9"},
		{name: "every hop trusted: the leftmost", remote: "10.0.0.5:1234", xff: []string{"10.0.0.8, 10.0.0.7"}, wantIP: "10.0.0.8"},
		{name: "empty entries skipped", remote: "10.0.0.5:1234", xff: []string{"203.0.113.9, ,", ""}, wantIP: "203.0.113.9"},
		{name: "malformed entry stops the walk at the last trusted hop", remote: "10.0.0.5:1234", xff: []string{"203.0.113.9, 10.0.0.7, bogus"}, wantIP: "10.0.0.5"},
		{name: "malformed entry behind a trusted hop", remote: "10.0.0.5:1234", xff: []string{"bogus, 10.0.0.7"}, wantIP: "10.0.0.7"},
		{name: "entries with ports", remote: "10.0.0.5:1234", xff: []string{"203.0.113.9:5555, 10.0.0.7:80"}, wantIP: "203.0.113.9"},
		{name: "IPv6 client keyed by /64", remote: "10.0.0.5:1234", xff: []string{"2001:db8:1:2:aaaa::1"}, wantIP: "2001:db8:1:2::/64"},
		{name: "bracketed IPv6 client with port", remote: "10.0.0.5:1234", xff: []string{"[2001:db8:1:2::1]:443"}, wantIP: "2001:db8:1:2::/64"},
		{name: "IPv6 client with zone", remote: "10.0.0.5:1234", xff: []string{"fe80::1%eth0"}, wantIP: "fe80::/64"},
		{name: "IPv6 trusted proxy", remote: "[2001:db8:ffff::1]:443", xff: []string{"198.51.100.4, 2001:db8:ffff:1::2"}, wantIP: "198.51.100.4"},
		{name: "IPv4-mapped peer and entries", remote: "[::ffff:10.0.0.5]:1234", xff: []string{"::ffff:198.51.100.4, ::ffff:10.0.0.7"}, wantIP: "198.51.100.4"},
		{name: "unparseable peer", remote: "pipe", xff: []string{"203.0.113.9"}, proto: "https", wantIP: "pipe"},
		{name: "proto from trusted peer", remote: "10.0.0.5:1234", proto: "https", wantIP: "10.0.0.5", wantTLS: true},
		{name: "proto case-insensitive", remote: "10.0.0.5:1234", proto: "HTTPS", wantIP: "10.0.0.5", wantTLS: true},
		{name: "proto list: the client-facing proxy's", remote: "10.0.0.5:1234", proto: "https, http", wantIP: "10.0.0.5", wantTLS: true},
		{name: "proto http from trusted peer", remote: "10.0.0.5:1234", proto: "http", wantIP: "10.0.0.5"},
		{name: "own TLS wins over proto", remote: "10.0.0.5:1234", proto: "http", tls: true, wantIP: "10.0.0.5", wantTLS: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := testProxies
			if tt.noProxies {
				p = nil
			}
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			for _, v := range tt.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			r = p.resolve(r)
			if got := clientIP(r); got != tt.wantIP {
				t.Errorf("clientIP = %q, want %q", got, tt.wantIP)
			}
			if got := isTLS(r); got != tt.wantTLS {
				t.Errorf("isTLS = %v, want %v", got, tt.wantTLS)
			}
		})
	}
}

// Without the middleware (handlers called directly) only the peer counts.
func TestClientWithoutMiddleware(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.0.0.5:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := clientIP(r); got != "10.0.0.5" {
		t.Errorf("clientIP = %q", got)
	}
	if isTLS(r) {
		t.Error("isTLS from a forwarded header without trusted proxies")
	}
}

func newProxiedHandler(t *testing.T) http.Handler {
	t.Helper()
	h, svc, _ := newTestHandlerOpts(t, func(environments.Environment) (docker.Client, error) { return &dockertest.Fake{}, nil }, func(o *Options) { o.TrustedProxies = testProxies })
	if _, err := svc.CreateFirstUser(context.Background(), "admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	return h
}

// failLogins fails logins (unknown users, so the per-user limit stays out of it) as the
// client that remote and xff make up until it is rate limited.
func failLogins(t *testing.T, h http.Handler, remote, xff string) {
	t.Helper()
	for i := range 30 {
		rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", remote: remote,
			body:    fmt.Sprintf(`{"username":"nobody%d","password":"wrong-password"}`, i),
			headers: map[string]string{"X-Forwarded-For": xff}})
		if rec.Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Fatal("never rate limited")
}

func TestLoginRateLimitBehindProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// attacker and victim: TCP peer and X-Forwarded-For.
		attackerRemote, attackerXFF string
		victimRemote, victimXFF     string
		wantVictim                  int
	}{
		{name: "clients behind a trusted proxy are limited apart",
			attackerRemote: "10.0.0.5:1234", attackerXFF: "203.0.113.9",
			victimRemote: "10.0.0.5:1234", victimXFF: "198.51.100.4", wantVictim: http.StatusOK},
		{name: "a spoofed left entry doesn't escape the limit",
			attackerRemote: "10.0.0.5:1234", attackerXFF: "203.0.113.9",
			victimRemote: "10.0.0.5:1234", victimXFF: "198.51.100.4, 203.0.113.9", wantVictim: http.StatusTooManyRequests},
		{name: "IPv6 clients share their /64",
			attackerRemote: "10.0.0.5:1234", attackerXFF: "2001:db8:1:2::1",
			victimRemote: "[2001:db8:ffff::1]:1234", victimXFF: "2001:db8:1:2::99", wantVictim: http.StatusTooManyRequests},
		{name: "untrusted peer: forged header ignored",
			attackerRemote: "198.51.100.1:1234", attackerXFF: "203.0.113.9",
			victimRemote: "198.51.100.1:1234", victimXFF: "198.51.100.4", wantVictim: http.StatusTooManyRequests},
		{name: "untrusted peer can't pin failures on a forwarded client",
			attackerRemote: "198.51.100.1:1234", attackerXFF: "203.0.113.9",
			victimRemote: "10.0.0.5:1234", victimXFF: "203.0.113.9", wantVictim: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newProxiedHandler(t)
			failLogins(t, h, tt.attackerRemote, tt.attackerXFF)
			rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds, remote: tt.victimRemote,
				headers: map[string]string{"X-Forwarded-For": tt.victimXFF}})
			if rec.Code != tt.wantVictim {
				t.Errorf("victim login: %d, want %d (%s)", rec.Code, tt.wantVictim, rec.Body)
			}
		})
	}
}

func TestSecureCookieBehindProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		remote string
		want   bool
	}{
		{name: "trusted proxy sets X-Forwarded-Proto", remote: "10.0.0.5:1234", want: true},
		{name: "untrusted peer sets X-Forwarded-Proto", remote: "198.51.100.1:1234", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newProxiedHandler(t)
			rec := do(t, h, request{method: "POST", path: "/api/v1/auth/login", body: adminCreds, remote: tt.remote,
				headers: map[string]string{"X-Forwarded-Proto": "https"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("login: %d %s", rec.Code, rec.Body)
			}
			if c := sessionCookieOf(t, rec); c.Secure != tt.want {
				t.Errorf("Secure = %v, want %v", c.Secure, tt.want)
			}
		})
	}
}
