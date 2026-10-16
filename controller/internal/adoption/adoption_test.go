package adoption

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// env is a fake network: an adopted node in settings.json (203.0.113.51), a node to adopt
// (203.0.113.52, unadopted, on its board's address), a web server on 203.0.113.99, a zone
// primary in settings.json that allows zone transfers from 203.0.113.0/24, and a data
// directory.
type env struct {
	t    *testing.T
	dir  string
	lan  *fakenode.Lan
	peer *fakenode.Node
	node *fakenode.Node
	key  *ecdsa.PrivateKey
	logs []string
	mu   sync.Mutex
}

func newEnv(t *testing.T) *env {
	e := &env{t: t, dir: t.TempDir(), lan: fakenode.NewLan()}
	t.Cleanup(e.lan.Close)
	e.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	mk := func(name string, last byte, unadopted bool) *fakenode.Node {
		n := fakenode.New(name, [6]byte{2, 0, 0, 0, 0, last}, "esp32p4-rev1", "p4-ip101", release.PublicRaw(e.key))
		n.Addr, n.Gateway, n.Unadopted, n.DownFor = fmt.Sprintf("203.0.113.%d", last), "203.0.113.1", unadopted, 20*time.Millisecond
		e.lan.Add(n)
		return n
	}
	e.peer, e.node = mk("dns1", 51, false), mk("new", 52, true)
	if _, err := pins.Open(e.dir).Pin(e.peer.ID(), "203.0.113.51"); err != nil { // adopted before
		t.Fatal(err)
	}
	e.lan.Other("203.0.113.99", http.NotFoundHandler())
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"203.0.113.51"}, Primary: zp()}); err != nil {
		t.Fatal(err)
	}
	e.config("n2.json", `{"name": "dns2", "forwarders": ["9.9.9.9"], "secondary": {"primary": "203.0.113.254", "zones": ["home.example", "lab.example"]}}`)
	return e
}

func (e *env) config(name, text string) {
	p := filepath.Join(configs.Path(e.dir), name)
	os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) logf(f string, a ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, fmt.Sprintf(f, a...))
}

func (e *env) log() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.logs, "\n")
}

func (e *env) client() *fleet.Client {
	hc := e.lan.HTTP()
	return &fleet.Client{HTTP: hc, DNS: e.lan, PeerDNS: e.lan, Poll: 10 * time.Millisecond, Logf: e.logf,
		Pusher: &release.Pusher{Key: e.key, KeyID: release.KeyRelease, Pins: pins.Open(e.dir), Client: hc}}
}

func (e *env) req(r Request) Request {
	r.DataDir, r.Wait = e.dir, 5*time.Second
	if r.Host == "" {
		r.Host = "203.0.113.52"
	}
	if r.Config == "" {
		r.Config = "n2.json"
	}
	return r
}

// zp is the zone primary in settings.json: it allows zone transfers from 203.0.113.0/24.
func zp() *primary.Config {
	return &primary.Config{Address: "203.0.113.254", Allows: []string{"203.0.113.0/24"}}
}

func (e *env) adopt(r Request) (Result, error) {
	ctx := context.Background()
	c := e.client()
	b, err := Build(ctx, c, e.req(r), e.logf)
	if err != nil {
		return Result{}, err
	}
	return Run(ctx, c, b, "test", e.logf)
}

func (e *env) pushes(n *fakenode.Node) []string {
	return slices.DeleteFunc(n.Events(), func(s string) bool { return !strings.HasPrefix(s, "push config") })
}

