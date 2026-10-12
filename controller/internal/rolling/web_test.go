package rolling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// noNet is a transport that reaches nothing.
type noNet struct{}

func (noNet) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network here")
}

// A node out of service: left out of a Push page request (Skipped), and the canary with it;
// alone, refused; the node being returned (Returning, never from JSON) is taken. Build
// refuses it as a target unless returning, and Counted doesn't count it.
func TestWebOutOfService(t *testing.T) {
	dir := t.TempDir()
	a, b := "192.0.2.2", "192.0.2.3"
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: []string{a, b}, OutOfService: []string{b}}); err != nil {
		t.Fatal(err)
	}
	r, err := Web{Kind: "zones", Nodes: []string{a, b}, Canary: b, EmptyZones: true}.Request(dir, "")
	if err != nil || !slices.Equal(r.Hosts, []string{a}) || !slices.Equal(r.Skipped, []string{b}) || r.Canary != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := (Web{Kind: "zones", Nodes: []string{b}, EmptyZones: true}).Request(dir, ""); err == nil || !strings.Contains(err.Error(), "out of service") {
		t.Errorf("only it: %v", err)
	}
	r, err = Web{Kind: "zones", Nodes: []string{b}, EmptyZones: true, Returning: b}.Request(dir, "")
	if err != nil || !slices.Equal(r.Hosts, []string{b}) || r.Returning != b {
		t.Fatalf("returning: %+v %v", r, err)
	}
	if _, err := ParseWeb([]byte(`{"kind": "zones", "nodes": ["192.0.2.3"], "Returning": "192.0.2.3"}`)); err == nil {
		t.Error("Returning from JSON")
	}
	if _, err := Build(context.Background(), &fleet.Client{}, Request{Kind: release.Zones, Hosts: []string{b}, DataDir: dir, EmptyZones: true},
		func(string, ...any) {}); err == nil || !strings.Contains(err.Error(), "out of service") {
		t.Errorf("Build: %v", err)
	}
	var said []string
	// Nothing is sent anywhere: every node read fails here.
	ns, err := Counted(context.Background(), &fleet.Client{HTTP: &http.Client{Transport: noNet{}}}, Request{DataDir: dir},
		func(f string, v ...any) { said = append(said, fmt.Sprintf(f, v...)) })
	if err != nil || len(ns) != 1 || ns[0].Addr != a || len(said) != 1 || !strings.Contains(said[0], b+": skipped: out of service") {
		t.Errorf("Counted: %+v %v %q", ns, err, said)
	}
}

// No hosted zones at all (EmptyZones) only from the controller's apply, never from the
// Push page's JSON; and never with zones named.
func TestWebEmptyZones(t *testing.T) {
	dir := t.TempDir()
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: []string{"192.0.2.2"}}); err != nil {
		t.Fatal(err)
	}
	r, err := Web{Kind: "zones", Nodes: []string{"192.0.2.2"}, EmptyZones: true}.Request(dir, "")
	if err != nil || !r.EmptyZones || len(r.Zones) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := (Web{Kind: "zones", Nodes: []string{"192.0.2.2"}}).Request(dir, ""); err == nil || !strings.Contains(err.Error(), `"zones"`) {
		t.Errorf("no zones: %v", err)
	}
	if _, err := (Web{Kind: "zones", Nodes: []string{"192.0.2.2"}, Zones: []string{"example.com.zone"}, EmptyZones: true}).Request(dir, ""); err == nil {
		t.Error("both")
	}
	if _, err := ParseWeb([]byte(`{"kind": "zones", "nodes": ["192.0.2.2"], "EmptyZones": true}`)); err == nil {
		t.Error("EmptyZones from JSON")
	}
}

// The chip images Get firmware gets by default: each node's, its board's image when it runs
// that or its sibling (as an update moves it), else the one it runs; every chip image
// already imported only when a listed node has no status; sorted, each once.
func TestNeededImages(t *testing.T) {
	dir, catalog := t.TempDir(), t.TempDir()
	if got := NeededImages(dir, catalog, nil, false); len(got) != 0 {
		t.Fatalf("nothing there: %v", got)
	}
	for name, img := range map[string]string{"ws-s3-eth": "esp32s3-eth", "p4": "esp32p4-rev1"} {
		if err := os.WriteFile(filepath.Join(catalog, name+".json"), []byte(`{"image":"`+img+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "boards"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "boards", "mine.json"), []byte(`{"image":"esp32s3-quad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, img := range []string{"esp32s3-octal", ".esp32p4.import"} {
		if err := os.MkdirAll(filepath.Join(dir, ImagesDir, img), 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, ImagesDir, img, "image.json"), []byte(`{}`), 0o600)
	}
	nodes := []release.NodeStatus{
		{Board: "ws-s3-eth", Image: "esp32s3-octal"}, // its board's image, the sibling it moves to
		{Board: "p4", Image: "esp32p4-rev1"},
		{Board: "mine"},                     // a saved board, before chip images
		{Board: "gone", Image: "esp32p4"},   // a board not here: the image it runs
		{Board: "p4", Image: "esp32s3-eth"}, // not swappable: the image it runs
		{},
	}
	want := []string{"esp32p4", "esp32p4-rev1", "esp32s3-eth", "esp32s3-quad"}
	if got := NeededImages(dir, catalog, nodes, false); !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	// A node with no status: the imported one (esp32s3-octal) too
	if got := NeededImages(dir, catalog, nodes, true); !slices.Equal(got, []string{"esp32p4", "esp32p4-rev1", "esp32s3-eth", "esp32s3-octal", "esp32s3-quad"}) {
		t.Errorf("with a node unknown: %v", got)
	}
	if got := NeededImages(dir, catalog, nil, false); len(got) != 0 {
		t.Errorf("no nodes: %v", got)
	}
	if got := NeededImages(dir, catalog, nil, true); !slices.Equal(got, []string{"esp32s3-octal"}) {
		t.Errorf("no node known: %v", got)
	}
}
