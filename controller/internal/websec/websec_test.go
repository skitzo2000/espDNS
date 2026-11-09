package websec

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Exactly these headers, with exactly these values.
var want = map[string]string{
	"Content-Security-Policy":      "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
	"X-Frame-Options":              "DENY",
	"X-Content-Type-Options":       "nosniff",
	"Referrer-Policy":              "no-referrer",
	"Cross-Origin-Opener-Policy":   "same-origin",
	"Cross-Origin-Resource-Policy": "same-origin",
	"Permissions-Policy":           "serial=(self), usb=(), camera=(), microphone=(), geolocation=()",
}

func TestSet(t *testing.T) {
	if len(Set) != len(want) {
		t.Fatalf("%d headers, want %d", len(Set), len(want))
	}
	for _, kv := range Set {
		if want[kv[0]] != kv[1] {
			t.Errorf("%s: %q, want %q", kv[0], kv[1], want[kv[0]])
		}
	}
}

// Every response gets them: a page, an error, a redirect, a not-found, one that wrote nothing.
// Every path gets the same CSP, no style-src-attr and no hash: the pages carry no inline
// style attribute, not even on the ones that open Install over USB.
func TestHeaders(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"ok":       func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<!doctype html>")) },
		"error":    func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) },
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login.html", http.StatusSeeOther) },
		"notfound": http.NotFound,
		"nothing":  func(w http.ResponseWriter, r *http.Request) {},
	}
	paths := []string{
		"/", "/index.html", "/index.html?x=1", "/setup.html", "/node.html",
		"/Index.html", "/index", "/x/index.html", "/index.html/", "/usb.js",
		"/login.html", "/vendor/esptool-js/bundle.js", "/api/build",
	}
	for name, h := range handlers {
		for _, p := range paths {
			w := httptest.NewRecorder()
			Headers(h).ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8480"+p, nil))
			for k, v := range want {
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s %s: %s = %q", name, p, k, got)
				}
			}
		}
	}
}

// The CSP itself: nothing hashed, nothing from another origin, and no style-src-attr.
func TestCSPNoHashes(t *testing.T) {
	for _, s := range []string{"unsafe-hashes", "sha256-", "style-src-attr", "http://", "https://", "inline"} {
		if strings.Contains(CSP, s) {
			t.Errorf("CSP contains %q: %s", s, CSP)
		}
	}
}
