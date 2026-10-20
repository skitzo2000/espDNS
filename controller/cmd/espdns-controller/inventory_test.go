package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/inventory"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/settings"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
)

// fakeZonePrimary is a zone primary over DNS, whatever its software: it answers an SOA
// query for a zone it holds authoritatively, refuses everything else, and records who was
// asked. No query leaves the test.
type fakeZonePrimary struct {
	mu    sync.Mutex
	at    string
	zones map[string]uint32
	asked []string
}

func (f *fakeZonePrimary) Exchange(_ context.Context, host string, m *dns.Msg) (*dns.Msg, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, host)
	r := new(dns.Msg)
	r.SetReply(m)
	q := strings.TrimSuffix(m.Question[0].Name, ".")
	serial, ok := f.zones[q]
	if host != f.at || !ok || m.RecursionDesired || m.Question[0].Qtype != dns.TypeSOA {
		r.Rcode = dns.RcodeRefused
		return r, nil
	}
	r.Authoritative = true
	r.Answer = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: dns.Fqdn(q), Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 300},
		Ns: "ns1." + dns.Fqdn(q), Mbox: "hostmaster." + dns.Fqdn(q), Serial: serial}}
	return r, nil
}

func (f *fakeZonePrimary) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.asked...)
}

// The zone inventory and the lookup over HTTP: a login (its session token) needed; every
// zone with its source and the fleet's state, never a node's; the zone primary, naming no
// node; a lookup that finds the fleet's zone, a zone on the primary (over DNS, from the
// fake), and none; a name that isn't one refused before any query.
func TestZoneInventoryAPI(t *testing.T) {
	dir := t.TempDir()
	if err := auth.SetPassword(auth.Path(dir), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: []string{"192.0.2.11", "192.0.2.12"},
		Primary: &primary.Config{Address: "192.0.2.254"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := zonefiles.Save(dir, "home.example.zone", []byte("$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. "+
		"( 5 3600 600 604800 300 )\n  IN NS ns1\nns1 IN A 192.0.2.53\n"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Save(dir, "a.json", []byte(`{"name": "a", "secondary": {"primary": "192.0.2.254", "zones": ["lab.example"]},
		"forward_zones": [{"zone": "corp.example", "forwarder": "198.51.100.53"}]}`), ""); err != nil {
		t.Fatal(err)
	}
	st := map[string]any{"zones": []any{map[string]any{"name": "lab.example", "expired": false}},
		"forward_zones": []any{map[string]any{"name": "corp.example", "forwarder": "198.51.100.53"}},
		"hosted":        map[string]any{"state": "on", "zones": []any{map[string]any{"name": "home.example", "serial": 5, "records": 3}}}}
	ns := []nodes.Node{{Addr: "192.0.2.11", Online: true, Status: st}, {Addr: "192.0.2.12", Online: true, Status: st},
		{Addr: "192.0.2.99", Online: true, Status: map[string]any{"zones": []any{map[string]any{"name": "stray.example"}}}}}
	fp := &fakeZonePrimary{at: "192.0.2.254", zones: map[string]uint32{"new.example": 42, "lab.example": 7}}
	s := &server{dataDir: dir, auth: auth.New(auth.Path(dir)), runner: jobs.New(dir, map[string]jobs.Kind{}),
		key: keys.FileSource{Path: keys.Path(dir)}, builder: newBuilder(t.TempDir(), t.TempDir(), t.TempDir()),
		nodes: func() []nodes.Node { return ns }, zoneDNS: fp}
	h, _ := s.handler()
	cookie, tok := login(t, h)
	get := func(path string, session bool) (int, string) {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8480"+path, nil)
		if session {
			r.AddCookie(&http.Cookie{Name: auth.CookieName(r.Host), Value: cookie})
			r.Header.Set(auth.TokenHeader, tok)
		}
		w := serve(h, r)
		return w.Code, w.Body.String()
	}

	// The session model: no cookie, or the cookie without its token, is refused.
	for _, p := range []string{"/api/zone-inventory", "/api/zone-lookup?name=new.example"} {
		if code, _ := get(p, false); code != http.StatusUnauthorized {
			t.Errorf("%s without a session: %d", p, code)
		}
		r := httptest.NewRequest("GET", "http://127.0.0.1:8480"+p, nil)
		r.AddCookie(&http.Cookie{Name: auth.CookieName(r.Host), Value: cookie})
		if w := serve(h, r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without the token: %d", p, w.Code)
		}
	}
	if len(fp.calls()) != 0 {
		t.Fatalf("a refused lookup asked the primary: %v", fp.calls())
	}

	code, body := get("/api/zone-inventory", true)
	if code != 200 {
		t.Fatalf("inventory: %d %s", code, body)
	}
	var inv struct {
		inventory.Inventory
		Primary map[string]any `json:"primary"`
	}
	if err := json.Unmarshal([]byte(body), &inv); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, z := range inv.Zones {
		got = append(got, z.Name+" "+z.Kind+" "+z.State)
	}
	// The node not in settings.json (192.0.2.99) isn't the fleet: its stray zone isn't listed.
	if strings.Join(got, ", ") != "corp.example forward ok, home.example hosted ok, lab.example secondary ok" {
		t.Errorf("zones: %v", got)
	}
	if inv.Fleet != 2 || inv.Nodes != 2 || strings.Join(inv.Primaries, ",") != "192.0.2.254" {
		t.Errorf("inventory: %+v", inv.Inventory)
	}
	if inv.Primary["address"] != "192.0.2.254" || inv.Primary["allows"] != nil || inv.Primary["set"] != true ||
		inv.Primary["uncovered"] != nil {
		t.Errorf("primary: %v", inv.Primary)
	}
	for _, leak := range []string{"192.0.2.11", "192.0.2.12"} {
		if strings.Contains(body, leak) {
			t.Errorf("the inventory holds %q: %s", leak, body)
		}
	}

	lookup := func(name string) inventory.Lookup {
		t.Helper()
		code, body := get("/api/zone-lookup?name="+name, true)
		var l inventory.Lookup
		if code != 200 || json.Unmarshal([]byte(body), &l) != nil {
			t.Fatalf("lookup %s: %d %s", name, code, body)
		}
		return l
	}
	if l := lookup("Lab.Example."); l.Found != inventory.FoundZone || l.Name != "lab.example" || l.Zone == nil ||
		l.Zone.Primary != "192.0.2.254" || len(l.Tried) != 0 {
		t.Errorf("lab: %+v", l)
	}
	if len(fp.calls()) != 0 {
		t.Errorf("a zone of the fleet's asked the primary: %v", fp.calls())
	}
	if l := lookup("new.example"); l.Found != inventory.FoundPrimary || l.Primary != "192.0.2.254" || l.Serial != 42 {
		t.Errorf("new: %+v", l)
	}
	// settings.json's primary and the configs' are the same server: asked once.
	if c := fp.calls(); len(c) != 1 || c[0] != "192.0.2.254" {
		t.Errorf("asked: %v", c)
	}
	if l := lookup("nowhere.example"); l.Found != inventory.FoundNone || len(l.Tried) != 1 || !strings.HasPrefix(l.Tried[0].Answer, "refused") {
		t.Errorf("nowhere: %+v", l)
	}
	before := len(fp.calls())
	for _, bad := range []string{"", "a..b", "-x.example", "a%20b", "x/y"} {
		if code, _ := get("/api/zone-lookup?name="+bad, true); code != http.StatusBadRequest {
			t.Errorf("lookup %q: %d", bad, code)
		}
	}
	if len(fp.calls()) != before {
		t.Errorf("a refused name asked the primary: %v", fp.calls())
	}
}

// The primaries a lookup asks: settings.json's first (its address), then the configs',
// each once; none set up, only the configs'.
func TestLookupPrimaries(t *testing.T) {
	inv := inventory.Inventory{Primaries: []string{"192.0.2.253", "192.0.2.254"}}
	s := settings.Settings{Primary: &primary.Config{Address: "192.0.2.254"}}
	if got := strings.Join(primaries(s, inv), ","); got != "192.0.2.254,192.0.2.253" {
		t.Errorf("got %s", got)
	}
	if got := strings.Join(primaries(settings.Settings{Primary: &primary.Config{Zones: []string{"home.example"}}}, inv), ","); got != "192.0.2.253,192.0.2.254" {
		t.Errorf("no address: %s", got)
	}
	if got := primaries(settings.Settings{}, inventory.Inventory{}); len(got) != 0 {
		t.Errorf("none: %v", got)
	}
}

// fakeTransfer is a primary's zone transfer: the zones it sends, who asked for what. No
// query leaves the test.
type fakeTransfer struct {
	mu    sync.Mutex
	at    string
	zones map[string][]dns.RR
	asked []string
}

func (f *fakeTransfer) Transfer(_ context.Context, p, zone string) ([]dns.RR, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, p+" "+zone)
	rrs, ok := f.zones[zone]
	if p != f.at || !ok {
		return nil, errors.New("dns: bad xfr rcode: 5")
	}
	return rrs, nil
}

// A copied zone's records (GET /api/zone-records): a login needed; read from the primary
// the configs name, by one transfer; a hosted or forward zone, or no zone, refused before any
// transfer; a refused transfer said in words.
func TestZoneRecordsAPI(t *testing.T) {
	dir := t.TempDir()
	if err := auth.SetPassword(auth.Path(dir), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: []string{"192.0.2.11"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Save(dir, "a.json", []byte(`{"name": "a", "secondary": {"primary": "192.0.2.254", "zones": ["lab.example", "gone.example"]},
		"forward_zones": [{"zone": "corp.example", "forwarder": "198.51.100.53"}]}`), ""); err != nil {
		t.Fatal(err)
	}
	rr := func(s string) dns.RR {
		r, err := dns.NewRR(s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	soa := rr("lab.example. 3600 IN SOA ns1.lab.example. hostmaster.lab.example. 7 3600 600 604800 300")
	ft := &fakeTransfer{at: "192.0.2.254", zones: map[string][]dns.RR{"lab.example": {soa,
		rr("lab.example. 3600 IN NS ns1.lab.example."), rr("NAS.lab.example. 300 IN A 192.0.2.10"), rr(`lab.example. 300 IN TXT "hello there"`)}}}
	ns := []nodes.Node{{Addr: "192.0.2.11", Online: true, Status: map[string]any{"zones": []any{map[string]any{"name": "lab.example", "serial": 7}}}}}
	v := inventoryServer{dataDir: dir, nodes: func() []nodes.Node { return ns }, xfr: ft}
	get := func(name string) (int, string) {
		w := httptest.NewRecorder()
		v.records(w, httptest.NewRequest("GET", "/api/zone-records?name="+name, nil))
		return w.Code, w.Body.String()
	}

	code, body := get("Lab.Example.")
	if code != 200 {
		t.Fatalf("lab: %d %s", code, body)
	}
	var got struct {
		Name    string       `json:"name"`
		Primary string       `json:"primary"`
		Serial  uint32       `json:"serial"`
		Records []zoneRecord `json:"records"`
		More    int          `json:"more"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "lab.example" || got.Primary != "192.0.2.254" || got.Serial != 7 || len(got.Records) != 4 || got.More != 0 {
		t.Fatalf("lab: %+v", got)
	}
	if r := got.Records[2]; r.Owner != "nas.lab.example." || r.Type != "A" || r.TTL != 300 || r.Data != "192.0.2.10" {
		t.Errorf("A: %+v", r)
	}
	if r := got.Records[3]; r.Type != "TXT" || r.Data != `"hello there"` {
		t.Errorf("TXT: %+v", r)
	}

	// Refused before any transfer: a forward zone, no zone, not a name.
	before := len(ft.asked)
	for name, want := range map[string]int{"corp.example": 404, "nowhere.example": 404, "a..b": 400} {
		if code, body := get(name); code != want {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if len(ft.asked) != before {
		t.Errorf("a refused read transferred: %v", ft.asked)
	}
	// A zone the primary won't send: said, with where it was asked.
	code, body = get("gone.example")
	if code != http.StatusBadGateway || !strings.Contains(body, "192.0.2.254") || !strings.Contains(body, `"kind":"refused"`) || !strings.Contains(body, "REFUSED") || !strings.Contains(body, `"error":"your primary DNS server`) {
		t.Errorf("gone: %d %s", code, body)
	}

	// Over HTTP, the route needs a login.
	s := &server{dataDir: dir, auth: auth.New(auth.Path(dir)), runner: jobs.New(dir, map[string]jobs.Kind{}),
		key: keys.FileSource{Path: keys.Path(dir)}, builder: newBuilder(t.TempDir(), t.TempDir(), t.TempDir()),
		nodes: func() []nodes.Node { return ns }}
	h, _ := s.handler()
	if w := serve(h, httptest.NewRequest("GET", "http://127.0.0.1:8480/api/zone-records?name=lab.example", nil)); w.Code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", w.Code)
	}
}

// A copied zone's records read from a node that holds it: the highest serial among the nodes
// online, answering and holding an unexpired copy; the next one when its read fails; the
// primary when none hands it over, or no node holds it. The reply names where they came from.
func TestZoneRecordsFromNode(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3", "203.0.113.4", "203.0.113.5"}
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: hosts}); err != nil {
		t.Fatal(err)
	}
	if _, err := configs.Save(dir, "a.json", []byte(`{"name": "a", "secondary": {"primary": "192.0.2.254", "zones": ["lab.example"]}}`), ""); err != nil {
		t.Fatal(err)
	}
	zone := func(serial uint32, a string) []dns.RR {
		var rrs []dns.RR
		for _, s := range []string{fmt.Sprintf("lab.example. 3600 IN SOA ns1.lab.example. hostmaster.lab.example. %d 3600 600 604800 300", serial),
			"lab.example. 3600 IN NS ns1.lab.example.", "nas.lab.example. 300 IN A " + a} {
			rr, err := dns.NewRR(s)
			if err != nil {
				t.Fatal(err)
			}
			rrs = append(rrs, rr)
		}
		return rrs
	}
	lan := fakenode.NewLan()
	defer lan.Close()
	fake := map[string]*fakenode.Node{}
	for i, h := range hosts {
		n := fakenode.New(fmt.Sprintf("dns-%d", i+1), [6]byte{2, 0, 0, 0, 0, byte(i + 1)}, "esp32p4-rev1", "p4-ip101", nil)
		n.Addr = h
		n.SetCopy("lab.example", zone(uint32(7+i), fmt.Sprintf("192.0.2.%d", i+1)))
		lan.Add(n)
		fake[h] = n
	}
	// As the controller last read each: .1 serial 7; .2 serial 8; .3 serial 9, not
	// answering; .4 serial 10, expired; .5 serial 11, offline.
	st := func(name string, serial uint32, answering, expired bool) map[string]any {
		return map[string]any{"reads": []any{"zone"}, "config": map[string]any{"name": name},
			"health": map[string]any{"state": "healthy", "answering": answering},
			"zones":  []any{map[string]any{"name": "lab.example", "serial": serial, "expired": expired}}}
	}
	ns := []nodes.Node{
		{Addr: hosts[0], Online: true, Status: st("dns-a", 7, true, false)},
		{Addr: hosts[1], Online: true, Status: st("dns-b", 8, true, false)},
		{Addr: hosts[2], Online: true, Status: st("dns-c", 9, false, false)},
		{Addr: hosts[3], Online: true, Status: st("dns-d", 10, true, true)},
		{Addr: hosts[4], Online: false, Status: st("dns-e", 11, true, false)},
	}
	ft := &fakeTransfer{at: "192.0.2.254", zones: map[string][]dns.RR{"lab.example": zone(6, "192.0.2.254")}}
	v := inventoryServer{dataDir: dir, nodes: func() []nodes.Node { return ns }, xfr: ft, client: &fleet.Client{HTTP: lan.HTTP()}}
	get := func() (node, a string, serial uint32) {
		t.Helper()
		w := httptest.NewRecorder()
		v.records(w, httptest.NewRequest("GET", "/api/zone-records?name=lab.example", nil))
		var got struct {
			Node    string       `json:"node"`
			Serial  uint32       `json:"serial"`
			Records []zoneRecord `json:"records"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || len(got.Records) != 3 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		return got.Node, got.Records[2].Data, got.Serial
	}

	if node, a, serial := get(); node != "dns-b" || a != "192.0.2.2" || serial != 8 || len(ft.asked) != 0 {
		t.Errorf("best node: %s %s %d, primary asked %v", node, a, serial, ft.asked)
	}
	// dns-b no longer holds it (404): the next one, dns-a.
	fake[hosts[1]].Do(func(n *fakenode.Node) { delete(n.Copies, "lab.example") })
	if node, a, serial := get(); node != "dns-a" || a != "192.0.2.1" || serial != 7 || len(ft.asked) != 0 {
		t.Errorf("next node: %s %s %d, primary asked %v", node, a, serial, ft.asked)
	}
	// No node hands it over: the primary.
	fake[hosts[0]].Do(func(n *fakenode.Node) { delete(n.Copies, "lab.example") })
	if node, a, serial := get(); node != "" || a != "192.0.2.254" || serial != 6 || len(ft.asked) != 1 {
		t.Errorf("primary: %q %s %d, primary asked %v", node, a, serial, ft.asked)
	}
	// Firmware without /zone ("reads" missing): not asked; the primary.
	for i := range ns {
		delete(ns[i].Status, "reads")
	}
	if node, _, _ := get(); node != "" || len(ft.asked) != 2 {
		t.Errorf("no reads: %q, primary asked %v", node, ft.asked)
	}
}

// A primary that takes the transfer and sends nothing: the read ends when its context does,
// not after the per-message timeouts.
func TestAXFRContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held open, silent, until the test ends
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = axfr{port: port}.Transfer(ctx, "127.0.0.1", "lab.example")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}

// A transfer's failure, sorted for the page: the rcode the primary answered with, a
// timeout, no connection; the technical detail kept apart.
func TestXFRFailure(t *testing.T) {
	timeout := &net.OpError{Op: "read", Net: "tcp", Err: timeoutErr{}}
	for _, c := range []struct {
		err          error
		kind, detail string
		source       string
	}{
		{&xfrError{source: "192.0.2.5", err: errors.New("dns: bad xfr rcode: 5")}, xfrRefused, "rcode 5 (REFUSED)", "192.0.2.5"},
		{errors.New("dns: bad xfr rcode: 9"), xfrRefused, "rcode 9 (NOTAUTH)", ""},
		{errors.New("dns: bad xfr rcode: 3"), xfrUnknown, "rcode 3 (NXDOMAIN)", ""},
		{errors.New("dns: bad xfr rcode: 10"), xfrUnknown, "rcode 10 (NOTZONE)", ""},
		{errors.New("dns: bad xfr rcode: 2"), xfrFailed, "rcode 2 (SERVFAIL)", ""},
		{&xfrError{source: "192.0.2.5", err: dns.ErrSoa}, xfrUnknown, dns.ErrSoa.Error(), "192.0.2.5"},
		{fmt.Errorf("x: %w", context.DeadlineExceeded), xfrTimeout, "no answer in time", ""},
		{&xfrError{source: "192.0.2.5", err: timeout}, xfrTimeout, "no answer in time", "192.0.2.5"},
		{&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}, xfrUnreachable, "dial tcp: connect: connection refused", ""},
		{errors.New("something else"), xfrFailed, "something else", ""},
	} {
		f := xfrFailure(c.err)
		if f.Kind != c.kind || f.Detail != c.detail || f.Source != c.source {
			t.Errorf("%v: %+v", c.err, f)
		}
		if s := f.say("192.0.2.254"); !strings.Contains(s, "(192.0.2.254)") || strings.Contains(s, "rcode") {
			t.Errorf("%v: said %q", c.err, s)
		}
	}
}

// loopback is the real transfer, to a primary on loopback whatever the config names (a
// config can't name 127.0.0.1).
type loopback struct{ port string }

func (l loopback) Transfer(ctx context.Context, _, zone string) ([]dns.RR, error) {
	return axfr{port: l.port}.Transfer(ctx, "127.0.0.1", zone)
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// A primary on loopback that refuses the transfer: the route says so, with the address the
// controller connected from (127.0.0.1 here), which the page offers to copy.
func TestZoneRecordsRefusedSource(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{Listener: ln, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeRefused)
		w.WriteMsg(m)
	})}
	go srv.ActivateAndServe()
	defer srv.Shutdown()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	dir := t.TempDir()
	if _, err := configs.Save(dir, "a.json", []byte(`{"name": "a", "secondary": {"primary": "192.0.2.254", "zones": ["lab.example"]}}`), ""); err != nil {
		t.Fatal(err)
	}
	v := inventoryServer{dataDir: dir, nodes: func() []nodes.Node { return nil }, xfr: loopback{port}}
	w := httptest.NewRecorder()
	v.records(w, httptest.NewRequest("GET", "/api/zone-records?name=lab.example", nil))
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w.Code != http.StatusBadGateway || got["kind"] != xfrRefused || got["source"] != "127.0.0.1" || got["primary"] != "192.0.2.254" ||
		got["detail"] != "rcode 5 (REFUSED)" || !strings.HasPrefix(got["error"], "your primary DNS server (192.0.2.254) refused") {
		t.Errorf("%d %v", w.Code, got)
	}

	// Nothing listening: unreachable, no source.
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ := net.SplitHostPort(ln2.Addr().String())
	ln2.Close()
	_, err = axfr{port: closed}.Transfer(context.Background(), "127.0.0.1", "lab.example")
	if f := xfrFailure(err); f.Kind != xfrUnreachable || f.Source != "" {
		t.Errorf("closed port: %+v (%v)", f, err)
	}
}
