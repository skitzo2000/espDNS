package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// espdns node out and remove change settings.json only (a node offline is no matter), each
// in the action log; in refuses a node that doesn't answer, which stays out of service.
func TestNodeOutRemove(t *testing.T) {
	dir := t.TempDir()
	a, off := "127.0.0.1:9", "127.0.0.1:7"
	if err := settings.Save(settings.Path(dir), settings.Settings{Nodes: []string{a, off}}); err != nil {
		t.Fatal(err)
	}
	if err := cmdNode([]string{"out", "-node", off, "-data", dir}); err != nil {
		t.Fatal(err)
	}
	if s, _ := settings.Load(settings.Path(dir)); !slices.Equal(s.OutOfService, []string{off}) {
		t.Fatalf("out: %+v", s)
	}
	if err := cmdNode([]string{"out", "-node", off, "-data", dir}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("out twice: %v", err)
	}
	err := cmdNode([]string{"in", "-node", off, "-data", dir, "-key", "testdata/none.pem"})
	if err == nil {
		t.Error("in, with no key and no node answering")
	}
	if s, _ := settings.Load(settings.Path(dir)); !s.IsOutOfService(off) {
		t.Errorf("back in service: %+v", s)
	}
	if err := cmdNode([]string{"remove", "-node", off, "-data", dir}); err != nil {
		t.Fatal(err)
	}
	if s, _ := settings.Load(settings.Path(dir)); !slices.Equal(s.Nodes, []string{a}) || len(s.OutOfService) != 0 {
		t.Fatalf("removed: %+v", s)
	}
	if err := cmdNode([]string{"sideways", "-node", a, "-data", dir}); err == nil {
		t.Error("an unknown subcommand")
	}
	es, _ := actionlog.Tail(dir, 0)
	var acts []string
	for _, e := range es {
		if e.Event == "end" {
			acts = append(acts, e.Action+" "+e.Args[0])
		}
	}
	if !slices.Equal(acts, []string{"node out", "node in", "node remove"}) {
		t.Errorf("action log: %v", acts)
	}
}
