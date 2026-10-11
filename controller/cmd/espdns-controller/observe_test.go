package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/observe"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
)

// The dashboard's and the query log's APIs, on a fake node read twice: JSON, the query log
// behind the login, its filters, and a hostile name kept as text (escaped in the JSON).
func TestObserveAPI(t *testing.T) {
	n := fakenode.New("dns-a", [6]byte{2, 0, 0, 0, 0, 1}, "esp32p4-rev1", "p4-ip101", nil)
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	node := nodes.Node{ID: n.ID(), Addr: strings.TrimPrefix(srv.URL, "http://"), Online: true,
		Status: map[string]any{"health": map[string]any{"state": "healthy"}}}
	store := observe.New(&fleet.Client{})
	ctx := context.Background()
	store.Scrape(ctx, []nodes.Node{node})
	n.LogQuery("192.0.2.10", "<img src=x onerror=alert(1)>.example", "A", "blocked", "NOERROR")
	n.LogQuery("192.0.2.10", "ok.example", "AAAA", "cache", "NOERROR")
	store.Scrape(ctx, []nodes.Node{node})

	n.LogQuery("192.0.2.11", "printer-cloud.example", "A", "servfail", "SERVFAIL")
	store.Scrape(ctx, []nodes.Node{node})

	var dataDir string
	h, _ := testServer(t, true, func(s *server) { s.observe = store; dataDir = s.dataDir })
	if _, err := zonefiles.Save(dataDir, "home.example.zone", []byte("$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. "+
		"( 5 3600 600 604800 300 )\n  IN NS ns1\nns1 IN A 192.0.2.53\nlaptop IN A 192.0.2.10\n"), ""); err != nil {
		t.Fatal(err)
	}
	cookie, tok := login(t, h)
	get := func(path string, session bool) *httptest.ResponseRecorder {
		r := request("GET " + path)
		if session {
			withSession(r, cookie, tok)
		}
		return serve(h, r)
	}

	w := get("/api/dashboard", true)
	var d observe.Dashboard
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &d) != nil || len(d.Nodes) != 1 || d.Bins != 60 {
		t.Fatalf("dashboard: %d %s", w.Code, w.Body)
	}
	if d.Nodes[0].Metrics.State != "ok" || len(d.Fleet.QPS) != 60 {
		t.Errorf("dashboard: %+v", d.Nodes[0])
	}

	if w := get("/api/querylog", false); !refusedByLogin(w) {
		t.Errorf("query log without a session: %d", w.Code)
	}
	w = get("/api/querylog?result=blocked", true)
	if strings.Contains(w.Body.String(), "<img") {
		t.Errorf("a name's markup in the JSON as is: %s", w.Body)
	}
	var l observe.LogView
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &l) != nil || len(l.Entries) != 1 ||
		l.Entries[0].QName != "<img src=x onerror=alert(1)>.example" || l.Entries[0].Node != n.ID() {
		t.Fatalf("query log: %d %s", w.Code, w.Body)
	}
	w = get("/api/querylog?qtype=aaaa&after=0&limit=5", true)
	if json.Unmarshal(w.Body.Bytes(), &l) != nil || len(l.Entries) != 1 || l.Entries[0].QName != "ok.example" {
		t.Errorf("qtype: %s", w.Body)
	}
	for _, bad := range []string{"after=x", "limit=0", "limit=-1"} {
		if w := get("/api/querylog?"+bad, true); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	// The devices' names, from the hosted zones' records; the box finds a device by its name;
	// a device exactly; several results.
	var named struct {
		observe.LogView
		Names map[string]string `json:"names"`
	}
	w = get("/api/querylog?q=LAPTOP", true)
	if json.Unmarshal(w.Body.Bytes(), &named) != nil || len(named.Entries) != 2 || named.Names["192.0.2.10"] != "laptop.home.example" ||
		len(named.Names) != 1 {
		t.Errorf("by a device's name: %s", w.Body)
	}
	for q, want := range map[string]int{"device=192.0.2.1": 0, "device=192.0.2.11": 1, "result=servfail,refused,error": 1,
		"result=blocked,overridden": 1, "q=cloud": 1, "q=192.0.2.1": 3} {
		w = get("/api/querylog?"+q, true)
		if json.Unmarshal(w.Body.Bytes(), &named) != nil || len(named.Entries) != want {
			t.Errorf("%s: %d entries, want %d: %s", q, len(named.Entries), want, w.Body)
		}
	}
	var top struct {
		observe.Top
		Names map[string]string `json:"names"`
	}
	w = get("/api/querylog/top?node="+n.ID(), true)
	if json.Unmarshal(w.Body.Bytes(), &top) != nil || top.Entries != 3 || len(top.Blocked) != 1 || len(top.Clients) != 2 ||
		top.Clients[0] != (observe.TopName{Name: "192.0.2.10", Count: 2}) || top.Names["192.0.2.10"] != "laptop.home.example" {
		t.Errorf("top: %s", w.Body)
	}
}

