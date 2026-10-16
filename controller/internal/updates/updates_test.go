package updates

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/release"
)

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func status(image, board, version, elf, built, name string) *release.NodeStatus {
	st := &release.NodeStatus{Image: image, Board: board, Version: version, ElfSHA256: elf, Built: built}
	if name != "" {
		st.Config = &release.ConfigStatus{Name: name}
	}
	return st
}

func TestVersion(t *testing.T) {
	// What is shown: the descriptor's version, plain, else the build.
	if v := Of(" 0.0.4\x00\n", "", "00000000000000d4"); v.Text != "0.0.4" || !v.ok {
		t.Errorf("text %q", v.Text)
	}
	if v := Of("", "", "00000000000000d4"); v.Text != "00000000000000d4" || v.ok {
		t.Errorf("no version: %q", v.Text)
	}
	if v := Of(strings.Repeat("v", 40), "", "00000000000000d4"); len(v.Text) != maxText {
		t.Errorf("long: %q", v.Text)
	}
}

// fleet is a data directory with a P4 board's build and an exported S3 image of 0.0.3.
func fleet(t *testing.T) (dir, catalog string) {
	dir, catalog = t.TempDir(), t.TempDir()
	write(t, filepath.Join(catalog, "p4-board.json"), []byte(`{"image": "esp32p4-rev1"}`))
	write(t, filepath.Join(dir, "firmware/builds/p4-board/dns2.bin"), fakenode.AppBuilt("0.0.9", "00000000000000b2", "Oct  7 2026", "09:30:00"))
	write(t, filepath.Join(dir, "firmware/images/esp32s3/image.json"), []byte(`{"image": "esp32s3", "chip": "esp32s3"}`))
	write(t, filepath.Join(dir, "firmware/images/esp32s3/app.bin"), fakenode.AppBuilt("0.0.3", "00000000000000c1", "Oct  5 2026", "08:00:00"))
	return dir, catalog
}

func TestLatest(t *testing.T) {
	dir := t.TempDir()
	if Latest(dir) != "" {
		t.Fatal("none yet")
	}
	if err := SetLatest(dir, "0.0.4"); err != nil || Latest(dir) != "0.0.4" {
		t.Fatalf("%q %v", Latest(dir), err)
	}
}

// The newest version is the forge's newest release, or a newer chip image held; a board's
// build doesn't count. A node on an older version is out of date whatever its chip or board.
func TestReport(t *testing.T) {
	dir, catalog := fleet(t)
	builds := Builds(dir, catalog)
	if n := Newest("", builds); n.Text != "0.0.3" {
		t.Errorf("the image held: %q", n.Text)
	}
	if n := Newest("0.0.2", builds); n.Text != "0.0.3" {
		t.Errorf("a newer image held over the release: %q", n.Text)
	}
	if n := Newest("0.0.4", builds); n.Text != "0.0.4" {
		t.Errorf("the release: %q", n.Text)
	}
	ns := []Node{
		// A chip whose image isn't held, on a board with a build: out of date all the same.
		{Host: "192.0.2.52", Listed: true, Online: true, Status: status("esp32p4-rev1", "p4-board", "0.0.1", "00000000000000a1", "", "dns2")},
		{Host: "192.0.2.53", Listed: true, Online: true, Status: status("esp32s3", "s3-board", "0.0.4", "00000000000000c1", "", "dns3")},
		{Host: "192.0.2.54", Listed: true, Status: status("esp32s3-octal", "xiao", "0.0.9", "00000000000000d1", "", "")},
		{Host: "192.0.2.55", Listed: true, Status: status("esp32s3", "", "3fbae0a", "00000000000000e1", "", "")},
		{Host: "192.0.2.56", Listed: true},
		{Host: "192.0.2.57", Listed: false, Status: status("esp32s3", "", "0.0.1", "00000000000000a1", "", "")},
		{Host: "192.0.2.58", Listed: true, Status: status("esp32s3-octal", "xiao", "0.0.2", "00000000000000a1", "", "")},
	}
	pending := []changes.Change{{ID: "c1", Kind: changes.Firmware, Firmware: "images/esp32s3-octal", Nodes: []string{"192.0.2.58"}, Summary: "Update 192.0.2.58"}}
	r := For(ns, builds, "0.0.4", pending)
	if r.Scheme != Scheme || r.Newest != "0.0.4" {
		t.Errorf("scheme %q newest %q", r.Scheme, r.Newest)
	}
	want := []string{Available, Current, Newer, Unknown, Unknown, Available, Available}
	for i, u := range r.Nodes {
		if u.State != want[i] {
			t.Errorf("%s: %s (%s), want %s", u.Host, u.State, u.Why, want[i])
		}
	}
	if u := r.Nodes[0]; u.Name != "dns2" || u.Runs.Text != "0.0.1" || u.Newest.Text != "0.0.4" || u.Why != "Update available: 0.0.1 → 0.0.4" {
		t.Errorf("dns2 %+v", u)
	}
	if u := r.Nodes[6]; u.Pending != "c1" || u.Offered() {
		t.Errorf("pending: %+v", u)
	}
	// Offered: listed, available, none pending.
	if !slices.Equal(r.Available, []string{"192.0.2.52"}) {
		t.Errorf("available %v", r.Available)
	}
	// No newest known: unknown, nothing offered.
	if u := For(ns[:1], nil, "", nil).Nodes[0]; u.State != Unknown || u.Newest != nil {
		t.Errorf("none known: %+v", u)
	}
}

