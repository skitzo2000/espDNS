package fakenode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/release"
)

// Its /status says where its keys came from as the firmware does (firmware/main/keys_node.h),
// and says nothing of it as firmware from before the keys partition.
func TestStatusKeys(t *testing.T) {
	n := New("espdns-0a0b0c", [6]byte{2, 0, 0, 0, 0, 1}, "esp32p4", "p4-ip101", nil)
	srv := httptest.NewServer(n)
	defer srv.Close()
	n.Addr = strings.TrimPrefix(srv.URL, "http://")
	p := &release.Pusher{}
	for _, c := range []struct {
		set    func(n *Node)
		source string
		gen    *uint32
		err    string
	}{
		{func(n *Node) {}, "partition", ptr(1), ""},
		{func(n *Node) { n.KeysGen = 4 }, "partition", ptr(4), ""},
		{func(n *Node) { n.KeysSource, n.KeysGen = "seeded", 0 }, "seeded", ptr(1), ""},
		{func(n *Node) { n.KeysSource, n.KeysError = "corrupt", "no valid slot" }, "corrupt", nil, "no valid slot"},
		{func(n *Node) { n.NoKeysPartition = true }, "", nil, ""},
	} {
		n.Do(c.set)
		st, err := p.Status(context.Background(), n.Addr)
		if err != nil {
			t.Fatal(err)
		}
		if st.KeysSource != c.source || st.KeysError != c.err || (st.KeysGen == nil) != (c.gen == nil) ||
			(c.gen != nil && *st.KeysGen != *c.gen) {
			t.Errorf("want %q %v %q: source %q gen %v error %q", c.source, c.gen, c.err, st.KeysSource, st.KeysGen,
				st.KeysError)
		}
	}
}

func ptr(v uint32) *uint32 { return &v }

// The fake node checks Host as the firmware does (firmware/main/httpguard.h): the
// controller addresses nodes by IP, which passes; a page rebound to the node's address
// (DNS rebinding) sends its own name, which is refused.
func TestHostCheck(t *testing.T) {
	n := New("espdns-0a0b0c", [6]byte{2, 0, 0, 0, 0, 1}, "esp32p4", "p4-ip101", nil)
	srv := httptest.NewServer(n)
	defer srv.Close()
	n.Addr = strings.TrimPrefix(srv.URL, "http://")
	host, _, _ := strings.Cut(n.Addr, ":")
	for _, c := range []struct {
		host string
		want int
	}{
		{"", http.StatusOK}, // the URL's own host:port, as the controller sends it
		{host, http.StatusOK},
		{host + ":80", http.StatusOK},
		{"espdns-0a0b0c.local", http.StatusOK},
		{"ESPDNS-0A0B0C.local.", http.StatusOK},
		{"evil.example", http.StatusMisdirectedRequest},
		{"evil.example:80", http.StatusMisdirectedRequest},
		{host + ".evil.example", http.StatusMisdirectedRequest},
		{"[::1]", http.StatusMisdirectedRequest},
	} {
		for _, path := range []string{"/status", "/health"} {
			req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
			if c.host != "" {
				req.Host = c.host
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != c.want {
				t.Errorf("Host %q %s: %d, want %d", c.host, path, resp.StatusCode, c.want)
			}
		}
	}
	// Its configured name, once a config names it
	n.Do(func(n *Node) { n.cfg.Name = "dns-a.example.com" })
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/status", nil)
	req.Host = "dns-a.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("configured name: %d", resp.StatusCode)
	}
}
