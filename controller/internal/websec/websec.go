// Package websec is the security headers every response of the controller carries: a
// Content-Security-Policy that lets a page run only the controller's own scripts and styles
// (no inline code, no other origin), connect only to the controller, and headers that keep
// other sites from framing a page,
// reading a response or learning where a page was.
package websec

import (
	"net/http"
)

// CSP is the Content-Security-Policy. img-src allows data: for the pages' favicons.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// Set is every header Headers sets, with its value, in the order set.
var Set = [][2]string{
	{"Content-Security-Policy", CSP},
	{"X-Frame-Options", "DENY"},
	{"X-Content-Type-Options", "nosniff"},
	{"Referrer-Policy", "no-referrer"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Cross-Origin-Resource-Policy", "same-origin"},
	// Web Serial for the Install over USB panel's flasher, from this origin only; nothing else.
	{"Permissions-Policy", "serial=(self), usb=(), camera=(), microphone=(), geolocation=()"},
}

// Headers sets the headers on every response, before next runs: outermost, so a refusal,
// a redirect or a not-found from anything inside it carries them too.
func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for _, kv := range Set {
			h.Set(kv[0], kv[1])
		}
		next.ServeHTTP(w, r)
	})
}