// A node whose protocol this controller doesn't speak is offered nothing, and says so; none
// said (firmware from before "proto") is protocol 1.
func TestProto(t *testing.T) {
	ns := []Node{
		{Host: "192.0.2.52", Listed: true, Status: status("esp32s3", "s3-board", "0.0.1", "", "", "")},
		{Host: "192.0.2.53", Listed: true, Status: status("esp32s3", "s3-board", "0.0.1", "", "", "")},
		{Host: "192.0.2.54", Listed: true, Status: status("esp32s3", "s3-board", "0.0.1", "", "", "")},
	}
	ns[1].Status.Proto = ProtoMax
	ns[2].Status.Proto = ProtoMax + 1
	r := For(ns, nil, "0.0.4", nil)
	if r.Nodes[0].State != Available || r.Nodes[1].State != Available {
		t.Errorf("%+v", r.Nodes[:2])
	}
	if u := r.Nodes[2]; u.State != Unknown || !strings.Contains(u.Why, "node protocol 2, which this controller (1 to 1) doesn't") {
		t.Errorf("%+v", u)
	}
	if _, err := r.Plan([]string{"192.0.2.54"}); err == nil || !strings.Contains(err.Error(), "node protocol 2") {
		t.Errorf("planned: %v", err)
	}
}

func TestPlan(t *testing.T) {
	ns := []Node{
		{Host: "192.0.2.52", Listed: true, Status: status("esp32p4-rev1", "p4-board", "0.0.1", "", "", "dns2")},
		{Host: "192.0.2.53", Listed: true, Status: status("esp32s3", "s3-board", "0.0.2", "", "", "dns3")},
		{Host: "192.0.2.54", Listed: true, Status: status("esp32p4-rev1", "other-p4", "0.0.1", "", "", "")},
		{Host: "192.0.2.55", Listed: true, Status: status("esp32s3", "s3-board", "0.0.3", "", "", "dns5")},
		{Host: "192.0.2.56", Listed: false, Status: status("esp32s3", "s3-board", "0.0.2", "", "", "")},
		{Host: "192.0.2.57", Listed: true, Status: status("", "", "0.0.1", "", "", "")},
	}
	r := For(ns, nil, "0.0.3", nil)
	// Update all: one change per chip image, whatever the boards.
	offers, err := r.Plan(nil)
	if err == nil {
		t.Fatal("a node of no chip image: planned")
	}
	offers, err = r.Plan([]string{"192.0.2.52", "192.0.2.53", "192.0.2.54"})
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 2 || offers[0].Source != "images/esp32p4-rev1" || !slices.Equal(offers[0].Nodes, []string{"192.0.2.52", "192.0.2.54"}) ||
		offers[1].Source != "images/esp32s3" || !slices.Equal(offers[1].Nodes, []string{"192.0.2.53"}) {
		t.Fatalf("offers %+v", offers)
	}
	if offers[0].Summary != "Update dns2, 192.0.2.54 from 0.0.1 to 0.0.3" || offers[1].Summary != "Update dns3 from 0.0.2 to 0.0.3" {
		t.Errorf("summaries %+v", offers)
	}
	for _, bad := range [][]string{{"192.0.2.55"}, {"192.0.2.56"}, {"192.0.2.99"}, {"192.0.2.52", "192.0.2.52"}, {""}} {
		if _, err := r.Plan(bad); err == nil {
			t.Errorf("%v: planned", bad)
		}
	}
	// Nothing to update.
	if _, err := For(ns[3:4], nil, "0.0.3", nil).Plan(nil); err == nil {
		t.Error("nothing offered: planned")
	}
	// Many nodes: counted.
	var many []string
	for i := 0; i < 20; i++ {
		many = append(many, "a-long-node-name-"+strings.Repeat("x", 10))
	}
	if s := summary(many, []string{"1"}, "2"); s != "Update 20 nodes from 1 to 2" {
		t.Errorf("many: %q", s)
	}
	if s := summary([]string{"a"}, []string{"1", "2"}, "3"); s != "Update a to 3" {
		t.Errorf("from two: %q", s)
	}
}

