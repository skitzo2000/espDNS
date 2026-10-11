package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// offline is an address nothing answers on: a listed node that is off.
const offline = "127.0.0.1:9"

// newServiceEnv is an apply's environment with the service jobs; with off, settings.json
// lists an offline node after the fake ones.
func newServiceEnv(t *testing.T, n int, off bool) *applyEnv {
	e := newApplyEnv(t, n)
	for name, k := range (serviceKind{apply: *e.apply, jobs: func() []jobs.Job { return e.runner.List() }}).kinds() {
		e.kinds[name] = k
	}
	if off {
		if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: append(slices.Clone(e.addrs), offline)}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// job runs a job of kind to its end; its refusal is returned.
func (e *applyEnv) job(kind string, params map[string]any) (jobs.Job, error) {
	e.t.Helper()
	b, _ := json.Marshal(params)
	j, err := e.runner.Start(kind, "admin", b)
	if err != nil {
		return j, err
	}
	return e.wait(j, nil), nil
}

func (e *applyEnv) settings() settings.Settings {
	e.t.Helper()
	s, err := settings.Load(settings.Path(e.dir))
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// An offline node holds up every apply until it is taken out of service, which works while
// it is offline; then the apply skips it, says so, and changes the others.
func TestServiceOutUnblocksApply(t *testing.T) {
	e := newServiceEnv(t, 2, true)
	e.add(changes.Zone, "example.com.zone", zoneOther)
	if j, _, _ := e.runApply(nil); j.State != jobs.Failed || !strings.Contains(j.Error, offline) {
		t.Fatalf("an offline node didn't hold up the apply: %+v", j)
	}
	j, err := e.job("node-out", map[string]any{"node": offline})
	if err != nil || j.State != jobs.Done {
		t.Fatalf("node-out: %v %+v", err, j)
	}
	if s := e.settings(); !slices.Equal(s.OutOfService, []string{offline}) || len(s.Nodes) != 3 {
		t.Fatalf("settings %+v", s)
	}
	if _, err := e.job("node-out", map[string]any{"node": offline}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("out twice: %v", err)
	}
	j, r, p := e.runApply(nil)
	if j.State != jobs.Done || !r.Applied || !slices.Contains(r.Notes, offline+": skipped: out of service") {
		t.Fatalf("apply %+v %+v %+v", j, r, p)
	}
	for i, n := range e.nodes {
		if ev := n.Events(); !slices.Contains(ev, "push zones") {
			t.Errorf("%s: %v", e.addrs[i], ev)
		}
	}
	for _, rr := range r.Rollouts {
		if slices.Contains(rr.Order, offline) {
			t.Errorf("a rollout went to the node out of service: %+v", rr)
		}
	}
	// In the action log, as every job.
	es, _ := actionlog.Tail(e.dir, 0)
	if !slices.ContainsFunc(es, func(x actionlog.Entry) bool {
		return x.Action == "job node-out" && x.Event == "end" && x.Result == "ok"
	}) {
		t.Errorf("action log %+v", es)
	}
	// Returning it while it doesn't answer: refused, and it stays out.
	j, err = e.job("node-in", map[string]any{"node": offline})
	if err != nil || j.State != jobs.Failed || !strings.Contains(j.Error, "doesn't answer") || !strings.Contains(j.Error, "stays out of service") {
		t.Errorf("node-in, offline: %v %+v", err, j)
	}
	if s := e.settings(); !s.IsOutOfService(offline) {
		t.Errorf("back in service without answering: %+v", s)
	}
}

// The Push page's rollout, the config push and the firmware change skip a node out of
// service: the rollout says so and changes the others; a config push to it is refused, as is
// an update for it.
func TestServiceOutSkipped(t *testing.T) {
	e := newServiceEnv(t, 3, false)
	out := e.addrs[2]
	if j, err := e.job("node-out", map[string]any{"node": out}); err != nil || j.State != jobs.Done {
		t.Fatalf("node-out: %v %+v", err, j)
	}
	e.soak = 0
	j, r, _ := e.dryThenPush(map[string]any{"kind": "blocklist", "nodes": e.addrs, "file": "list.bin"})
	if j.State != jobs.Done || slices.Contains(r.Order, out) || len(r.Order) != 2 {
		t.Fatalf("rollout %+v %+v", j, r)
	}
	if full, _ := e.runner.Get(j.ID); !slices.ContainsFunc(full.Log, func(l jobs.Line) bool { return l.Text == out+": skipped: out of service" }) {
		t.Errorf("the rollout doesn't say it skipped %s", out)
	}
	if ev := e.nodes[2].Events(); len(ev) != 0 {
		t.Errorf("the node out of service was changed: %v", ev)
	}
	if _, err := e.start(map[string]any{"kind": "blocklist", "nodes": []string{out}, "file": "list.bin", "dry_run": true}); err == nil ||
		!strings.Contains(err.Error(), "out of service") {
		t.Errorf("a rollout to it alone: %v", err)
	}
	cp := configPush{actions: e.push.actions, catalog: e.catalog, secret: newSecret()}
	if _, err := cp.kind(json.RawMessage(`{"node": "` + out + `", "config": "fake-c.json", "dry_run": true}`)); err == nil ||
		!strings.Contains(err.Error(), "skipped: out of service") {
		t.Errorf("config push: %v", err)
	}
}

// Back in service: it answers and trusts the release key, so it is sent, alone, what it
// missed (its config, the hosted zones, the overrides, newer firmware), each a dry run first,
// and only then counted again.
func TestServiceReturnCatchesUp(t *testing.T) {
	e := newServiceEnv(t, 3, false)
	back := e.nodes[2]
	// Its firmware is older than its board's build.
	back.Do(func(n *fakenode.Node) { n.Version = "0.0.1" })
	writeFile(t, filepath.Join(e.dir, "firmware/builds/p4-ip101/dns2.bin"), fakenode.App("0.0.3", "0000000000000003"))
	if j, err := e.job("node-out", map[string]any{"node": e.addrs[2]}); err != nil || j.State != jobs.Done {
		t.Fatalf("node-out: %v %+v", err, j)
	}
	e.add(changes.Zone, "example.com.zone", zoneOther)
	e.add(changes.Source, "overrides.txt", "ads.example.org\n")
	if j, r, _ := e.runApply(nil); j.State != jobs.Done || !r.Applied {
		t.Fatalf("apply %+v %+v", j, r)
	}
	if ev := back.Events(); len(ev) != 0 {
		t.Fatalf("changed while out of service: %v", ev)
	}
	j, err := e.job("node-in", map[string]any{"node": e.addrs[2]})
	if err != nil || j.State != jobs.Done {
		t.Fatalf("node-in: %v %+v", err, j)
	}
	ev := back.Events()
	for _, want := range []string{"push config", "push zones", "push overrides", "push firmware"} {
		if !slices.Contains(ev, want) {
			t.Errorf("%s not sent: %v", want, ev)
		}
	}
	for i, n := range e.nodes[:2] {
		for _, x := range n.Events() {
			if x == "push config" || x == "push firmware" {
				t.Errorf("%s, in service, was changed by the return: %v", e.addrs[i], n.Events())
			}
		}
	}
	st := fakeStatus(back)
	if hz, _ := json.Marshal(st["hosted"]); !strings.Contains(string(hz), `"example.com"`) {
		t.Errorf("its hosted zones: %s", hz)
	}
	var p applyProgress
	json.Unmarshal(j.Progress, &p)
	if !strings.HasPrefix(p.Outcome, "done") || len(p.Rollouts) != 4 {
		t.Errorf("progress %+v", p)
	}
	if s := e.settings(); s.IsOutOfService(e.addrs[2]) {
		t.Errorf("still out of service: %+v", s)
	}
	if _, err := e.job("node-in", map[string]any{"node": e.addrs[2]}); err == nil || !strings.Contains(err.Error(), "in service already") {
		t.Errorf("in twice: %v", err)
	}
}

// Removed from the group while offline: out of settings.json (and out of service), its
// config file kept. Refused while a pending firmware update names it.
func TestServiceRemove(t *testing.T) {
	e := newServiceEnv(t, 2, true)
	e.job("node-out", map[string]any{"node": offline})
	if _, err := e.job("node-remove", map[string]any{"node": "192.0.2.99"}); err == nil {
		t.Error("a node not listed removed")
	}
	j, err := e.job("node-remove", map[string]any{"node": offline})
	if err != nil || j.State != jobs.Done {
		t.Fatalf("node-remove: %v %+v", err, j)
	}
	if s := e.settings(); !slices.Equal(s.Nodes, e.addrs) || len(s.OutOfService) != 0 {
		t.Errorf("settings %+v", s)
	}
	// A pending firmware update for a node: it isn't removed under it.
	if _, err := e.store.Add(changes.Edit{Kind: changes.Firmware, Firmware: "builds/p4-ip101", Nodes: []string{e.addrs[1]},
		Summary: "Update fake-b", Who: "admin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job("node-remove", map[string]any{"node": e.addrs[1]}); err == nil || !strings.Contains(err.Error(), "discard it first") {
		t.Errorf("removed under a pending update: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configs.Path(e.dir), "fake-b.json")); err != nil {
		t.Errorf("config file: %v", err)
	}
}

// The scheduled refresh skips a node out of service (offline here) and sends the list to
// the others; the fleet check skips it too.
func TestServiceRefreshSkips(t *testing.T) {
	e := newRefreshEnv(t, 2)
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: append(slices.Clone(e.addrs), offline),
		OutOfService: []string{offline}}); err != nil {
		t.Fatal(err)
	}
	e.feed.set("/ads.txt", nameLines("ads", 300))
	e.define(map[string]string{"fleet": "/ads.txt"})
	j := e.pass(false)
	if j == nil || j.State != jobs.Done {
		t.Fatalf("refresh: %+v", j)
	}
	var r applyResult
	json.Unmarshal(j.Result, &r)
	if len(r.Rollouts) != 1 || len(r.Rollouts[0].Done) != 2 || !slices.Contains(r.Notes, offline+": skipped: out of service") {
		t.Errorf("result %+v", r)
	}
	if n := e.pushes(); n != 2 {
		t.Errorf("%d pushes, want one per node in service", n)
	}
}

// A job queued on the node holds up its removal.
func TestServiceRemoveBusy(t *testing.T) {
	e := newServiceEnv(t, 2, false)
	k := serviceKind{apply: *e.apply, jobs: func() []jobs.Job {
		return []jobs.Job{{ID: "x", Kind: "flush", State: jobs.Running, Args: []string{`{"node":"` + e.addrs[0] + `"}`}}}
	}}
	if _, err := k.remove(json.RawMessage(`{"node": "` + e.addrs[0] + `"}`)); err == nil || !strings.Contains(err.Error(), "wait for it to end") {
		t.Errorf("removed under a job: %v", err)
	}
	if _, err := k.remove(json.RawMessage(`{"node": "` + e.addrs[1] + `"}`)); err != nil {
		t.Errorf("another node: %v", err)
	}
}