// Who asked for a site (Blocking's "check a site"): every device, the most recent first,
// named from a reverse zone's PTR records, a forward zone's A records or a node's name,
// and marked when it is a gateway (in a config, or the one a node says it runs with).
func TestCheckSiteAskers(t *testing.T) {
	n := fakenode.New("dns-a", [6]byte{2, 0, 0, 0, 0, 1}, "esp32p4-rev1", "p4-ip101", nil)
	srv := httptest.NewServer(n)
	t.Cleanup(srv.Close)
	node := nodes.Node{ID: n.ID(), Addr: strings.TrimPrefix(srv.URL, "http://"), Online: true,
		Status: map[string]any{"health": map[string]any{"state": "healthy"}, "config": map[string]any{"name": "dns1", "gateway": "192.0.2.254"}}}
	other := nodes.Node{Addr: "192.0.2.54", Hostname: "espdns-abcdef.local", Status: map[string]any{"config": map[string]any{"name": "dns2"}}}
	store := observe.New(&fleet.Client{})
	ctx := context.Background()
	store.Scrape(ctx, []nodes.Node{node})
	for _, c := range []string{"192.0.2.1", "192.0.2.40", "192.0.2.10", "192.0.2.54", "192.0.2.254", "192.0.2.1", "192.0.2.77"} {
		n.LogQuery(c, "ads.example.com", "A", "blocked", "NOERROR")
	}
	n.LogQuery("192.0.2.99", "other.example.com", "A", "cache", "NOERROR")
	store.Scrape(ctx, []nodes.Node{node})

	var dataDir string
	h, _ := testServer(t, true, func(s *server) {
		s.observe = store
		dataDir = s.dataDir
		s.nodes = func() []nodes.Node { return []nodes.Node{node, other} }
	})
	soa := "$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. ( 5 3600 600 604800 300 )\n  IN NS ns1.home.example.\n"
	for f, z := range map[string]string{"home.example.zone": "laptop IN A 192.0.2.10\nprinter IN A 192.0.2.40\n",
		"2.0.192.in-addr.arpa.zone": "40 IN PTR office-printer.home.example.\n"} {
		if _, err := zonefiles.Save(dataDir, f, []byte(soa+z), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := configs.Save(dataDir, "dns1.json", []byte(`{"name":"dns1","network":{"address":"192.0.2.53/24","gateway":"192.0.2.1"}}`), ""); err != nil {
		t.Fatal(err)
	}
	cookie, tok := login(t, h)
	get := func(path string, session bool) *httptest.ResponseRecorder {
		r := request("GET " + path)
		if session {
			withSession(r, cookie, tok)
		}
		return serve(h, r)
	}
	if w := get("/api/querylog/askers?name=ads.example.com", false); !refusedByLogin(w) {
		t.Errorf("without a session: %d", w.Code)
	}
	if w := get("/api/querylog/askers?name=bad..name", true); w.Code != http.StatusBadRequest {
		t.Errorf("a bad name: %d", w.Code)
	}
	var v struct {
		Name    string `json:"name"`
		Queries int    `json:"queries"`
		More    int    `json:"more"`
		Cap     int    `json:"cap"`
		Askers  []struct {
			Client  *string  `json:"client"`
			Name    string   `json:"name"`
			Queries int      `json:"queries"`
			Nodes   []string `json:"nodes"`
			Gateway bool     `json:"gateway"`
		} `json:"askers"`
	}
	w := get("/api/querylog/askers?name=ADS.example.com.", true)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v.Name != "ads.example.com" || v.Queries != 7 || v.More != 0 || v.Cap != observe.LogCap || len(v.Askers) != 6 {
		t.Fatalf("%s", w.Body)
	}
	got := map[string]string{}
	for _, a := range v.Askers {
		if a.Client == nil || len(a.Nodes) != 1 || a.Nodes[0] != n.ID() {
			t.Errorf("asker %+v", a)
			continue
		}
		g := a.Name
		if a.Gateway {
			g += " (gateway)"
		}
		got[*a.Client] = g
	}
	want := map[string]string{"192.0.2.1": " (gateway)", "192.0.2.40": "office-printer.home.example", "192.0.2.10": "laptop.home.example",
		"192.0.2.54": "dns2", "192.0.2.254": " (gateway)", "192.0.2.77": ""}
	if !maps.Equal(got, want) {
		t.Errorf("askers %v, want %v", got, want)
	}
	if *v.Askers[0].Client != "192.0.2.77" {
		t.Errorf("not the most recent first: %s", w.Body)
	}
	for _, a := range v.Askers {
		if *a.Client == "192.0.2.1" && a.Queries != 2 {
			t.Errorf("the gateway asked twice: %+v", a)
		}
	}
	w = get("/api/querylog/askers?name=never.example.com", true)
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || len(v.Askers) != 0 || v.Queries != 0 || !strings.Contains(w.Body.String(), `"askers":[]`) {
		t.Errorf("none: %s", w.Body)
	}
}
