package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/adoption"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// adoptWorld is a data directory (settings.json with a node in service and the zone primary,
// which allows zone transfers from 203.0.113.0/24, a config), and a fake network with that
// node and one to adopt.
type adoptWorld struct {
	data, catalog string
	lan           *fakenode.Lan
	node, peer    *fakenode.Node
	key           *ecdsa.PrivateKey
}

func newAdoptWorld(t *testing.T) *adoptWorld {
	w := &adoptWorld{data: t.TempDir(), catalog: t.TempDir(), lan: fakenode.NewLan()}
	t.Cleanup(w.lan.Close)
	w.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	put(t, filepath.Join(w.catalog, "p4-ip101.json"), []byte(`{"name": "p4-ip101", "image": "esp32p4-rev1"}`))
	for i, name := range []string{"dns1", "new"} {
		n := fakenode.New(name, [6]byte{2, 0, 0, 0, 0, byte(0x51 + i)}, "esp32p4-rev1", "p4-ip101", release.PublicRaw(w.key))
		n.Addr, n.Gateway, n.Unadopted, n.DownFor = []string{"203.0.113.51", "203.0.113.52"}[i], "203.0.113.1", i == 1, 20*time.Millisecond
		w.lan.Add(n)
		if i == 0 {
			w.peer = n
		} else {
			w.node = n
		}
	}
	if err := settings.Save(settings.Path(w.data), settings.Settings{Nodes: []string{"203.0.113.51"}, DNSPeers: []string{"203.0.113.254"},
		DNSPeerZones: []string{"home.example"}, Primary: &primary.Config{Address: "203.0.113.254", Allows: []string{"203.0.113.0/24"}}}); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(configs.Path(w.data), "n2.json"), []byte(`{"name": "dns2", "forwarders": ["9.9.9.9"], `+
		`"secondary": {"primary": "203.0.113.254", "zones": ["home.example", "lab.example"]}}`))
	return w
}

func (w *adoptWorld) client() *fleet.Client {
	hc := w.lan.HTTP()
	return &fleet.Client{HTTP: hc, DNS: w.lan, PeerDNS: w.lan, Poll: 10 * time.Millisecond, Logf: func(string, ...any) {},
		Pusher: &release.Pusher{Key: w.key, KeyID: release.KeyRelease, Pins: pins.Open(w.data), Client: hc}}
}

// adoptView is what an adoption does, without its hooks: what two builds are compared on.
type adoptView struct {
	Host, ID, Address, Gateway string
	DHCP, Reserved             bool
	Payload                    string
	Identify, Wait             time.Duration
	Peers                      []string
	DNSPeers                   []fleet.DNSPeer
	AllowSingle, Force, DryRun bool
	Checks                     fleet.Checks
	CheckSet                   bool
	ConfigName, Primary        string
	Zones                      []string
	ZonePrimary                string
	Allows                     []string
}

func viewOf(t *testing.T, b adoption.Built) adoptView {
	a := b.Adopt
	p, err := a.Config.Payload()
	if err != nil {
		t.Fatal(err)
	}
	v := adoptView{Host: a.Host, ID: a.ID, Address: a.Address.String(), Gateway: a.Gateway.String(), DHCP: a.DHCP,
		Reserved: a.Reserved, Payload: string(p), Identify: a.Identify, Wait: a.ConfirmWait,
		Peers: a.Peers, DNSPeers: a.DNSPeers, AllowSingle: a.AllowSingle, Force: a.Force, DryRun: a.DryRun, Checks: a.Checks,
		CheckSet: a.Check != nil, ConfigName: b.ConfigName, Primary: b.Primary, Zones: b.Zones,
		ZonePrimary: a.Primary.Address, Allows: a.Primary.Allows}
	return v
}