// A node adopted onto a new static address, in a network the zone primary allows (nothing
// said about it), the config goes to where it is, it reboots onto the address, is confirmed there,
// the config it runs is recorded and its address added to settings.json.
func TestAdoptNewAddress(t *testing.T) {
	e := newEnv(t)
	// The dry run: nothing changed anywhere.
	r, err := e.adopt(Request{Address: "203.0.113.60/24", DryRun: true})
	if err != nil {
		t.Fatal(err, e.log())
	}
	if len(e.pushes(e.node)) != 0 || r.Seq != 0 {
		t.Fatal("the dry run changed something", e.node.Events())
	}
	if _, err := pins.Open(e.dir).Seq(e.node.ID(), "config"); !errors.Is(err, pins.ErrNotPinned) {
		t.Fatal("the dry run pinned the node:", err)
	}
	if !strings.Contains(e.log(), "4. the zone primary allows zone transfers from 203.0.113.0/24, which 203.0.113.60 is in") ||
		strings.Contains(e.log(), "must allow") {
		t.Fatal(e.log())
	}
	r, err = e.adopt(Request{Address: "203.0.113.60/24", AddToSettings: true})
	if err != nil {
		t.Fatal(err, e.log())
	}
	if ev := e.node.Events(); !slices.Contains(ev, "moved to 203.0.113.60") || len(e.pushes(e.node)) != 1 {
		t.Fatal(ev)
	}
	if r.Addr != "203.0.113.60" || r.NodeID != e.node.ID() || !r.Recorded || !r.AddedToSettings {
		t.Fatalf("%+v", r)
	}
	p, err := configs.LoadPushed(e.dir, e.node.ID())
	if err != nil || p == nil || p.File != "n2.json" || p.Host != "203.0.113.60" || p.Seq != r.Seq || p.By != "test" ||
		!strings.Contains(string(p.Payload), `"address":"203.0.113.60/24"`) {
		t.Fatalf("%+v %v", p, err)
	}
	s, _ := settings.Load(settings.Path(e.dir))
	if !slices.Equal(s.Nodes, []string{"203.0.113.51", "203.0.113.60"}) || s.ZonePrimary().Address != "203.0.113.254" {
		t.Fatal(s)
	}
	// Its ID pinned where it was adopted, then moved with it; the config's seq recorded.
	l := pins.Open(e.dir)
	if id, err := l.Pinned("203.0.113.60"); err != nil || id != e.node.ID() {
		t.Fatal("pin:", id, err)
	}
	if _, err := l.Pinned("203.0.113.52"); !errors.Is(err, pins.ErrNotPinned) {
		t.Fatal("pin left at the old address:", err)
	}
	if seq, _ := l.Seq(e.node.ID(), "config"); seq != r.Seq {
		t.Fatalf("recorded config seq %d, pushed %d", seq, r.Seq)
	}
	// Its node key pinned with it, as its /status gave it at adoption.
	if k, err := l.NodeKey(e.node.ID()); err != nil || k == "" || k != e.node.NodeKey() {
		t.Fatal("node key:", k, err)
	}
	// Two adopted: the clients' DNS list.
	if !slices.Equal(r.Servers, []string{"203.0.113.60", "203.0.113.51"}) {
		t.Fatal(r.Servers)
	}
}

// A node listed in settings.json by the address it was flashed with: adopted onto another,
// its entry moves with it (in its place), when asked; else the log says to change it.
func TestAdoptListedMoves(t *testing.T) {
	e := newEnv(t)
	settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"203.0.113.52", "203.0.113.51"}, Canary: "203.0.113.52",
		Primary: zp()})
	r, err := e.adopt(Request{Address: "203.0.113.61/24", AddToSettings: true})
	if err != nil || !r.AddedToSettings {
		t.Fatal(err, r)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); !slices.Equal(s.Nodes, []string{"203.0.113.61", "203.0.113.51"}) || s.Canary != "203.0.113.61" {
		t.Fatal(s)
	}
}

// The address the node runs on, kept (no address given, none in the config): applied live,
// recorded; not added to settings.json unless asked, and the log says so.
func TestAdoptKeepsAddress(t *testing.T) {
	e := newEnv(t)
	r, err := e.adopt(Request{})
	if err != nil {
		t.Fatal(err, e.log())
	}
	if r.Addr != "203.0.113.52" || !r.Recorded || r.AddedToSettings || r.InSettings || slices.Contains(e.node.Events(), "reboot") {
		t.Fatalf("%+v %v", r, e.node.Events())
	}
	if !strings.Contains(e.log(), "static address 203.0.113.52/24, gateway 203.0.113.1 (kept, from its board)") ||
		!strings.Contains(e.log(), "203.0.113.52 is not in settings.json") {
		t.Fatal(e.log())
	}
}

// An address something else answers on, or another node's in settings.json: refused
// before anything changes.
func TestAdoptAddressTaken(t *testing.T) {
	e := newEnv(t)
	_, err := e.adopt(Request{Address: "203.0.113.99/24"})
	if err == nil || !strings.Contains(err.Error(), "3. 203.0.113.99 is in use: something there answers HTTP") {
		t.Fatal(err)
	}
	e.peer.Break(true) // off: still its address
	_, err = e.adopt(Request{Address: "203.0.113.51/24"})
	if err == nil || !strings.Contains(err.Error(), "203.0.113.51 is 203.0.113.51's address") {
		t.Fatal(err)
	}
	if len(e.pushes(e.node)) != 0 {
		t.Fatal("changed:", e.node.Events())
	}
}

// The config's checks for the node (internal/configs), with its network set: a config the
// node's firmware would refuse stops the adoption at step 3.
func TestAdoptConfigCheck(t *testing.T) {
	e := newEnv(t)
	e.config("cpu.json", `{"name": "dns2", "cpu": {"dfs": true}}`)
	_, err := e.adopt(Request{Config: "cpu.json"})
	if err == nil || !strings.HasPrefix(err.Error(), "3. the config") || !strings.Contains(err.Error(), `sets "cpu"`) {
		t.Fatal(err)
	}
	if len(e.pushes(e.node)) != 0 {
		t.Fatal("changed:", e.node.Events())
	}
}

