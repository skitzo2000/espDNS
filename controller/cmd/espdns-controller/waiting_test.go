package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

func (e *addNodeEnv) postWaiting(path string, v any) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(v)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
	mux := &routes{mux: http.NewServeMux()}
	mux.HandleFunc("POST /api/nodes/waiting", e.add.addWaiting)
	mux.HandleFunc("POST /api/nodes/waiting/{id}/retry", e.add.retryWaiting)
	mux.HandleFunc("POST /api/nodes/waiting/{id}/delete", e.add.deleteWaiting)
	mux.mux.ServeHTTP(w, req)
	return w.Code, w.Body.String()
}

func (e *addNodeEnv) waitingList() []waitingNode {
	e.t.Helper()
	ws, err := loadWaiting(e.dir)
	if err != nil {
		e.t.Fatal(err)
	}
	return ws
}

func (e *addNodeEnv) waiter() waiter {
	return waiter{adopt: *e.adopt, runner: e.runner, lookup: e.reg.Lookup}
}

// POST /api/nodes/waiting: the name, the MAC, the address checked; a listed node refused;
// a new name's config written with its network and the zone primary's zones; a name whose
// config says another network refused; Retry and Remove.
func TestWaitingAPI(t *testing.T) {
	e := newAddNodeEnv(t)
	s, _ := settings.Load(settings.Path(e.dir))
	s.Primary = &primary.Config{Address: "203.0.113.254", Allows: []string{"203.0.113.0/24"}, Zones: []string{"home.example"}}
	if err := settings.Save(settings.Path(e.dir), s); err != nil {
		t.Fatal(err)
	}
	ok := map[string]any{"name": "dns3", "board": "p4-ip101", "id": "02-00-00-00-00-AA", "address": "203.0.113.80/24", "gateway": "203.0.113.1"}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for x, y := range ok {
			m[x] = y
		}
		m[k] = v
		return m
	}
	for _, c := range []struct {
		req  map[string]any
		code int
		want string
	}{
		{with("name", "Bad_Name"), 400, "name:"},
		{with("id", "02:00:00:00:00:zz"), 400, "not a chip MAC"},
		{with("gateway", "198.51.100.1"), 400, "gateway"},
		{with("address", "203.0.113.80"), 400, "network.address"},
		{with("node_key", "nope"), 400, "node_key"},
		{with("id", e.peer.ID()), 409, "one of your nodes already"},
		{with("name", "n2"), 409, "That name is in use"},
	} {
		if code, body := e.postWaiting("/api/nodes/waiting", c.req); code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%v: %d %s", c.req, code, body)
		}
	}
	if len(e.waitingList()) != 0 {
		t.Fatal("a refused one was kept")
	}
	if code, body := e.postWaiting("/api/nodes/waiting", ok); code != 200 {
		t.Fatal(code, body)
	}
	ws := e.waitingList()
	if len(ws) != 1 || ws[0].ID != "02:00:00:00:00:aa" || ws[0].State != waitingWait || ws[0].Since.IsZero() {
		t.Fatalf("%+v", ws)
	}
	f, err := configs.Read(e.dir, "dns3.json")
	if err != nil || f.Config.Name != "dns3" || f.Config.Network.Address != "203.0.113.80/24" || f.Config.Network.Gateway != "203.0.113.1" ||
		f.Config.Secondary == nil || f.Config.Secondary.Primary != "203.0.113.254" || !slices.Equal(*f.Config.Secondary.Zones, []string{"home.example"}) {
		t.Fatalf("%v %s", err, f.Text)
	}
	// The same board again (installed again): one entry, the config as it was. Another board
	// by that name: refused.
	if code, body := e.postWaiting("/api/nodes/waiting", ok); code != 200 || len(e.waitingList()) != 1 {
		t.Fatal(code, body)
	}
	if code, body := e.postWaiting("/api/nodes/waiting", with("id", "02:00:00:00:00:ab")); code != 409 || !strings.Contains(body, "That name is in use") {
		t.Fatal(code, body)
	}
	// DHCP: no gateway.
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": "dns4", "id": "02:00:00:00:00:bb", "address": "dhcp"}); code != 200 {
		t.Fatal(code, body)
	}
	setWaiting(e.dir, "02:00:00:00:00:aa", waitingFailed, "it broke")
	if code, body := e.postWaiting("/api/nodes/waiting/02:00:00:00:00:aa/retry", nil); code != 200 {
		t.Fatal(code, body)
	}
	if ws := e.waitingList(); ws[0].State != waitingWait || ws[0].Error != "" {
		t.Fatalf("%+v", ws[0])
	}
	if code, _ := e.postWaiting("/api/nodes/waiting/02:00:00:00:00:aa/delete", nil); code != 200 {
		t.Fatal(code)
	}
	if code, _ := e.postWaiting("/api/nodes/waiting/02:00:00:00:00:aa/delete", nil); code != 404 {
		t.Fatal(code)
	}
	if ws := e.waitingList(); len(ws) != 1 || ws[0].Name != "dns4" {
		t.Fatalf("%+v", ws)
	}
	if _, err := configs.Read(e.dir, "dns3.json"); err != nil {
		t.Fatal("the config went with it:", err)
	}
}

