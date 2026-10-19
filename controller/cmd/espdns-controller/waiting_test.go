package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/pins"
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
	s.Primary = &primary.Config{Address: "203.0.113.254", Zones: []string{"home.example"}}
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
	// An address one of your nodes has, or another board waiting: refused.
	if code, body := e.postWaiting("/api/nodes/waiting", with("address", "203.0.113.51/24")); code != 409 || !strings.Contains(body, "one of your nodes' addresses") {
		t.Fatal(code, body)
	}
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": "dns7", "id": "02:00:00:00:00:cc", "address": "203.0.113.80/24",
		"gateway": "203.0.113.1"}); code != 409 || !strings.Contains(body, "another board's, waiting") {
		t.Fatal(code, body)
	}
	// DHCP: no gateway.
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": "dns4", "id": "02:00:00:00:00:bb", "address": "dhcp"}); code != 200 {
		t.Fatal(code, body)
	}
	failWaiting(e.dir, "02:00:00:00:00:aa", "it broke")
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

// The watcher: a board on a fixed address is added once a node answers as it, by the adopt
// jobs, as the user who installed it, into settings.json, and leaves the list. With its node
// key it is matched wherever it is found, only with that key; without one, only at its own
// address (an mDNS answer elsewhere is anyone's). Another release key fails it; a failed
// adoption stays.
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
	add("bad-key", e.typed.ID(), "203.0.113.71/24", e.node.NodeKey()) // its ID, another node key
	add("no-key-elsewhere", e.node.ID(), "203.0.113.61/24", "")       // found over mDNS at .52, no key: not it
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
		m["bad-key"].State != waitingWait || m["no-key-elsewhere"].State != waitingWait {
		t.Fatalf("%+v", m)
	}
	for _, n := range []*fakenode.Node{e.typed, other, e.node} {
		if slices.Contains(n.Events(), "push config") {
			t.Fatal(n.ID(), "was changed")
		}
	}
	// A failed adoption stays, with its error: the address is the node in service's.
	for _, id := range []string{e.typed.ID(), e.node.ID()} {
		e.postWaiting("/api/nodes/waiting/"+id+"/delete", nil)
	}
	changeWaiting(e.dir, func(ws []waitingNode) ([]waitingNode, error) {
		return append(ws, waitingNode{ID: e.node.ID(), Name: "n2", Address: "203.0.113.51/24", Gateway: "203.0.113.1", NodeKey: e.node.NodeKey(),
			State: waitingWait}), nil
	})
	e.waiter().once(context.Background())
	if w := byName()["n2"]; w.State != waitingFailed || w.Error == "" {
		t.Fatalf("%+v", w)
	}
	// The board installed, with its node key, found where mDNS has it: added, as its user, in
	// settings.json, and no longer waiting.
	e.postWaiting("/api/nodes/waiting/"+e.node.ID()+"/delete", nil)
	add("dns2b", e.node.ID(), "203.0.113.60/24", e.node.NodeKey())
	setWaiting(e.dir, e.node.ID(), func(x *waitingNode) { x.User = "alice" })
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
	for _, j := range e.runner.List() {
		if j.Kind == "adopt" && j.State == jobs.Done && j.Who != "alice" {
			t.Fatalf("%+v", j)
		}
	}
}

// Without a node key, a board on a fixed address is added once it answers there, as itself.
func TestWaitingNoKeyAtItsAddress(t *testing.T) {
	e := newAddNodeEnv(t)
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": "typed", "id": e.typed.ID(), "address": "203.0.113.70/24",
		"gateway": "203.0.113.1"}); code != 200 {
		t.Fatal(code, body)
	}
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !slices.Contains(s.Nodes, "203.0.113.70") {
		t.Fatal(s.Nodes)
	}
}

// On DHCP a board is only said found (its address, its interface's MAC); Add it adopts it,
// reserved. Without a node key, as mDNS names it; never automatically.
func TestWaitingDHCP(t *testing.T) {
	e := newAddNodeEnv(t)
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"name": "dns2c", "id": e.node.ID(), "address": "dhcp", "node_key": e.node.NodeKey()}); code != 200 {
		t.Fatal(code, body)
	}
	w := e.waiter()
	if err := w.addFound(e.node.ID(), "admin"); err == nil || !strings.Contains(err.Error(), "isn't found") {
		t.Fatal(err)
	}
	w.once(context.Background())
	ws := e.waitingList()
	if len(ws) != 1 || ws[0].State != waitingFound || ws[0].Found != "203.0.113.52" || ws[0].MAC == "" || slices.Contains(e.node.Events(), "push config") {
		t.Fatalf("%+v", ws)
	}
	if err := w.addFound(e.node.ID(), "admin"); err != nil {
		t.Fatal(err)
	}
	for range 2000 {
		if len(e.waitingList()) == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !slices.Contains(s.Nodes, "203.0.113.52") {
		t.Fatal(s.Nodes)
	}
}

