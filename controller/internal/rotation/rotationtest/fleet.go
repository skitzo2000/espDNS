// Package rotationtest is a fleet of fake nodes for the key rotation's tests
// (internal/rotation, the controller's): each with its keys partition (a release and a
// recovery key it trusts) and what a node in service stores, pushed with the old release key
// as the controller pushes it (a config recorded as pushed, hosted zones from the zone files,
// overrides and a blocklist from the compiled lists), in a data directory set up as the
// controller's (release.pem, recovery.pub, the pins). Nothing reaches a network: the nodes
// are httptest servers on loopback.
package rotationtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
	"github.com/skitzo2000/espdns/controller/internal/settings"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
	"github.com/skitzo2000/espdns/controller/internal/zones"
)

// Image is the fake nodes' chip image.
const Image = "esp32s3-octal"

// ZoneText is the hosted zone every node serves.
const ZoneText = "$TTL 3600\n@ IN SOA ns1.home.example. hostmaster.home.example. ( 5 3600 600 604800 300 )\n" +
	"  IN NS ns1\nns1 IN A 192.0.2.53\nlaptop IN A 192.0.2.10\n"

// Fleet is the fake nodes and the data directory.
type Fleet struct {
	Data     string
	Release  *ecdsa.PrivateKey // release.pem: the key the nodes trust now
	Recovery *ecdsa.PrivateKey // the recovery key's private half (offline in real life)
	Nodes    []*fakenode.Node
	Addrs    []string
}

// Key is a new P-256 key.
func Key(t testing.TB) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// New is n fake nodes in service, listed in settings.json and pinned, each storing a config,
// hosted zones, overrides and a blocklist signed with the release key.
func New(t testing.TB, n int) *Fleet {
	f := &Fleet{Data: t.TempDir(), Release: Key(t), Recovery: Key(t)}
	if _, err := keys.Import(keys.Path(f.Data), f.Release, false); err != nil {
		t.Fatal(err)
	}
	if err := secfile.Write(keys.RecoveryPath(f.Data), release.PublicRaw(f.Recovery)); err != nil {
		t.Fatal(err)
	}
	if _, err := zonefiles.Save(f.Data, "home.example.zone", []byte(ZoneText), ""); err != nil {
		t.Fatal(err)
	}
	f.WriteList(t, "main.bin", "ads.example")
	f.WriteList(t, "overrides.bin", "tracker.example")
	for i := range n {
		mac := [6]byte{0x02, 0, 0, 0, 0x10, byte(i + 1)}
		nd := fakenode.New(fmt.Sprintf("dns%d", i+1), mac, Image, "ws-s3-eth", release.PublicRaw(f.Release))
		nd.Recovery = release.PublicRaw(f.Recovery)
		srv := httptest.NewServer(nd)
		t.Cleanup(srv.Close)
		addr := strings.TrimPrefix(srv.URL, "http://")
		nd.Do(func(n *fakenode.Node) { n.Addr = addr })
		if _, err := pins.Open(f.Data).Pin(nd.ID(), addr); err != nil {
			t.Fatal(err)
		}
		f.Nodes, f.Addrs = append(f.Nodes, nd), append(f.Addrs, addr)
	}
	if err := settings.Save(settings.Path(f.Data), settings.Settings{Nodes: f.Addrs}); err != nil {
		t.Fatal(err)
	}
	for i := range f.Nodes {
		f.Store(t, i, true)
	}
	return f
}

// WriteList writes a compiled list blocking name into <data>/lists/file.
func (f *Fleet) WriteList(t testing.TB, file, name string) []byte {
	es := []blocklist.Entry{{Name: name}}
	for i := range 200 {
		es = append(es, blocklist.Entry{Name: fmt.Sprintf("x%d.%s", i, name)})
	}
	b, _, err := blocklist.Build(blocklist.Compile(es), blocklist.BuildOptions{Bits: 44, XorBits: 8, Keys: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := secfile.MkdirAll(filepath.Join(f.Data, "lists")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.Data, "lists", file), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return b
}

// Pusher signs with the key k for the fleet's pinned nodes.
func (f *Fleet) Pusher(k *ecdsa.PrivateKey) *release.Pusher {
	return &release.Pusher{Key: k, KeyID: release.KeyRelease, Pins: pins.Open(f.Data)}
}

// Store pushes node i its config (recorded as pushed, if record), the hosted zones, the
// overrides and the blocklist, signed with the release key.
func (f *Fleet) Store(t testing.TB, i int, record bool) {
	ctx := context.Background()
	p, addr, nd := f.Pusher(f.Release), f.Addrs[i], f.Nodes[i]
	cfg := []byte(fmt.Sprintf(`{"name": "dns%d"}`, i+1))
	out, err := p.PushRelease(ctx, addr, release.Config, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if record {
		if err := configs.RecordPushed(f.Data, configs.Pushed{NodeID: nd.ID(), Host: addr, File: fmt.Sprintf("dns%d.json", i+1),
			Seq: out.Seq, Payload: cfg, By: "controller"}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := zonefiles.List(f.Data)
	if err != nil {
		t.Fatal(err)
	}
	set := &zones.Set{}
	for _, z := range files {
		set.Zones = append(set.Zones, z.Parsed)
	}
	bundle, err := set.Bundle(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PushRelease(ctx, addr, release.Zones, bundle); err != nil {
		t.Fatal(err)
	}
	for k, file := range map[release.Kind]string{release.Overrides: "overrides.bin", release.Blocklist: "main.bin"} {
		b, err := os.ReadFile(filepath.Join(f.Data, "lists", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.PushRelease(ctx, addr, k, b); err != nil {
			t.Fatal(err)
		}
	}
}