// The watcher: a board waiting is added once a node answers as it (its ID, your release key,
// its node key), by the adopt jobs, into settings.json, and leaves the list. Another ID at
// the address is not it; another release key or node key is refused, and stays failed.
func TestWaitingAdds(t *testing.T) {
	e := newAddNodeEnv(t)
	other := fakenode.New("other", [6]byte{2, 0, 0, 0, 0, 0x73}, "esp32p4-rev1", "p4-ip101", release.PublicRaw(newTestKey(t)))
	other.Addr, other.Gateway, other.Unadopted = "203.0.113.73", "203.0.113.1", true
	e.lan.Add(other)
	add := func(name, id, address, key string) {
		t.Helper()
		if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": name, "id": id, "address": address,
			"gateway": "203.0.113.1", "node_key": key}); code != 200 {
			t.Fatal(code, body)
		}
	}
	add("wrong-id", "02:00:00:00:00:99", "203.0.113.70/24", "")       // e.typed is there, as another ID
	add("other-key", other.ID(), "203.0.113.73/24", "")               // trusts another release key
	add("bad-key", e.typed.ID(), "203.0.113.70/24", e.node.NodeKey()) // another node key
	e.waiter().once(context.Background())
	byName := func() map[string]waitingNode {
		m := map[string]waitingNode{}
		for _, w := range e.waitingList() {
			m[w.Name] = w
		}
		return m
	}
	m := byName()
	if m["wrong-id"].State != waitingWait || m["other-key"].State != waitingFailed || !strings.Contains(m["other-key"].Error, "release key") ||
		m["bad-key"].State != waitingFailed || !strings.Contains(m["bad-key"].Error, "node key") {
		t.Fatalf("%+v", m)
	}
	for _, n := range []*fakenode.Node{e.typed, other} {
		if slices.Contains(n.Events(), "push config") {
			t.Fatal(n.ID(), "was changed")
		}
	}
	// A failed adoption stays, with its error: the address is the node in service's.
	e.postWaiting("/api/nodes/waiting/"+e.typed.ID()+"/delete", nil)
	add("taken", e.node.ID(), "203.0.113.51/24", e.node.NodeKey())
	e.waiter().once(context.Background())
	if w := byName()["taken"]; w.State != waitingFailed || w.Error == "" {
		t.Fatalf("%+v", w)
	}
	// The board installed: added, in settings.json, and no longer waiting.
	e.postWaiting("/api/nodes/waiting/"+e.node.ID()+"/delete", nil)
	add("dns2b", e.node.ID(), "203.0.113.60/24", e.node.NodeKey())
	e.waiter().once(context.Background())
	if w, ok := byName()["dns2b"]; ok {
		t.Fatalf("still waiting: %+v", w)
	}
	s, _ := settings.Load(settings.Path(e.dir))
	if !slices.Contains(s.Nodes, "203.0.113.60") {
		t.Fatal(s.Nodes)
	}
	if pr, _ := configs.LoadPushed(e.dir, e.node.ID()); pr == nil || pr.File != "dns2b.json" {
		t.Fatalf("%+v", pr)
	}
}
