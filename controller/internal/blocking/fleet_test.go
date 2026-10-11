package blocking

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
)

// The catalogue: every list a source a definition takes (https, a kind the compile reads),
// each ID, name and source once, each in one of the groups, and about forty of them.
func TestCatalog(t *testing.T) {
	if len(Catalog) < 35 {
		t.Errorf("%d lists", len(Catalog))
	}
	ids, nms, srcs := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, c := range Catalog {
		if err := CheckSource(c.Source); err != nil {
			t.Errorf("%s: %v", c.ID, err)
		}
		s, _ := blocklist.ParseSource(c.Source, false)
		if !strings.HasPrefix(s.Path, "https://") {
			t.Errorf("%s: not https: %s", c.ID, c.Source)
		}
		if !slices.Contains(Groups, c.Group) || c.Name == "" || c.What == "" || c.By == "" {
			t.Errorf("%+v", c)
		}
		if ids[c.ID] || nms[c.Name] || srcs[c.Source] {
			t.Errorf("twice: %+v", c)
		}
		ids[c.ID], nms[c.Name], srcs[c.Source] = true, true, true
		if got, ok := CatalogFor(c.Source); !ok || got.ID != c.ID {
			t.Errorf("CatalogFor %s", c.Source)
		}
		if got, ok := CatalogByID(c.ID); !ok || got.Source != c.Source {
			t.Errorf("CatalogByID %s", c.ID)
		}
		if SourceTitle(c.Source) != c.Name {
			t.Errorf("title %s", SourceTitle(c.Source))
		}
	}
	for _, g := range Groups {
		if !slices.ContainsFunc(Catalog, func(c CatalogList) bool { return c.Group == g }) {
			t.Errorf("group %s has no list", g)
		}
	}
	if SourceTitle("hosts:https://lists.example.com:8443/a/hosts.txt?key=1") != "lists.example.com" || SourceTitle("domains:local.txt") != "local.txt" {
		t.Error("titles of other sources")
	}
}

