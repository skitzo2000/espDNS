package site

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestNew(t *testing.T) {
	for _, c := range []struct {
		listen, url, proxy string
		loopback, ok       bool
	}{
		{"127.0.0.1:8480", "", "", true, true},                                          // today
		{"0.0.0.0:8480", "", "", false, false},                                          // off loopback, no -url
		{"0.0.0.0:8480", "https://espdns.example.net", "", false, false},                // no -proxy
		{"0.0.0.0:8480", "", "192.0.2.10", false, false},                                // no -url
		{"127.0.0.1:8480", "", "192.0.2.10", true, false},                               // -proxy alone
		{"0.0.0.0:8480", "https://espdns.example.net", "192.0.2.10", false, true},       //
		{"0.0.0.0:8480", "https://espdns.example.net", "192.0.2.0/24,::1", false, true}, //
		{"127.0.0.1:8480", "https://espdns.example.net", "", true, true},                // a proxy on this machine
		{"0.0.0.0:8480", "http://espdns.example.net", "192.0.2.10", false, false},       // not https
		{"0.0.0.0:8480", "https://espdns.example.net/x", "192.0.2.10", false, false},    // a path
		{"0.0.0.0:8480", "https://espdns.example.net", "espdns.example.net", false, false},
	} {
		_, err := New(c.listen, c.url, c.proxy, c.loopback)
		if (err == nil) != c.ok {
			t.Errorf("New(%q, %q, %q): %v, want ok %v", c.listen, c.url, c.proxy, err, c.ok)
		}
	}
	u, _ := ParseURL("https://EspDNS.example.net:443/")
	if u.String() != "https://espdns.example.net" {
		t.Errorf("ParseURL: %s", u)
	}
}

type seen struct{ public, origin, client string }

func run(t *testing.T, c Config, remote, host string, hdr map[string]string) (int, seen) {
	t.Helper()
	var got seen
	h := c.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = seen{map[bool]string{true: "public"}[Public(r)], Origin(r), Client(r)}
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr, r.Host = remote, host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, got
}

func TestGuard(t *testing.T) {
	c, err := New("0.0.0.0:8480", "https://espdns.example.net", "192.0.2.10", false)
	if err != nil {
		t.Fatal(err)
	}
	// The proxy, by the URL's host: public, its origin, the client from X-Forwarded-For's last entry.
	code, got := run(t, c, "192.0.2.10:5000", "espdns.example.net", map[string]string{"X-Forwarded-For": "203.0.113.9, 198.51.100.7"})
	if code != 200 || got != (seen{"public", "https://espdns.example.net", "198.51.100.7"}) {
		t.Errorf("proxy: %d %+v", code, got)
	}
	// Any other source: 403 before anything, whatever its Host.
	for _, remote := range []string{"192.0.2.11:5000", "198.51.100.7:5000"} {
		if code, _ := run(t, c, remote, "espdns.example.net", nil); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", remote, code)
		}
	}
	// The proxy with another Host: 403; with a localhost name: 403 too (not this machine).
	for _, host := range []string{"evil.example", "127.0.0.1:8480"} {
		if code, _ := run(t, c, "192.0.2.10:5000", host, nil); code != http.StatusForbidden {
			t.Errorf("proxy, Host %s: %d, want 403", host, code)
		}
	}
	// Loopback: by a localhost name (the health check), not public.
	code, got = run(t, c, "127.0.0.1:5000", "localhost:8480", nil)
	if code != 200 || got != (seen{"", "", "127.0.0.1"}) {
		t.Errorf("loopback: %d %+v", code, got)
	}
	// Loopback by a localhost name but relayed (a proxy on this machine passing a client's
	// Host on): 403, so it is never taken for this machine's own browser.
	for _, k := range []string{"X-Forwarded-For", "Forwarded", "X-Real-IP"} {
		if code, _ := run(t, c, "127.0.0.1:5000", "localhost:8480", map[string]string{k: "for=203.0.113.9"}); code != http.StatusForbidden {
			t.Errorf("loopback, localhost, %s: %d, want 403", k, code)
		}
	}
	// By the URL's host from loopback: public, X-Forwarded-For not read (not a -proxy source).
	code, got = run(t, c, "127.0.0.1:5000", "espdns.example.net", map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if code != 200 || got != (seen{"public", "https://espdns.example.net", "127.0.0.1"}) {
		t.Errorf("loopback by the URL: %d %+v", code, got)
	}
	// An IPv4-mapped proxy network matches the proxy's plain IPv4 source.
	if ps, _ := ParseProxies(" ::ffff:192.0.2.0/120 , "); len(ps) != 1 || !ps[0].Contains(netip.MustParseAddr("192.0.2.10")) {
		t.Errorf("ParseProxies(::ffff:192.0.2.0/120): %v", ps)
	}
	if code, _ := run(t, c, "127.0.0.1:5000", "evil.example", nil); code != http.StatusForbidden {
		t.Errorf("loopback, another Host: %d, want 403", code)
	}
	// Without -url: nothing checked, nothing marked.
	code, got = run(t, Config{}, "198.51.100.7:5000", "anything", map[string]string{"X-Forwarded-For": "203.0.113.9"})
	if code != 200 || got != (seen{"", "", "198.51.100.7"}) {
		t.Errorf("no -url: %d %+v", code, got)
	}
}

// This machine to its own LAN address (the health check with -listen on it) counts as
// local; the same Host from the proxy's address doesn't (TestGuard).
func TestGuardOwnAddress(t *testing.T) {
	var ip net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP
			break
		}
	}
	if ip == nil {
		t.Skip("no non-loopback IPv4 address here")
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Skip(err)
	}
	c, _ := New(l.Addr().String(), "https://espdns.example.net", "192.0.2.10", false)
	srv := httptest.NewUnstartedServer(c.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	for host, want := range map[string]int{"localhost:8480": 200, "espdns.example.net": 200, "evil.example": 403} {
		r, _ := http.NewRequest("GET", srv.URL, nil)
		r.Host = host
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("from %s to itself, Host %s: %d, want %d", ip, host, resp.StatusCode, want)
		}
	}
}
