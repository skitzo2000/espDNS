package settings

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/primary"
)

// blocklist_refresh: none is every day, "off" none, a time from an hour to 30 days;
// anything else is refused by its field.
func TestBlocklistRefresh(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 24 * time.Hour, "off": 0, "6h": 6 * time.Hour, "168h": 168 * time.Hour, "720h": 720 * time.Hour} {
		s, err := Parse([]byte(`{"blocklist_refresh": "` + v + `"}`))
		if err != nil || s.Refresh() != want {
			t.Errorf("%q: %v %v", v, s.Refresh(), err)
		}
	}
	for _, v := range []string{"30m", "721h", "daily", "-6h", "0"} {
		_, err := Parse([]byte(`{"blocklist_refresh": "` + v + `"}`))
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "blocklist_refresh" {
			t.Errorf("%q taken: %v", v, err)
		}
	}
}

// The repo's example (configs/example-settings.json, which make data never copies) loads,
// with every field it shows, on documentation addresses only (RFC 5737). The repo has no
// settings file to seed a data directory with: a fresh install starts with none.
func TestExample(t *testing.T) {
	repo := filepath.Join("..", "..", "..")
	s, err := Load(filepath.Join(repo, "configs", "example-settings.json"))
	if err != nil || len(s.Nodes) == 0 || len(s.DNSPeers) == 0 || s.Canary == "" || len(s.NoDHCP) == 0 ||
		s.ZonePrimary().Address == "" || len(s.ZonePrimary().Allows) == 0 || len(s.InternalSources) == 0 || s.BlocklistRefresh == "" {
		t.Fatal(s, err)
	}
	doc := netip.MustParsePrefix("192.0.2.0/24")
	for _, a := range append(append(append([]string{s.Canary, s.ZonePrimary().Address}, s.Nodes...), s.DNSPeers...), s.InternalSources...) {
		if !doc.Contains(netip.MustParseAddr(a)) {
			t.Errorf("%s: not a documentation address", a)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "configs", "fleet.json")); err == nil {
		t.Error("configs/fleet.json is back: a deployment's settings live in its data directory")
	}
}

