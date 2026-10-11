package rolling

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
