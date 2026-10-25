package api

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// proxies are the reverse proxies whose X-Forwarded-For and X-Forwarded-Proto headers
// are believed. Anyone else can forge them, so for every other peer they are ignored.
type proxies []netip.Prefix

// requester is who sent a request: the address the login limiter keys on and logs show,
// and whether the browser reached nudge over HTTPS.
type requester struct {
	ip  string
	tls bool
}

type requesterKey struct{}

// handler resolves the client of every request once, for clientIP and isTLS.
func (p proxies) handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, p.resolve(r))
	})
}

// resolve returns r carrying its client.
func (p proxies) resolve(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requesterKey{}, p.requester(r)))
}

// requester is the TCP peer, unless the peer is a trusted proxy: then it is the right-most
// X-Forwarded-For entry that isn't a trusted proxy (entries further left come from the
// client and are forgeable), and X-Forwarded-Proto: https counts as HTTPS. A malformed
// entry ends the walk at the last trusted hop; if every hop is trusted, the left-most.
func (p proxies) requester(r *http.Request) requester {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return requester{ip: host, tls: r.TLS != nil}
	}
	addr := peer.Unmap()
	if !p.trusts(addr) {
		return requester{ip: keyOf(addr), tls: r.TLS != nil}
	}
	tls := r.TLS != nil || forwardedHTTPS(r.Header.Get("X-Forwarded-Proto"))
	hops := forwardedFor(r.Header.Values("X-Forwarded-For"))
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseHop(hops[i])
		if !ok {
			break
		}
		addr = hop
		if !p.trusts(addr) {
			break
		}
	}
	return requester{ip: keyOf(addr), tls: tls}
}

func (p proxies) trusts(addr netip.Addr) bool {
	addr = addr.WithZone("")
	for _, prefix := range p {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// forwardedFor is the non-empty entries of every X-Forwarded-For line, in order.
func forwardedFor(lines []string) []string {
	var hops []string
	for _, line := range lines {
		for hop := range strings.SplitSeq(line, ",") {
			if hop = strings.TrimSpace(hop); hop != "" {
				hops = append(hops, hop)
			}
		}
	}
	return hops
}

// parseHop parses an X-Forwarded-For entry: an address, optionally with a port, IPv6
// optionally in brackets ([2001:db8::1] or [2001:db8::1]:443).
func parseHop(s string) (netip.Addr, bool) {
	if inner, ok := strings.CutPrefix(s, "["); ok {
		if inner, ok = strings.CutSuffix(inner, "]"); ok {
			s = inner
		}
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		host, _, splitErr := net.SplitHostPort(s)
		if splitErr != nil {
			return netip.Addr{}, false
		}
		if addr, err = netip.ParseAddr(host); err != nil {
			return netip.Addr{}, false
		}
	}
	return addr.Unmap(), true
}

// forwardedHTTPS reports whether X-Forwarded-Proto says https; in a list, the first
// value is the one the client-facing proxy set.
func forwardedHTTPS(proto string) bool {
	first, _, _ := strings.Cut(proto, ",")
	return strings.EqualFold(strings.TrimSpace(first), "https")
}

// keyOf is addr as the limiter keys on it: IPv4 as is, IPv6 as its /64 (one client
// usually holds a whole /64).
func keyOf(addr netip.Addr) string {
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr.WithZone(""), 64).Masked().String()
}

// clientFrom is r's client as the middleware resolved it; without the middleware (a
// handler called directly) only the peer counts.
func clientFrom(r *http.Request) requester {
	if c, ok := r.Context().Value(requesterKey{}).(requester); ok {
		return c
	}
	return proxies(nil).requester(r)
}

// clientIP is the client's address, as its /64 for IPv6. Forwarded headers count only
// from trusted proxies (Options.TrustedProxies); otherwise it is the TCP peer, and
// behind an untrusted proxy every client shares the proxy's rate limit.
func clientIP(r *http.Request) string { return clientFrom(r).ip }

// isTLS reports whether the browser reached us over HTTPS, directly or through a trusted
// TLS-terminating proxy (X-Forwarded-Proto: https).
func isTLS(r *http.Request) bool { return clientFrom(r).tls }
