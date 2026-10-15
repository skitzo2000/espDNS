package websec

import (
	"crypto/sha256"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/web"
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

// The pages that open Install over USB (USBPages): the same, and style attributes with exactly the vendored esp-web-tools'
// two static values and the three its Wi-Fi form's styleMaps start with.
const wantUSBCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; " +
	"style-src-attr 'unsafe-hashes' 'sha256-w68cv7ZL7DW/1c2v0g4i6aPlMi0iRw663JuMxEmNmU4=' 'sha256-2PZQPqAcY6IE7H879XiZ2Hm3cBUNVB41T1m3kjNvN6E=' " +
	"'sha256-EDKRS+IvBWVSFJYjnWOymFAGVOawal4UoghutFq1ngI=' 'sha256-0EZqoz+oBhx7gF4nvY2bSqoGyy4zLjNF+SDQXGp/ZrY=' 'sha256-rZZVnJxMO/XQV/2QyewiamVXFmVWYmr6mN1ilULHFrU='"

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
func TestHeaders(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"ok":       func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<!doctype html>")) },
		"error":    func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusUnauthorized) },
		"redirect": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login.html", http.StatusSeeOther) },
		"notfound": http.NotFound,
		"nothing":  func(w http.ResponseWriter, r *http.Request) {},
	}
	paths := map[string]string{
		"/":               wantUSBCSP,
		"/index.html":     wantUSBCSP,
		"/index.html?x=1": wantUSBCSP,
		"/login.html":     want["Content-Security-Policy"],
		"/node.html":      wantUSBCSP,
		"/usb.js":         want["Content-Security-Policy"],
		"/index.html/":    want["Content-Security-Policy"],
		"/vendor/esp-web-tools/install-button.js": want["Content-Security-Policy"],
		"/api/build": want["Content-Security-Policy"],
	}
	for name, h := range handlers {
		for p, csp := range paths {
			w := httptest.NewRecorder()
			Headers(h).ServeHTTP(w, httptest.NewRequest("GET", "http://127.0.0.1:8480"+p, nil))
			for k, v := range want {
				if k == "Content-Security-Policy" {
					v = csp
				}
				if got := w.Header().Values(k); len(got) != 1 || got[0] != v {
					t.Errorf("%s %s: %s = %q", name, p, k, got)
				}
			}
		}
	}
}

func TestCSPFor(t *testing.T) {
	for _, p := range []string{"/", "/index.html", "/setup.html", "/node.html"} {
		if got := CSPFor(p); got != wantUSBCSP {
			t.Errorf("%s: %q", p, got)
		}
	}
	for _, p := range []string{"/Index.html", "/index", "/x/index.html", "/usb.js", "/zones.html"} {
		if got := CSPFor(p); got != want["Content-Security-Policy"] {
			t.Errorf("%s: %q", p, got)
		}
	}
}

// USBStyleHashes are the hashes of exactly the static style attributes the embedded
// esp-web-tools has, recomputed from its files: an update that changes, adds or drops one
// fails here, and USBStyleHashes (and the README) are updated with it.
func TestUSBStyleHashes(t *testing.T) {
	attr := regexp.MustCompile(`\bstyle="([^"]*)"`)
	const dir = "vendor/esp-web-tools"
	files, err := fs.ReadDir(web.Files, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		b, err := fs.ReadFile(web.Files, path.Join(dir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range attr.FindAllSubmatch(b, -1) {
			sum := sha256.Sum256(m[1])
			h := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
			t.Logf("%s: style=%q: %s", f.Name(), m[1], h)
			if !slices.Contains(got, h) {
				got = append(got, h)
			}
		}
	}
	want := slices.Clone(USBStyleHashes)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the vendored esp-web-tools' style attributes hash to %q, USBStyleHashes is %q", got, want)
	}
	if len(want) != 2 {
		t.Errorf("%d hashes, want the dialog's 2", len(want))
	}
}

// USBStyleMapHashes are the hashes of the values the embedded esp-web-tools' styleMaps set
// first: each still starts so in the bundle, and there are no other styleMap bindings. An
// update that changes one fails here; update USBStyleMapHashes (and the README, and
// e2e/tests/csp.spec.js) with it, and run make e2e, which draws the Wi-Fi form under the CSP.
func TestUSBStyleMapHashes(t *testing.T) {
	b, err := fs.ReadFile(web.Files, "vendor/esp-web-tools/install-dialog-im156JnI.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	// Each value, and what in the bundle starts it so.
	for _, c := range []struct {
		value string
		from  []string
	}{
		{"direction:;", []string{`this.textDirection=""`, `e={direction:this.textDirection}`}},
		{"display:none;", []string{`this.surfaceStylesInternal={display:"none"}`, `style=${Ns(this.menuPositionController.surfaceStyles)}`}},
		{"--__menu-min-width:0px;", []string{`this.selectWidth=0`, "style=${Ns({\"--__menu-min-width\":`${this.selectWidth}px`,\"--__menu-max-width\":this.clampMenuWidth?`${this.selectWidth}px`:void 0})}"}},
	} {
		for _, f := range c.from {
			if !strings.Contains(src, f) {
				t.Errorf("%q: the bundle no longer has %s", c.value, f)
			}
		}
		sum := sha256.Sum256([]byte(c.value))
		if h := "sha256-" + base64.StdEncoding.EncodeToString(sum[:]); !slices.Contains(USBStyleMapHashes, h) {
			t.Errorf("%q: %s not in USBStyleMapHashes", c.value, h)
		}
	}
	// The styleMap bindings: the text field's input and textarea, the menu, the select's menu.
	if n := strings.Count(src, "style=${Ns("); n != 4 {
		t.Errorf("%d styleMap bindings, want 4", n)
	}
	if len(USBStyleMapHashes) != 3 {
		t.Errorf("%d hashes, want 3", len(USBStyleMapHashes))
	}
	for _, f := range []string{"install-button.js", "index-Dt9w-IG1.js", "styles-sT2V1cOw.js"} {
		b, _ := fs.ReadFile(web.Files, "vendor/esp-web-tools/"+f)
		if strings.Contains(string(b), "style=${") {
			t.Errorf("%s: a style binding", f)
		}
	}
}
