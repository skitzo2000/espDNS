// Package websec is the security headers every response of the controller carries: a
// Content-Security-Policy that lets a page run only the controller's own scripts and styles
// (no inline code, no other origin; the vendored esp-web-tools is the controller's own) and
// connect only to the controller, and headers that keep other sites from framing a page,
// reading a response or learning where a page was.
package websec

import (
	"net/http"
	"slices"
	"strings"
)

// CSP is the Content-Security-Policy. img-src allows data: for the pages' favicons.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// USBPages are the pages whose CSP is USBCSP: Nodes, at its two paths, and the first run,
// where + Add node and a found node's setup open the Install over USB panel (web/usb.js)
// with the flasher in it.
var USBPages = []string{"/", "/index.html", "/setup.html"}

// USBStyleHashes are the SHA-256 hashes (base64) of the only static inline style attributes
// the vendored esp-web-tools has, both in its "no port picked" dialog (shown when the port
// picker is cancelled): style="width: 28px; vertical-align: middle;" on an icon's svg and
// style="fill:currentColor;" on its path. The vendored files stay as the registry ships them
// (SHA256SUMS); websec_test.go recomputes these from the embedded copy, so an update of
// esp-web-tools that changes them fails there.
var USBStyleHashes = []string{
	"sha256-w68cv7ZL7DW/1c2v0g4i6aPlMi0iRw663JuMxEmNmU4=", // width: 28px; vertical-align: middle;
	"sha256-2PZQPqAcY6IE7H879XiZ2Hm3cBUNVB41T1m3kjNvN6E=", // fill:currentColor;
}

// USBStyleMapHashes are the hashes of the style attributes the install dialog's Wi-Fi form
// (Improv, after installing a Wi-Fi board) sets when it is first drawn: Lit's styleMap sets
// the attribute once, then changes it through the CSSOM, which the CSP allows. The text
// fields' input "direction:;" (textDirection ""), the menu's surface "display:none;" (its
// position controller's start) and the network select's menu "--__menu-min-width:0px;"
// (selectWidth 0). websec_test.go checks that the embedded copy still starts them so, and
// e2e/tests/csp.spec.js draws the form under the CSP.
var USBStyleMapHashes = []string{
	"sha256-EDKRS+IvBWVSFJYjnWOymFAGVOawal4UoghutFq1ngI=", // direction:;
	"sha256-0EZqoz+oBhx7gF4nvY2bSqoGyy4zLjNF+SDQXGp/ZrY=", // display:none;
	"sha256-rZZVnJxMO/XQV/2QyewiamVXFmVWYmr6mN1ilULHFrU=", // --__menu-min-width:0px;
}

// USBCSP is CSP plus, for style attributes only, exactly those values: no other inline
// style, no inline script, on USBPages and nowhere else.
var USBCSP = CSP + "; style-src-attr 'unsafe-hashes' '" +
	strings.Join(append(slices.Clone(USBStyleHashes), USBStyleMapHashes...), "' '") + "'"

// CSPFor is the Content-Security-Policy of a response to a request for path:
// USBCSP for USBPages, CSP for everything else.
func CSPFor(path string) string {
	if slices.Contains(USBPages, path) {
		return USBCSP
	}
	return CSP
}

// Set is every header Headers sets, with its value, in the order set; the
// Content-Security-Policy is CSPFor's for the request's path.
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
		h.Set("Content-Security-Policy", CSPFor(r.URL.Path))
		next.ServeHTTP(w, r)
	})
}