// One in settings.json or adopted meanwhile leaves the list, even one being added when the
// controller stopped: not adopted again, no failed row left.
func TestWaitingAddedMeanwhile(t *testing.T) {
	e := newAddNodeEnv(t)
	changeWaiting(e.dir, func(ws []waitingNode) ([]waitingNode, error) {
		return append(ws, waitingNode{ID: e.peer.ID(), Name: "dns1", Address: "203.0.113.51/24", Gateway: "203.0.113.1", State: waitingAdding},
			waitingNode{ID: "02:00:00:00:00:44", Name: "pinned", Address: "203.0.113.44/24", Gateway: "203.0.113.1", State: waitingFailed, Error: "x"}), nil
	})
	if _, err := pins.Open(e.dir).Pin("02:00:00:00:00:44", "203.0.113.44"); err != nil {
		t.Fatal(err)
	}
	w := e.waiter()
	w.restart()
	if ws := e.waitingList(); ws[0].State != waitingWait {
		t.Fatalf("%+v", ws)
	}
	w.once(context.Background())
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if slices.Contains(e.peer.Events(), "push config") {
		t.Fatal("adopted again")
	}
}

// The adoption checks the node key expected on the node as it reads it: another is refused,
// before anything is pinned or pushed.
func TestAdoptNodeKeyExpected(t *testing.T) {
	e := newAdoptEnv(t)
	j := e.run("adopt", e.web(map[string]any{"address": "203.0.113.60/24", "dry_run": true, "node_key": e.peer.NodeKey()}))
	if j.State != jobs.Failed || !strings.Contains(j.Error, "another node key") {
		t.Fatal(j.State, j.Error)
	}
	if j := e.run("adopt", e.web(map[string]any{"address": "203.0.113.60/24", "dry_run": true, "node_key": e.node.NodeKey()})); j.State != jobs.Done {
		t.Fatal(j.State, j.Error)
	}
}

// reinstalled pins dns1 (the node in service at .51) with its node key, its config (n1.json,
// with a Wi-Fi password and network, if any) recorded as pushed to it, then installs it
// again over USB: on its board's settings with no name, a new node key if erase. It returns
// the node key pinned before.
func (e *addNodeEnv) reinstalled(erase bool, network string) string {
	e.t.Helper()
	l := pins.Open(e.dir)
	if _, err := l.Pin(e.peer.ID(), "203.0.113.51"); err != nil {
		e.t.Fatal(err)
	}
	old := e.peer.NodeKey()
	if err := l.SetNodeKey(e.peer.ID(), old); err != nil {
		e.t.Fatal(err)
	}
	if network != "" {
		network = `, "network": ` + network
	}
	writeFile(e.t, filepath.Join(configs.Path(e.dir), "n1.json"),
		[]byte(`{"name": "dns1", "secondary": {"zones": ["home.example"]}, "wifi": {"ssid": "home", "password": "wifi-secret-pass-123"}`+network+`}`))
	if err := configs.RecordPushed(e.dir, configs.Pushed{NodeID: e.peer.ID(), Host: "203.0.113.51", File: "n1.json", Seq: 1, By: "controller"}); err != nil {
		e.t.Fatal(err)
	}
	e.peer.Reinstall(erase)
	return old
}

// postReinstall posts dns1 installed again with key, its image's address at.
func (e *addNodeEnv) postReinstall(key, at string) {
	e.t.Helper()
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"reinstall": true, "id": e.peer.ID(), "node_key": key, "name": "dns1",
		"address": at, "gateway": "203.0.113.1", "uptime_s": 5}); code != 200 {
		e.t.Fatal(code, body)
	}
}

func (e *addNodeEnv) source(n *fakenode.Node) string {
	e.t.Helper()
	st, err := (&fleet.Client{HTTP: e.lan.HTTP()}).Status(context.Background(), n.Addr)
	if err != nil || st.Config == nil {
		e.t.Fatal(err)
	}
	return st.Config.Source
}

