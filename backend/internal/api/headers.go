package api

import "net/http"

// contentSecurityPolicy lets the page load only the app's own files. Inline styles are
// allowed because the terminal (xterm) creates style elements and attributes with content
// computed at runtime, which no hash or nonce can cover; our own code uses none. The
// data: images are the ones in xterm's and our style sheets.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders sets the response headers that limit what a bug in the page could do.
// A script with the session could open a shell in any container, which is root on the host.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
