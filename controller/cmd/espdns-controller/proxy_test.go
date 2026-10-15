package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/site"
)

// Behind an HTTPS reverse proxy (-url, -proxy), through the whole handler: only the proxy
// and this machine are served, by the URL's Host; the API takes the URL's Origin; the login
// cookie is Secure; the first password is never set through the proxy.
func TestBehindProxy(t *testing.T) {
	cfg, err := site.New("0.0.0.0:8480", "https://espdns.example.net", "192.0.2.10", false)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := testServer(t, true, func(s *server) { s.site = cfg })
	req := func(method, path, remote, host, origin, body string) *http.Request {
		r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return r
	}
	const proxy, other, host, origin = "192.0.2.10:40000", "192.0.2.99:40000", "espdns.example.net", "https://espdns.example.net"
	creds := `{"user":"admin","password":"` + testPassword + `"}`

	// Another source: 403 for the login page, a script and the login itself.
	for _, p := range []string{"GET /login.html", "GET /common.js", "POST /api/login"} {
		m, path, _ := strings.Cut(p, " ")
		if w := serve(h, req(m, path, other, host, origin, creds)); w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s from another source: %d", p, w.Code)
		}
	}
	// The proxy by another Host, or with another Origin: refused.
	if w := serve(h, req("GET", "/login.html", proxy, "evil.example", "", "")); w.Code != http.StatusForbidden {
		t.Errorf("another Host: %d", w.Code)
	}
	for _, o := range []string{"http://espdns.example.net", "https://evil.example"} {
		if w := serve(h, req("POST", "/api/login", proxy, host, o, creds)); w.Code != http.StatusForbidden {
			t.Errorf("Origin %s: %d", o, w.Code)
		}
	}
	// The proxy, the URL's Host and Origin: logged in, the cookie Secure, HttpOnly, Strict.
	w := serve(h, req("POST", "/api/login", proxy, host, origin, creds))
	if w.Code != http.StatusOK {
		t.Fatalf("login through the proxy: %d %s", w.Code, w.Body)
	}
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].Secure || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie through the proxy: %+v", c)
	}
	// Loopback by a localhost name (the health check, an SSH tunnel): served as today, the
	// cookie not Secure (plain http://localhost).
	if w := serve(h, req("GET", "/", "127.0.0.1:40000", "localhost:8480", "", "")); w.Code >= 400 {
		t.Errorf("loopback GET /: %d", w.Code)
	}
	w = serve(h, req("POST", "/api/login", "127.0.0.1:40000", "127.0.0.1:8480", "http://127.0.0.1:8480", creds))
	if c := w.Result().Cookies(); w.Code != http.StatusOK || len(c) != 1 || c[0].Secure {
		t.Errorf("loopback login: %d %+v", w.Code, c)
	}
}

// The first password through a proxy on this machine (a loopback source, the URL's Host):
// refused; only this machine's own browser sets it.
func TestFirstPasswordNotThroughProxy(t *testing.T) {
	cfg, _ := site.New("127.0.0.1:8480", "https://espdns.example.net", "", true)
	h, _ := testServer(t, false, func(s *server) { s.site = cfg })
	r := httptest.NewRequest("POST", "https://espdns.example.net/api/setup/password", strings.NewReader(`{"password":"a long first password"}`))
	r.RemoteAddr = "127.0.0.1:40000"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://espdns.example.net")
	if w := serve(h, r); w.Code != http.StatusForbidden {
		t.Errorf("first password through the proxy: %d %s", w.Code, w.Body)
	}
}