// The fleet's list edited: the first list made when there is none, lists added (each once),
// turned off and on again, removed; the last in use kept; other lists left as they are.
func TestEditFleet(t *testing.T) {
	pro, oisd := Catalog[2].Source, Catalog[6].Source
	d, sum, err := EditFleet(Defs{}, "add", []string{pro})
	if err != nil || sum != "Add blocklist HaGeZi Pro" || len(d.Lists) != 1 || d.Lists[0].Name != FleetName || !slices.Equal(d.Lists[0].Sources, []string{pro}) {
		t.Fatalf("first: %+v %q %v", d, sum, err)
	}
	d.Lists = append(d.Lists, List{Name: "other", Sources: []string{"domains:x.txt"}})
	d, sum, err = EditFleet(d, "add", []string{oisd, "domains:local.txt"})
	if err != nil || sum != "Add 2 blocklists: OISD Big, local.txt" || len(d.Lists[0].Sources) != 3 {
		t.Fatalf("two: %+v %q %v", d, sum, err)
	}
	if _, _, err := EditFleet(d, "add", []string{pro}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("again: %v", err)
	}
	if _, _, err := EditFleet(d, "add", []string{"domains:../x.txt"}); err == nil {
		t.Error("a bad source taken")
	}
	d, sum, err = EditFleet(d, "unuse", []string{oisd})
	if err != nil || sum != "Stop using blocklist OISD Big" || slices.Contains(d.Lists[0].Sources, oisd) || !slices.Equal(d.Lists[0].Unused, []string{oisd}) {
		t.Fatalf("unuse: %+v %q %v", d, sum, err)
	}
	if _, _, err := EditFleet(d, "unuse", []string{oisd}); !errors.Is(err, ErrNoChange) {
		t.Errorf("unuse again: %v", err)
	}
	if _, _, err := EditFleet(d, "add", []string{oisd}); err == nil || !strings.Contains(err.Error(), "turn it on") {
		t.Errorf("add an unused one: %v", err)
	}
	d, sum, err = EditFleet(d, "use", []string{oisd})
	if err != nil || sum != "Use blocklist OISD Big" || !slices.Contains(d.Lists[0].Sources, oisd) || len(d.Lists[0].Unused) != 0 {
		t.Fatalf("use: %+v %q %v", d, sum, err)
	}
	for _, s := range []string{oisd, "domains:local.txt"} {
		if d, _, err = EditFleet(d, "remove", []string{s}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := EditFleet(d, "unuse", []string{pro}); err == nil || !strings.Contains(err.Error(), "last list in use") {
		t.Errorf("the last off: %v", err)
	}
	if _, _, err := EditFleet(d, "remove", []string{pro}); err == nil || !strings.Contains(err.Error(), "last list in use") {
		t.Errorf("the last removed: %v", err)
	}
	if _, _, err := EditFleet(d, "remove", []string{oisd}); err == nil || !strings.Contains(err.Error(), "not in your blocklists") {
		t.Errorf("not there: %v", err)
	}
	if d.Lists[1].Name != "other" || !slices.Equal(d.Lists[1].Sources, []string{"domains:x.txt"}) {
		t.Errorf("the other list: %+v", d.Lists[1])
	}
	if _, _, err := EditFleet(d, "rename", []string{pro}); err == nil {
		t.Error("an unknown op")
	}
	// The definitions with an unused source parse, and an unused source is checked.
	if _, err := Parse([]byte(`{"lists": [{"name": "main", "sources": ["domains:a.txt"], "unused": ["domains:../b"]}]}`)); err == nil {
		t.Error("a bad unused source parsed")
	}
	if _, err := Parse([]byte(`{"lists": [{"name": "main", "sources": ["domains:a.txt"], "unused": ["domains:a.txt"]}]}`)); err == nil {
		t.Error("a source both in use and not parsed")
	}
}

// The always allowed and blocked sites: a site as typed or pasted, added once, taken out
// with every line naming it, comments kept.
func TestSites(t *testing.T) {
	for in, want := range map[string]string{"Ads.Example.com": "ads.example.com", "*.ads.example.com.": "ads.example.com",
		"https://ads.example.com/x?y": "ads.example.com", " ads.example.com ": "ads.example.com"} {
		if got, err := Site(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "localhost", "a b.example", "192.0.2.1", "ads..example"} {
		if _, err := Site(bad); err == nil {
			t.Errorf("%q taken", bad)
		}
	}
	text := []byte("# mine\nads.example.com\n*.track.example.net # by hand")
	if s := Sites(text); !slices.Equal(s, []string{"ads.example.com", "track.example.net"}) {
		t.Errorf("sites %v", s)
	}
	b, err := EditSites(text, "new.example.org", true)
	if err != nil || string(b) != string(text)+"\nnew.example.org\n" {
		t.Errorf("add: %q %v", b, err)
	}
	if _, err := EditSites(text, "ads.example.com", true); !errors.Is(err, ErrNoChange) {
		t.Errorf("add again: %v", err)
	}
	b, err = EditSites(text, "track.example.net", false)
	if err != nil || string(b) != "# mine\nads.example.com\n" {
		t.Errorf("remove: %q %v", b, err)
	}
	if _, err := EditSites(text, "none.example.org", false); !errors.Is(err, ErrNoChange) {
		t.Errorf("remove one not there: %v", err)
	}
	if b, _ := EditSites(nil, "a.example.com", true); string(b) != "a.example.com\n" {
		t.Errorf("first: %q", b)
	}
}

// The compile's record: each source's count, and the index that names the list blocking a
// name, as the node decides (the name, then its parents; an allow first; an entry for the
// name only matching the name), the overrides first.
func TestCheckName(t *testing.T) {
	dir := t.TempDir()
	l := List{Name: "main", Sources: []string{"domains:a.txt", "wildcard:https://lists.example.com/w.txt?key=secret"}, Allow: []string{"domains:ok.txt"}}
	entries := []IndexEntry{
		{Name: "ads.example.com", Source: 0},
		{Name: "ads.example.com", Suffix: true, Source: 1},
		{Name: "track.example.net", Source: 0},
		{Name: "cdn.example.org", Suffix: true, Source: 1},
		{Name: "good.cdn.example.org", Allow: true, Source: 2},
	}
	res := &blocklist.Result{Sources: []blocklist.SourceResult{{ParseStats: blocklist.ParseStats{Entries: 2}}, {ParseStats: blocklist.ParseStats{Entries: 2}}, {Allow: true, ParseStats: blocklist.ParseStats{Entries: 1}}},
		BlockedExact: 2, BlockedSuffix: 2, AllowedExact: 1}
	at := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)
	if err := WriteCompiled(dir, l, res, entries, at); err != nil {
		t.Fatal(err)
	}
	rep, err := ReadReport(dir, "main")
	if err != nil || !rep.Indexed || rep.Blocked != 4 || !rep.Time.Equal(at) || rep.Specs[1] != "wildcard:https://lists.example.com/w.txt?key=xxxxx" {
		t.Fatalf("report %+v %v", rep, err)
	}
	if n, ok := rep.Count(l.Sources[1]); !ok || n != 2 {
		t.Errorf("count %d %v", n, ok)
	}
	if _, ok := rep.Count("domains:other.txt"); ok {
		t.Error("a count for a source not compiled")
	}
	for _, c := range []struct {
		name string
		want Verdict
		by   string
		srcs []int
	}{
		{"ads.example.com", Blocked, "list", []int{0, 1}},
		{"x.ads.example.com", Blocked, "list", []int{1}},
		{"x.track.example.net", NotBlocked, "", nil},
		{"track.example.net", Blocked, "list", []int{0}},
		{"cdn.example.org", Blocked, "list", []int{1}},
		{"good.cdn.example.org", NotBlocked, "", []int{2}},
		{"example.com", NotBlocked, "", nil},
		{"ADS.Example.com.", Blocked, "list", []int{0, 1}},
	} {
		got, err := CheckName(dir, "main", c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var srcs []int
		for _, e := range got.Entries {
			srcs = append(srcs, e.Source)
		}
		slices.Sort(srcs)
		if got.Result != c.want || got.By != c.by || !slices.Equal(srcs, c.srcs) || !got.Indexed || len(got.Specs) != 3 {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if _, err := CheckName(dir, "main", "not a name"); err == nil {
		t.Error("a bad name checked")
	}
	// The overrides decide first.
	writeTestFile(t, SourcePath(dir, OverridesAllow), "ads.example.com\n")
	writeTestFile(t, SourcePath(dir, OverridesBlock), "*.example.org\n")
	if got, _ := CheckName(dir, "main", "x.ads.example.com"); got.Result != Allowed || got.By != OverridesName {
		t.Errorf("allowed: %+v", got)
	}
	if got, _ := CheckName(dir, "main", "good.cdn.example.org"); got.Result != Blocked || got.By != OverridesName || got.Entries[0].Name != "example.org" {
		t.Errorf("blocked: %+v", got)
	}
	// No index: the list's sources can't be looked at.
	if got, _ := CheckName(dir, "none", "track.example.net"); got.Indexed || got.Result != NotBlocked {
		t.Errorf("no index: %+v", got)
	}
	if got, _ := CheckName(dir, "", "x.example.org"); got.Result != Blocked {
		t.Errorf("no list: %+v", got)
	}
}

// The index's binary search finds every name of a big index, and none not in it.
func TestIndexLookup(t *testing.T) {
	dir := t.TempDir()
	r := rand.New(rand.NewPCG(1, 2))
	want := map[string]int{}
	var es []IndexEntry
	for i := range 20000 {
		n := fmt.Sprintf("%x.%s.example", r.Uint32(), []string{"a", "b", "a-b", "a.b"}[i%4])
		es = append(es, IndexEntry{Name: n, Source: i % 3})
		want[n]++
		if i%7 == 0 {
			es = append(es, IndexEntry{Name: n, Source: 3, Suffix: true})
			want[n]++
		}
	}
	l := List{Name: "big", Sources: []string{"domains:a.txt", "domains:b.txt", "domains:c.txt", "wildcard:d.txt"}}
	if err := WriteCompiled(dir, l, &blocklist.Result{}, es, time.Now()); err != nil {
		t.Fatal(err)
	}
	for n, k := range want {
		got, err := Lookup(dir, "big", n)
		if err != nil || len(got) != k {
			t.Fatalf("%s: %d entries, want %d (%v)", n, len(got), k, err)
		}
	}
	for _, n := range []string{"0.a.example", "zzz.example", "a.example", "", "~"} {
		if got, _ := Lookup(dir, "big", n); len(got) != 0 {
			t.Errorf("%s: %v", n, got)
		}
	}
}

func writeTestFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