// What a node or a build says is shown as one line of valid text, cut at a character.
func TestClean(t *testing.T) {
	if s := Clean(" dns2\n\x1b[31m ", 64); s != "dns2[31m" {
		t.Errorf("control: %q", s)
	}
	if s := Clean("a\xffb", 64); s != "ab" {
		t.Errorf("invalid UTF-8: %q", s)
	}
	if s := Clean(strings.Repeat("é", 20), 5); s != "éé" {
		t.Errorf("cut: %q", s)
	}
	if v := Of(strings.Repeat("é", 20), "", "00000000000000d4"); len(v.Text) > maxText || !utf8.ValidString(v.Text) {
		t.Errorf("version cut: %q", v.Text)
	}
}

// A node's config name with a control character (its /status says it) is shown, and an
// update of it summarized, without it: the summary a change takes is one plain line.
func TestPlanNameCleaned(t *testing.T) {
	r := For([]Node{{Host: "192.0.2.52", Listed: true,
		Status: status("esp32p4-rev1", "p4-board", "0.0.1", "", "", "dns2\nx"+strings.Repeat("y", 300))}}, nil, "0.0.4", nil)
	if n := r.Nodes[0].Name; strings.ContainsAny(n, "\n") || len(n) > maxName {
		t.Errorf("name %q", n)
	}
	offers, err := r.Plan(nil)
	if err != nil || len(offers) != 1 {
		t.Fatalf("%+v %v", offers, err)
	}
	if err := changes.CheckSummary(offers[0].Summary); err != nil {
		t.Errorf("summary %q: %v", offers[0].Summary, err)
	}
}

// A build that can't be read says why without the data directory's path.
func TestBuildErrorNoPath(t *testing.T) {
	dir, catalog := fleet(t)
	write(t, filepath.Join(dir, "firmware/images/esp32s3/app.bin"), []byte("not an image"))
	for _, b := range Builds(dir, catalog) {
		if strings.Contains(b.Error, dir) {
			t.Errorf("%s: %s", b.Source, b.Error)
		}
		if b.Source == "images/esp32s3" && !strings.Contains(b.Error, "firmware/images/esp32s3/app.bin") {
			t.Errorf("why: %q", b.Error)
		}
	}
}

// Left takes out of a chip image's update the nodes on the newest version or a newer one,
// its summary for the nodes left; one offline is kept; a build's is kept whole.
func TestLeft(t *testing.T) {
	ns := []Node{
		{Host: "192.0.2.52", Listed: true, Online: true, Status: status("esp32s3", "s3-board", "0.0.4", "", "", "dns2")},
		{Host: "192.0.2.53", Listed: true, Online: true, Status: status("esp32s3", "s3-board", "0.0.3", "", "", "dns3")},
		{Host: "192.0.2.54", Listed: true, Online: true, Status: status("esp32s3", "s3-board", "0.0.5", "", "", "dns4")},
		{Host: "192.0.2.55", Listed: true},
		{Host: "192.0.2.56", Listed: true, Status: status("esp32s3", "s3-board", "0.0.4", "", "", "dns6")},
	}
	r := For(ns, nil, "0.0.4", nil)
	c := changes.Change{Kind: changes.Firmware, Firmware: "images/esp32s3", Nodes: []string{"192.0.2.52", "192.0.2.53", "192.0.2.54", "192.0.2.55", "192.0.2.56"}, Summary: "x"}
	got := r.Left(c)
	if !slices.Equal(got.Nodes, []string{"192.0.2.53", "192.0.2.55", "192.0.2.56"}) || got.Summary != "Update dns3, 192.0.2.55, dns6 to 0.0.4" {
		t.Errorf("left %+v", got)
	}
	if got := r.Left(changes.Change{Kind: changes.Firmware, Firmware: "images/esp32s3", Nodes: []string{"192.0.2.52"}}); len(got.Nodes) != 0 {
		t.Errorf("reached: %+v", got)
	}
	if got := r.Left(changes.Change{Kind: changes.Firmware, Firmware: "images/esp32s3", Nodes: []string{"192.0.2.53"}, Summary: "x"}); got.Summary != "x" {
		t.Errorf("none taken: %+v", got)
	}
	b := changes.Change{Kind: changes.Firmware, Firmware: "builds/s3-board", Nodes: []string{"192.0.2.52"}, Summary: "x"}
	if got := r.Left(b); !slices.Equal(got.Nodes, b.Nodes) {
		t.Errorf("build: %+v", got)
	}
}