// no_dhcp: IPv4 networks, each as its network, none twice; NoDHCPAt finds the one an
// address is on. None (the default): DHCP anywhere.
func TestNoDHCP(t *testing.T) {
	for body, want := range map[string]string{
		`{"no_dhcp": ["192.0.2.0"]}`:                    "not an IPv4 network",
		`{"no_dhcp": [""]}`:                             "not an IPv4 network",
		`{"no_dhcp": ["2001:db8::/32"]}`:                "not an IPv4 network",
		`{"no_dhcp": ["192.0.2.1/24"]}`:                 "not a network (192.0.2.0/24 is)",
		`{"no_dhcp": [" 192.0.2.0/24"]}`:                "not an IPv4 network",
		`{"no_dhcp": ["192.0.2.0/24", "192.0.2.0/24"]}`: "twice",
		`{"no_dhcp": "192.0.2.0/24"}`:                   "cannot unmarshal",
	} {
		if _, err := Parse([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	s, err := Parse([]byte(`{"no_dhcp": ["192.0.2.0/23", "198.51.100.0/24"]}`))
	if err != nil {
		t.Fatal(err)
	}
	for a, want := range map[string]string{"192.0.3.9": "192.0.2.0/23", "198.51.100.1": "198.51.100.0/24",
		"192.0.4.1": "", "::ffff:192.0.2.9": "192.0.2.0/23"} {
		p, ok := s.NoDHCPAt(netip.MustParseAddr(a))
		if ok != (want != "") || (ok && p.String() != want) {
			t.Errorf("%s: %v %v, want %q", a, p, ok, want)
		}
	}
	if _, ok := (Settings{}).NoDHCPAt(netip.MustParseAddr("192.0.2.9")); ok {
		t.Error("no no_dhcp, yet a network without DHCP")
	}
	// Saved and read back as written.
	dir := t.TempDir()
	if err := Save(Path(dir), s); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(Path(dir)); err != nil || !reflect.DeepEqual(got, s) {
		t.Fatal(got, err)
	}
}

// A settings file from before "primary" (as the repo's configs/fleet.json seeded one) loads as it is.
func TestLoad(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir), []byte(`{
  "nodes": ["192.0.2.253", "192.0.2.252"],
  "dns_peers": ["192.0.2.254"],
  "dns_peer_zones": ["home.example"]
}`), 0o644)
	s, err := Load(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{Nodes: []string{"192.0.2.253", "192.0.2.252"}, DNSPeers: []string{"192.0.2.254"},
		DNSPeerZones: []string{"home.example"}}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("%+v", s)
	}
	// No file: no settings, no error.
	if s, err := Load(filepath.Join(dir, "none.json")); err != nil || !reflect.DeepEqual(s, Settings{}) {
		t.Fatal(s, err)
	}
}

func TestParseRefuses(t *testing.T) {
	for body, want := range map[string]string{
		`{"node": ["203.0.113.1"]}`:                 "unknown field",
		`{"dns_peer": ["203.0.113.254"]}`:           "unknown field",
		`{"nodes": ["203.0.113.1", "203.0.113.1"]}`: "twice",
		`{"nodes": [""]}`:                           "not a host",
		`{"nodes": [" 203.0.113.1"]}`:               "not a host",
		`{"dns_peers": ["203.0.113.254:"]}`:         "not a host",
		`{"dns_peer_zones": ["home example"]}`:      "not a zone",
		`{"nodes": []} {"nodes": []}`:               "more than one",
		`{"nodes": "203.0.113.1"}`:                  "cannot unmarshal",
	} {
		if _, err := Parse([]byte(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", body, err, want)
		}
	}
	if s, err := Parse([]byte(`{"nodes": ["203.0.113.1:8080"], "dns_peers": ["203.0.113.254:5353"]}`)); err != nil ||
		s.Nodes[0] != "203.0.113.1:8080" {
		t.Fatal(s, err)
	}
}

// Save writes what Load reads back, and refuses what Check refuses.
func TestSave(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Nodes: []string{"203.0.113.1"}, DNSPeers: []string{"203.0.113.254"}}
	if err := Save(Path(dir), s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(Path(dir))
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatal(got, err)
	}
	if err := Save(Path(dir), Settings{Nodes: []string{"a", "a"}}); err == nil {
		t.Fatal("saved a repeated node")
	}
	if got, _ := Load(Path(dir)); !reflect.DeepEqual(got, s) {
		t.Fatal("a refused save changed the file:", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("left behind: %v", ents)
	}
}

// The zone primary: "primary" is its address and the networks it allows zone transfers
// from, checked; an older file's API fields ("kind", "url", "cert_sha256") are accepted and
// ignored (the API's IP address taken as the address when none is given), anything else
// refused; none: the zero Config.
func TestPrimary(t *testing.T) {
	pin := strings.Repeat("0f", 32)
	for _, c := range []struct {
		json    string
		address string
		allows  []string
		zones   []string
		ignored []string
		bad     string
	}{
		{json: `{}`},
		{json: `{"primary": {}}`},
		{json: `{"primary": {"address": "192.0.2.254", "allows": ["192.0.2.0/24", "198.51.100.0/25"], "zones": ["home.example", "_x.lab.example"]}}`,
			address: "192.0.2.254", allows: []string{"192.0.2.0/24", "198.51.100.0/25"}, zones: []string{"home.example", "_x.lab.example"}},
		// An older file: loads, its API fields ignored
		{json: `{"primary": {"kind": "manual"}}`, ignored: []string{"kind"}},
		{json: `{"primary": {"kind": "technitium", "url": "https://192.0.2.254:53443", "cert_sha256": "` + pin + `"}}`,
			address: "192.0.2.254", ignored: []string{"kind", "url", "cert_sha256"}},
		{json: `{"primary": {"kind": "technitium", "url": "http://dns.example:5380"}}`, ignored: []string{"kind", "url"}},
		{json: `{"primary": {"kind": "technitium", "url": "https://192.0.2.9", "address": "192.0.2.254"}}`,
			address: "192.0.2.254", ignored: []string{"kind", "url"}},
		{json: `{"primary": {"token": "x"}}`, bad: "unknown field"},
		{json: `{"technitium": "http://192.0.2.254:5380"}`, bad: "unknown field"},
		{json: `{"primary": {"address": "dns.example"}}`, bad: `"address": "dns.example" is not an IPv4 address`},
		{json: `{"primary": {"address": "2001:db8::1"}}`, bad: "not an IPv4 address"},
		{json: `{"primary": {"allows": ["192.0.2.1/24"]}}`, bad: "192.0.2.1/24 is not a network (192.0.2.0/24 is)"},
		{json: `{"primary": {"allows": ["192.0.2.0"]}}`, bad: "not an IPv4 network"},
		{json: `{"primary": {"allows": ["192.0.2.0/24", "192.0.2.0/24"]}}`, bad: "192.0.2.0/24 twice"},
		{json: `{"primary": {"zones": ["Home.example"]}}`, bad: `"zones": "Home.example" is not a zone name`},
		{json: `{"primary": {"zones": ["home.example."]}}`, bad: "not a zone name"},
		{json: `{"primary": {"zones": ["-a.example"]}}`, bad: "not a zone name"},
		{json: `{"primary": {"zones": ["home.example", "home.example"]}}`, bad: "home.example twice"},
	} {
		s, err := Parse([]byte(c.json))
		zp := s.ZonePrimary()
		switch {
		case c.bad != "" && (err == nil || !strings.Contains(err.Error(), c.bad)):
			t.Errorf("%s: %v, want %q", c.json, err, c.bad)
		case c.bad == "" && (err != nil || zp.Address != c.address || !slices.Equal(zp.Allows, c.allows) || !slices.Equal(zp.Zones, c.zones) ||
			!slices.Equal(zp.Ignored(), c.ignored)):
			t.Errorf("%s: %+v %v %v", c.json, zp, zp.Ignored(), err)
		}
	}
	// Written back: the API fields dropped, and a "primary" left empty dropped whole.
	path := filepath.Join(t.TempDir(), File)
	for in, want := range map[string]string{
		`{"primary": {"kind": "technitium", "url": "https://192.0.2.254:53443", "cert_sha256": "` + pin + `"}}`: `"primary": {
    "address": "192.0.2.254"
  }`,
		`{"nodes": ["192.0.2.53"], "primary": {"kind": "manual"}}`: `"nodes"`,
	} {
		s, err := Parse([]byte(in))
		if err == nil {
			err = Save(path, s)
		}
		b, _ := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), want) || strings.Contains(string(b), "kind") || strings.Contains(string(b), "url") ||
			(want == `"nodes"` && strings.Contains(string(b), "primary")) {
			t.Errorf("%s: wrote %s, %v", in, b, err)
		}
	}
}