// The CLI's flags, as make fleet-adopt[-dry] passes them, and the Adopt page's request for
// the same adoption make the same request, and it builds into the same adoption: the same
// node and ID, the same address and gateway (or DHCP with its reservation), the same
// config payload and checks, the same peers and DNS peers for the rule, the same zone
// primary from settings.json (its address and the networks it allows). So the page adopts
// as the CLI does, not with a copy of it.
func TestCLIAndAdoptPageBuildTheSameAdoption(t *testing.T) {
	w := newAdoptWorld(t)
	id := w.node.ID()
	cfg := filepath.Join(configs.Path(w.data), "n2.json")
	base := []string{"-key", "-", "-data", w.data, "-mdns", "0", "-host", "203.0.113.52", "-node", id, "-config", cfg,
		"-catalog", w.catalog}
	set := primary.Config{Address: "203.0.113.254", Allows: []string{"203.0.113.0/24"}}
	var none primary.Config
	var newFlags []string
	for _, tc := range []struct {
		name string
		set  primary.Config
		cli  []string
		web  string
	}{
		{"the address it runs on, dry run", set, append(slices.Clone(newFlags), "-dry-run"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","dry_run":true}`},
		{"a new address and gateway", set, append(slices.Clone(newFlags), "-address", "203.0.113.60/24", "-gateway", "203.0.113.1"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","address":"203.0.113.60/24","gateway":"203.0.113.1"}`},
		{"added to settings", set, append(slices.Clone(newFlags), "-add-to-settings"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","add_to_settings":true}`},
		{"DHCP, reserved (on no network in no_dhcp)", set, append(slices.Clone(newFlags), "-address", "dhcp", "-reserved", "-dry-run"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","address":"dhcp","reserved":true,"dry_run":true}`},
		{"+ Add node: DHCP, reserved in the router", set, append(slices.Clone(newFlags), "-address", "dhcp", "-reserved", "-dry-run"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","address":"dhcp","reserved":true,"static":true,"dry_run":true}`},
		{"+ Add node: a fixed address and its gateway", set, append(slices.Clone(newFlags), "-address", "203.0.113.60/24", "-gateway", "203.0.113.1", "-dry-run"),
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","address":"203.0.113.60/24","gateway":"203.0.113.1","static":true,"dry_run":true}`},
		{"no zone primary set up, dry run", none, []string{"-dry-run"},
			`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","dry_run":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := settings.Load(settings.Path(w.data))
			s.Primary = &tc.set
			if tc.set.IsZero() {
				s.Primary = nil
			}
			if err := settings.Save(settings.Path(w.data), s); err != nil {
				t.Fatal(err)
			}
			cliReq, _, err := adoptRequest(append(slices.Clone(base), tc.cli...))
			if err != nil {
				t.Fatal(err)
			}
			web, err := adoption.ParseWeb([]byte(tc.web))
			if err != nil {
				t.Fatal(err)
			}
			webReq, err := web.Request(w.data, w.catalog)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cliReq, webReq) {
				t.Fatalf("requests differ:\nCLI %+v\npage %+v", cliReq, webReq)
			}
			ctx := context.Background()
			cliB, err := adoption.Build(ctx, w.client(), cliReq, func(string, ...any) {})
			if err != nil {
				t.Fatal(err)
			}
			webB, err := adoption.Build(ctx, w.client(), webReq, func(string, ...any) {})
			if err != nil {
				t.Fatal(err)
			}
			cv, wv := viewOf(t, cliB), viewOf(t, webB)
			if !reflect.DeepEqual(cv, wv) {
				t.Fatalf("adoptions differ:\nCLI %+v\npage %+v", cv, wv)
			}
			if cv.ZonePrimary != tc.set.Address || !slices.Equal(cv.Allows, tc.set.Allows) ||
				!slices.Equal(cv.Peers, []string{"203.0.113.51"}) || len(cv.DNSPeers) != 1 || !cv.CheckSet ||
				cv.Wait != adoption.DefaultWait || !slices.Equal(cv.Checks.Forwarded, []string{"example.com"}) {
				t.Fatalf("%+v", cv)
			}
		})
	}
}

// settings.json's no_dhcp: the CLI's flags and the Adopt page's request for DHCP on a
// network named there are refused the same way, from the same setting.
func TestCLIAndAdoptPageNoDHCP(t *testing.T) {
	w := newAdoptWorld(t)
	s, _ := settings.Load(settings.Path(w.data))
	s.NoDHCP = []string{"203.0.113.0/24"}
	if err := settings.Save(settings.Path(w.data), s); err != nil {
		t.Fatal(err)
	}
	id := w.node.ID()
	cliReq, _, err := adoptRequest([]string{"-key", "-", "-data", w.data, "-mdns", "0", "-host", "203.0.113.52", "-node", id,
		"-config", filepath.Join(configs.Path(w.data), "n2.json"), "-catalog", w.catalog,
		"-address", "dhcp", "-reserved", "-dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	web, err := adoption.ParseWeb([]byte(`{"node":"203.0.113.52","node_id":"` + id + `","config":"n2.json","address":"dhcp","reserved":true,"dry_run":true}`))
	if err != nil {
		t.Fatal(err)
	}
	webReq, err := web.Request(w.data, w.catalog)
	if err != nil {
		t.Fatal(err)
	}
	_, cerr := adoption.Build(context.Background(), w.client(), cliReq, func(string, ...any) {})
	_, werr := adoption.Build(context.Background(), w.client(), webReq, func(string, ...any) {})
	if cerr == nil || werr == nil || cerr.Error() != werr.Error() || !strings.Contains(cerr.Error(), "203.0.113.0/24, which has no DHCP server") {
		t.Fatal(cerr, werr)
	}
}

// The page's old "primary_done" is gone: nothing is changed on the primary, so there is
// nothing to confirm done.
func TestAdoptPrimaryFlagsGone(t *testing.T) {
	if _, err := adoption.ParseWeb([]byte(`{"node":"203.0.113.52","primary_done":true}`)); err == nil {
		t.Fatal(`the page's old "primary_done" taken`)
	}
}

// espdns adopt: the node adopted, the config it runs recorded by the CLI, and with
// -add-to-settings its address in settings.json. A node in none of the networks the zone
// primary allows is adopted all the same, after being told what to set up there.
func TestCLIAdoptRecords(t *testing.T) {
	w := newAdoptWorld(t)
	keyFile := filepath.Join(t.TempDir(), "release.pem")
	pem, _ := keys.Encode(w.key)
	put(t, keyFile, pem)
	old := adoptClient
	t.Cleanup(func() { adoptClient = old })
	adoptClient = func(o *fleetOpts) (*fleet.Client, error) {
		c, err := o.client()
		if err != nil {
			return nil, err
		}
		hc := w.lan.HTTP()
		c.HTTP, c.DNS, c.PeerDNS, c.Poll, c.Pusher.Client = hc, w.lan, w.lan, 10*time.Millisecond, hc
		return c, nil
	}
	args := []string{"-key", keyFile, "-data", w.data, "-mdns", "0", "-host", "203.0.113.52", "-node", w.node.ID(),
		"-config", "n2.json", "-address", "203.0.113.60/24", "-catalog", w.catalog, "-add-to-settings", "-wait", "5s"}
	if err := cmdAdopt(args); err != nil {
		t.Fatal(err)
	}
	p, err := configs.LoadPushed(w.data, w.node.ID())
	if err != nil || p == nil || p.By != "cli" || p.File != "n2.json" || p.Host != "203.0.113.60" {
		t.Fatalf("%+v %v", p, err)
	}
	s, _ := settings.Load(settings.Path(w.data))
	if !slices.Equal(s.Nodes, []string{"203.0.113.51", "203.0.113.60"}) {
		t.Fatal(s.Nodes)
	}
	// Outside every network the primary allows: said, and adopted.
	w2 := newAdoptWorld(t)
	adoptClient = func(o *fleetOpts) (*fleet.Client, error) {
		c, err := o.client()
		if err != nil {
			return nil, err
		}
		hc := w2.lan.HTTP()
		c.HTTP, c.DNS, c.PeerDNS, c.Poll, c.Pusher.Client = hc, w2.lan, w2.lan, 10*time.Millisecond, hc
		return c, nil
	}
	s2, _ := settings.Load(settings.Path(w2.data))
	s2.Primary.Allows = []string{"198.51.100.0/24"}
	if err := settings.Save(settings.Path(w2.data), s2); err != nil {
		t.Fatal(err)
	}
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	keyFile2 := filepath.Join(t.TempDir(), "release.pem")
	pem2, _ := keys.Encode(w2.key)
	put(t, keyFile2, pem2)
	if err := cmdAdopt([]string{"-key", keyFile2, "-data", w2.data, "-mdns", "0", "-host", "203.0.113.52", "-config", "n2.json",
		"-catalog", w2.catalog, "-wait", "5s"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), "Your zone primary must allow zone transfers from 203.0.113.52: allow its network (e.g. 203.0.113.0/24) there once.") ||
		!slices.Contains(w2.node.Events(), "push config") {
		t.Fatal(logged.String(), w2.node.Events())
	}
}