func (e *addNodeEnv) pinnedKey() string {
	k, _ := pins.Open(e.dir).NodeKey(e.peer.ID())
	return k
}

func (e *addNodeEnv) restores() int {
	n := 0
	for _, j := range e.runner.List() {
		if j.Kind == "node-restore" {
			n++
		}
	}
	return n
}

// One of your nodes installed again over USB at its address, its NVS erased (a new node
// key), back on its board's settings with no name, its config with no network: another node
// key there is not it, and nothing is sent. With the key read over USB: its config (found as
// the one last pushed to it) sent with its Wi-Fi password sealed to the new key (the node
// opens it), that key pinned, and no longer waiting. A board that isn't one of your nodes is
// refused as a reinstall; a node on its board's settings with no entry is sent nothing.
func TestWaitingReinstall(t *testing.T) {
	e := newAddNodeEnv(t)
	e.reinstalled(true, "")
	e.waiter().once(context.Background())
	if slices.Contains(e.peer.Events(), "push config") {
		t.Fatal("a node on its board's settings with no entry was sent its config")
	}
	if code, body := e.postWaiting("/api/nodes/waiting", map[string]any{"reinstall": true, "id": e.node.ID(), "address": "203.0.113.52/24",
		"gateway": "203.0.113.1"}); code != 409 || !strings.Contains(body, "not one of your nodes") {
		t.Fatal(code, body)
	}
	e.postReinstall(e.node.NodeKey(), "203.0.113.51/24")
	if ws := e.waitingList(); len(ws) != 1 || !ws[0].Reinstall || ws[0].Address != "203.0.113.51/24" || ws[0].State != waitingWait || ws[0].Boot.IsZero() {
		t.Fatalf("%+v", ws)
	}
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 1 || ws[0].State != waitingWait || slices.Contains(e.peer.Events(), "push config") {
		t.Fatalf("another node key: %+v %v", ws, e.peer.Events())
	}
	e.postReinstall(e.peer.NodeKey(), "203.0.113.51/24")
	setWaiting(e.dir, e.peer.ID(), func(x *waitingNode) { x.User = "alice" })
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if e.pinnedKey() != e.peer.NodeKey() {
		t.Fatal("the node key read over USB isn't pinned")
	}
	if !slices.Contains(e.peer.Events(), "push config") || e.source(e.peer) != "node" {
		t.Fatalf("%v", e.peer.Events())
	}
	for _, j := range e.runner.List() {
		if j.Kind == "node-restore" && (j.State != jobs.Done || j.Who != "alice") {
			t.Fatalf("%+v", j)
		}
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !slices.Equal(s.Nodes, []string{"203.0.113.51"}) || len(s.OutOfService) != 0 {
		t.Fatalf("%+v", s)
	}
}

// Installed at a new address (in its image), with a new node key. Without a key it is
// matched only at the image's address: elsewhere it isn't it. With it: its entry moved there
// (its old address silent), its config made to keep the new address (pushed as it was, it
// would move the node back), the new key pinned, its settings sent.
func TestWaitingReinstallElsewhere(t *testing.T) {
	e := newAddNodeEnv(t)
	e.reinstalled(true, `{"address": "203.0.113.51/24", "gateway": "203.0.113.1"}`)
	e.silent()
	e.seen = append(e.seen, "203.0.113.200")
	e.postReinstall("", "203.0.113.51/24")
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 1 || ws[0].State != waitingWait || slices.Contains(e.peer.Events(), "push config") {
		t.Fatalf("no key, elsewhere: %+v", ws)
	}
	e.postReinstall(e.peer.NodeKey(), "203.0.113.200/24")
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !slices.Equal(s.Nodes, []string{"203.0.113.200"}) {
		t.Fatal(s.Nodes)
	}
	if f, err := configs.Read(e.dir, "n1.json"); err != nil || f.Config.Network.Address != "203.0.113.200/24" {
		t.Fatalf("%v %s", err, f.Text)
	}
	if e.source(e.peer) != "node" || e.pinnedKey() != e.peer.NodeKey() {
		t.Fatal(e.peer.Events())
	}
}

// The move refused (its old address still answers): failed, and the node key pinned is
// still the old one.
func TestWaitingReinstallMoveRefused(t *testing.T) {
	e := newAddNodeEnv(t)
	old := e.reinstalled(true, "")
	e.silent()
	e.seen = append(e.seen, "203.0.113.200")
	all := e.adopt.nodes
	answers := func() []nodes.Node {
		ns := all()
		for i := range ns {
			if ns[i].Addr == "203.0.113.51" {
				ns[i].Online = true
			}
		}
		return ns
	}
	e.adopt.nodes, e.add.adopt.nodes = answers, answers
	e.postReinstall(e.peer.NodeKey(), "203.0.113.200/24")
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 1 || ws[0].State != waitingFailed || !strings.Contains(ws[0].Error, "still answers") {
		t.Fatalf("%+v", ws)
	}
	if e.pinnedKey() != old || e.restores() != 0 {
		t.Fatal("re-pinned, or restored, though not moved")
	}
}

// Still on its board's settings after its catch-up (no config of its here): failed, with
// why, its old node key pinned again, and not sent again by the next check.
func TestWaitingReinstallStillDefaults(t *testing.T) {
	e := newAddNodeEnv(t)
	old := e.reinstalled(true, "")
	if err := os.Remove(filepath.Join(configs.Path(e.dir), "n1.json")); err != nil {
		t.Fatal(err)
	}
	e.postReinstall(e.peer.NodeKey(), "203.0.113.51/24")
	w := e.waiter()
	w.once(context.Background())
	ws := e.waitingList()
	if len(ws) != 1 || ws[0].State != waitingFailed || !strings.Contains(ws[0].Error, "board's settings") || !strings.Contains(ws[0].Error, "no config file") {
		t.Fatalf("%+v", ws)
	}
	if e.pinnedKey() != old {
		t.Fatal("the old node key isn't put back")
	}
	w.once(context.Background())
	if n := e.restores(); n != 1 {
		t.Fatalf("%d node-restore jobs", n)
	}
}

// A new node key from a node that hasn't restarted since the install isn't taken: nothing
// pinned or sent.
func TestWaitingReinstallNotRestarted(t *testing.T) {
	e := newAddNodeEnv(t)
	old := e.reinstalled(true, "")
	e.postReinstall(e.peer.NodeKey(), "203.0.113.51/24")
	// The panel restarted the board a minute after it last booted: not this boot.
	setWaiting(e.dir, e.peer.ID(), func(x *waitingNode) { x.Boot = time.Now().Add(time.Minute) })
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 1 || ws[0].State != waitingWait || e.pinnedKey() != old || slices.Contains(e.peer.Events(), "push config") {
		t.Fatalf("%+v", ws)
	}
}

// One out of service gets its settings back and stays out of service.
func TestWaitingReinstallOutOfService(t *testing.T) {
	e := newAddNodeEnv(t)
	e.reinstalled(false, "")
	s, _ := settings.Load(settings.Path(e.dir))
	s.OutOfService = []string{"203.0.113.51"}
	if err := settings.Save(settings.Path(e.dir), s); err != nil {
		t.Fatal(err)
	}
	e.postReinstall(e.peer.NodeKey(), "203.0.113.51/24")
	e.waiter().once(context.Background())
	if ws := e.waitingList(); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !s.IsOutOfService("203.0.113.51") || e.source(e.peer) != "node" {
		t.Fatalf("%+v %v", s, e.peer.Events())
	}
}

// Not seen within an hour of the install: failed, to install again; Retry waits an hour more.
func TestWaitingExpires(t *testing.T) {
	e := newAddNodeEnv(t)
	changeWaiting(e.dir, func(ws []waitingNode) ([]waitingNode, error) {
		return append(ws, waitingNode{ID: "02:00:00:00:00:99", Name: "gone", Address: "203.0.113.99/24", Gateway: "203.0.113.1",
			State: waitingWait, Since: time.Now().Add(-61 * time.Minute)}), nil
	})
	e.waiter().once(context.Background())
	if ws := e.waitingList(); ws[0].State != waitingFailed || ws[0].Error != "Not seen within an hour of the install: install it again." {
		t.Fatalf("%+v", ws)
	}
	e.postWaiting("/api/nodes/waiting/02:00:00:00:00:99/retry", nil)
	e.waiter().once(context.Background())
	if ws := e.waitingList(); ws[0].State != waitingWait || time.Since(ws[0].Since) > time.Minute {
		t.Fatalf("%+v", ws)
	}
}