// Covers and Uncovered: a node in a network the primary allows needs nothing said; one in
// none is told its network, by its prefix length (24 when unknown), once to allow there.
func TestPrimaryCovers(t *testing.T) {
	c := primary.Config{Address: "192.0.2.254", Allows: []string{"192.0.2.0/24", "198.51.100.128/25"}}
	for addr, want := range map[string]string{
		"192.0.2.53":     "",
		"198.51.100.200": "",
		"198.51.100.9":   "Your zone primary must allow zone transfers from 198.51.100.9: allow its network (e.g. 198.51.100.0/24) there once.",
		"203.0.113.7":    "Your zone primary must allow zone transfers from 203.0.113.7: allow its network (e.g. 203.0.113.0/24) there once.",
		"dns2.local":     "Your zone primary must allow zone transfers from dns2.local: allow its network there once.",
	} {
		if got := c.Uncovered(addr, 0); got != want {
			t.Errorf("%s: %q, want %q", addr, got, want)
		}
	}
	if got := (primary.Config{}).Uncovered("192.0.2.53", 23); !strings.Contains(got, "(e.g. 192.0.2.0/23)") {
		t.Error(got)
	}
}

// MoveNode: in its place, the canary with it.
func TestMoveNode(t *testing.T) {
	p := Path(t.TempDir())
	Save(p, Settings{Nodes: []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}, Canary: "203.0.113.2"})
	if ok, err := MoveNode(p, "203.0.113.2", "203.0.113.9"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s, _ := Load(p)
	if !reflect.DeepEqual(s, Settings{Nodes: []string{"203.0.113.1", "203.0.113.9", "203.0.113.3"}, Canary: "203.0.113.9"}) {
		t.Fatal(s)
	}
	if ok, err := MoveNode(p, "203.0.113.1", "203.0.113.3"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if s, _ := Load(p); !reflect.DeepEqual(s.Nodes, []string{"203.0.113.9", "203.0.113.3"}) {
		t.Fatal(s)
	}
	if ok, err := MoveNode(p, "203.0.113.7", "203.0.113.8"); ok || err != nil {
		t.Fatal(ok, err)
	}
}

// out_of_service: nodes of "nodes"; InService is the others, in order. A node moved keeps
// its place there, SetOutOfService takes one out and back, RemoveNode drops it from both
// (and as the canary).
func TestOutOfService(t *testing.T) {
	p := Path(t.TempDir())
	a, b, c := "203.0.113.1", "203.0.113.2", "203.0.113.3"
	if err := Save(p, Settings{Nodes: []string{a, b, c}, Canary: b}); err != nil {
		t.Fatal(err)
	}
	if ok, err := SetOutOfService(p, b, true); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := SetOutOfService(p, b, true); ok || err != nil {
		t.Fatal("out twice:", ok, err)
	}
	if _, err := SetOutOfService(p, "203.0.113.9", true); err == nil {
		t.Fatal("a node not listed taken out")
	}
	s, _ := Load(p)
	if !slices.Equal(s.OutOfService, []string{b}) || !slices.Equal(s.InService(), []string{a, c}) || !s.IsOutOfService(b) || s.IsOutOfService(a) {
		t.Fatal(s, s.InService())
	}
	if ok, err := MoveNode(p, b, "203.0.113.8"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if s, _ := Load(p); !slices.Equal(s.OutOfService, []string{"203.0.113.8"}) {
		t.Fatal("moved:", s)
	}
	if ok, err := SetOutOfService(p, "203.0.113.8", false); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if s, _ := Load(p); len(s.OutOfService) != 0 || len(s.InService()) != 3 {
		t.Fatal("back:", s)
	}
	SetOutOfService(p, c, true)
	if ok, err := RemoveNode(p, c); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := RemoveNode(p, c); ok || err != nil {
		t.Fatal("removed twice:", ok, err)
	}
	if ok, err := RemoveNode(p, "203.0.113.8"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if s, _ := Load(p); !reflect.DeepEqual(s, Settings{Nodes: []string{a}}) {
		t.Fatal("removed (the canary too):", s)
	}
}

// AddNode: appended, written whole and parsed again; there already, unchanged; a file that
// no longer parses is left alone.
func TestAddNode(t *testing.T) {
	dir := t.TempDir()
	p := Path(dir)
	if err := Save(p, Settings{Nodes: []string{"203.0.113.1"}, DNSPeers: []string{"203.0.113.254"}, Canary: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := AddNode(p, "203.0.113.2"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	s, err := Load(p)
	want := Settings{Nodes: []string{"203.0.113.1", "203.0.113.2"}, DNSPeers: []string{"203.0.113.254"}, Canary: "203.0.113.1"}
	if err != nil || !reflect.DeepEqual(s, want) {
		t.Fatal(s, err)
	}
	if ok, err := AddNode(p, "203.0.113.2"); ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := AddNode(p, "203.0.113.3 x"); ok || err == nil {
		t.Fatal("a bad address added:", ok, err)
	}
	os.WriteFile(p, []byte(`{"nodes": ["203.0.113.1"], "nodez": []}`), 0o644)
	if ok, err := AddNode(p, "203.0.113.2"); ok || err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatal(ok, err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "nodez") {
		t.Fatal("a file that doesn't parse was rewritten")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatal("temporary files left:", ents)
	}
}

// Check's refusals name their field (FieldError), and say it first.
func TestFieldErrors(t *testing.T) {
	for body, field := range map[string]string{
		`{"nodes": ["192.0.2.1", "192.0.2.1"]}`:                                  "nodes",
		`{"dns_peers": [""]}`:                                                    "dns_peers",
		`{"dns_peer_zones": ["a b"]}`:                                            "dns_peer_zones",
		`{"no_dhcp": ["192.0.2.1/24"]}`:                                          "no_dhcp",
		`{"nodes": ["192.0.2.1"], "canary": "192.0.2.2"}`:                        "canary",
		`{"primary": {"address": "x"}}`:                                          "primary",
		`{"nodes": ["192.0.2.1"], "out_of_service": ["192.0.2.2"]}`:              "out_of_service",
		`{"nodes": ["192.0.2.1"], "out_of_service": ["192.0.2.1", "192.0.2.1"]}`: "out_of_service",
		`{"nodes": ["192.0.2.1"], "out_of_service": [" 192.0.2.1"]}`:             "out_of_service",
	} {
		_, err := Parse([]byte(body))
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != field || !strings.HasPrefix(err.Error(), field+": ") {
			t.Errorf("%s: %v, want field %s", body, err, field)
		}
	}
}

// Read gives the version (none: ""; a file that doesn't parse: its version and the error);
// SaveIf writes only on the version read, runs before ahead of the write (its error stops
// it) and after once it is written (its error returned, the file saved).
func TestSaveIf(t *testing.T) {
	p := Path(t.TempDir())
	if _, v, ok, err := Read(p); v != "" || ok || err != nil {
		t.Fatal(v, ok, err)
	}
	one := Settings{Nodes: []string{"192.0.2.1"}}
	if err := SaveIf(p, "x", one, nil, nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("a version for no file: %v", err)
	}
	var order []string
	if err := SaveIf(p, "", one, func() error {
		if _, err := os.Stat(p); err == nil {
			t.Error("before ran after the write")
		}
		order = append(order, "before")
		return nil
	}, func() error {
		if s, _ := Load(p); !reflect.DeepEqual(s, one) {
			t.Error("after ran before the write")
		}
		order = append(order, "after")
		return nil
	}); err != nil || !slices.Equal(order, []string{"before", "after"}) {
		t.Fatal(err, order)
	}
	s, v, ok, err := Read(p)
	if err != nil || !ok || v == "" || !reflect.DeepEqual(s, one) {
		t.Fatal(s, v, ok, err)
	}
	if err := SaveIf(p, "", one, nil, nil); !errors.Is(err, ErrChanged) {
		t.Fatalf("no file, but there is one: %v", err)
	}
	// Changed since it was read (an adoption added a node): refused, neither run.
	AddNode(p, "192.0.2.2")
	ran := false
	if err := SaveIf(p, v, Settings{}, func() error { ran = true; return nil }, func() error { ran = true; return nil }); !errors.Is(err, ErrChanged) || ran {
		t.Fatal(err, ran)
	}
	_, v, _, _ = Read(p)
	if err := SaveIf(p, v, Settings{}, func() error { return errors.New("token") }, func() error { ran = true; return nil }); err == nil || err.Error() != "token" || ran {
		t.Fatal(err, ran)
	}
	if s, _ := Load(p); len(s.Nodes) != 2 {
		t.Fatal("saved though before failed:", s)
	}
	// after's error: returned, the file saved.
	if err := SaveIf(p, v, one, nil, func() error { return errors.New("token") }); err == nil || err.Error() != "token" {
		t.Fatal(err)
	}
	if s, _ := Load(p); !reflect.DeepEqual(s, one) {
		t.Fatal("not saved though only after failed:", s)
	}
	_, v, _, _ = Read(p)
	// Refused by Check: nothing runs.
	if err := SaveIf(p, v, Settings{Canary: "192.0.2.9"}, func() error { ran = true; return nil }, nil); err == nil || ran {
		t.Fatal(err)
	}
	// A file that doesn't parse: its version, so it can be replaced whole.
	os.WriteFile(p, []byte(`{"nodez": []}`), 0o600)
	if _, v, ok, err = Read(p); err == nil || !ok || v == "" {
		t.Fatal(v, ok, err)
	}
	if err := SaveIf(p, v, one, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// internal_sources: hosts alone (a name or an IP address), none twice in any of its forms.
func TestInternalSources(t *testing.T) {
	for body, bad := range map[string]string{
		`{"internal_sources": ["lists.example", "192.0.2.10", "2001:db8::10"]}`: "",
		`{"internal_sources": ["http://lists.example"]}`:                        "not a host name",
		`{"internal_sources": ["lists.example:8080"]}`:                          "not a host name",
		`{"internal_sources": ["*.example"]}`:                                   "not a host name",
		`{"internal_sources": [""]}`:                                            "not a host name",
		`{"internal_sources": ["lists.example", "LISTS.example."]}`:             "twice",
		`{"internal_sources": ["192.0.2.10", "::ffff:192.0.2.10"]}`:             "twice",
	} {
		s, err := Parse([]byte(body))
		switch {
		case bad == "" && (err != nil || len(s.InternalSources) != 3):
			t.Errorf("%s: %v", body, err)
		case bad != "":
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != "internal_sources" || !strings.Contains(err.Error(), bad) {
				t.Errorf("%s: %v", body, err)
			}
		}
	}
}