// A node whose address is in none of the networks the zone primary allows: the dry run and
// the adoption say once what to set up there, and go on (nothing is waited for). With no
// zone primary set up at all, the same.
func TestAdoptPrimaryAllows(t *testing.T) {
	for _, p := range []*primary.Config{{Address: "203.0.113.254", Allows: []string{"198.51.100.0/24"}}, nil} {
		e := newEnv(t)
		if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"203.0.113.51"}, Primary: p}); err != nil {
			t.Fatal(err)
		}
		const want = "4. Your zone primary must allow zone transfers from 203.0.113.52: allow its network (e.g. 203.0.113.0/24) there once."
		if _, err := e.adopt(Request{DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if strings.Count(e.log(), want) != 1 {
			t.Fatal(e.log())
		}
		if _, err := e.adopt(Request{}); err != nil || len(e.pushes(e.node)) != 1 {
			t.Fatal(err, e.node.Events())
		}
		if strings.Count(e.log(), want) != 2 {
			t.Fatal(e.log())
		}
	}
	// A config with no secondary zones: nothing said about a primary.
	e := newEnv(t)
	e.config("plain.json", `{"name": "dns2", "forwarders": ["9.9.9.9"]}`)
	settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"203.0.113.51"}})
	if _, err := e.adopt(Request{Config: "plain.json", DryRun: true}); err != nil || strings.Contains(e.log(), "must allow") {
		t.Fatal(err, e.log())
	}
}

// A settings.json written for an API-driven primary (its kind, its API, its pinned
// certificate) still loads: those fields are ignored, the API's IP address taken as the
// primary's address, and adoption goes on as for any primary.
func TestAdoptOldSettings(t *testing.T) {
	e := newEnv(t)
	old := `{"nodes": ["203.0.113.51"], "primary": {"kind": "technitium", "url": "https://203.0.113.254:53443", "cert_sha256": "` +
		strings.Repeat("ab", 32) + `"}}`
	if err := os.WriteFile(settings.Path(e.dir), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := settings.Load(settings.Path(e.dir))
	if err != nil || s.ZonePrimary().Address != "203.0.113.254" || !slices.Equal(s.ZonePrimary().Ignored(), []string{"kind", "url", "cert_sha256"}) {
		t.Fatal(s, err)
	}
	if _, err := e.adopt(Request{DryRun: true}); err != nil || !strings.Contains(e.log(), "must allow zone transfers from 203.0.113.52") {
		t.Fatal(err, e.log())
	}
}

// No DHCP on a network in settings.json's no_dhcp: a config on DHCP is refused there,
// whatever else. With no no_dhcp (the default) none is assumed: DHCP is offered anywhere.
func TestNoDHCPThere(t *testing.T) {
	e := newEnv(t)
	if _, err := Build(context.Background(), e.client(), e.req(Request{Host: "192.0.2.77", Address: "dhcp", Reserved: true}),
		e.logf); err != nil && strings.Contains(err.Error(), "DHCP server") {
		t.Fatal("refused with no no_dhcp:", err)
	}
	s, _ := settings.Load(settings.Path(e.dir))
	s.NoDHCP = []string{"192.0.2.0/23", "192.0.3.128/25"}
	if err := settings.Save(settings.Path(e.dir), s); err != nil {
		t.Fatal(err)
	}
	_, err := Build(context.Background(), e.client(), e.req(Request{Host: "192.0.2.77", Address: "dhcp", Reserved: true}), e.logf)
	if err == nil || !strings.Contains(err.Error(), "192.0.2.0/23, which has no DHCP server (settings.json, no_dhcp)") {
		t.Fatal(err)
	}
	// Elsewhere it is offered, with the reservation.
	b, err := Build(context.Background(), e.client(), e.req(Request{Address: "dhcp"}), e.logf)
	if err != nil {
		t.Fatal(err)
	}
	// -host given as a name: the address the node reports is checked at step 3.
	if err := b.Adopt.Check(b.Adopt.Config, release.NodeStatus{IP: "192.0.3.9"}); err == nil ||
		!strings.Contains(err.Error(), "no DHCP server") {
		t.Fatal(err)
	}
}

// While a node is adopted, the other nodes count only when added (settings.json) and
// adopted, or named (-peer): one found over mDNS that another controller adopted doesn't.
func TestAdoptPeersAddedOnly(t *testing.T) {
	host, a, b, other := "192.0.2.5", "192.0.2.2", "192.0.2.3", "192.0.2.9"
	node := func(addr string, adopted bool) fleet.Node {
		return fleet.Node{Addr: addr, Advert: &nodes.Advert{Addr: addr, Adopted: adopted}}
	}
	var said []string
	got := peers([]fleet.Node{node(host, false), node(a, true), node(b, false), node(other, true)}, host,
		[]string{a, b}, nil, func(f string, v ...any) { said = append(said, fmt.Sprintf(f, v...)) })
	if !slices.Equal(got, []string{a}) || len(said) != 1 || !strings.Contains(said[0], other+" not counted as a peer: found but not added") {
		t.Errorf("%v %q", got, said)
	}
	if got := peers([]fleet.Node{node(other, false)}, host, nil, []string{other}, t.Logf); !slices.Equal(got, []string{other}) {
		t.Errorf("-peer: %v", got)
	}
}
