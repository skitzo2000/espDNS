package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/service"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// A node taken out of service, returned to it, or removed from the group (internal/service;
// docs/operations.md, Taking a node out of service), the node page's buttons, each a job
// (under the fleet lock, in the action log, its progress live), as espdns node out, in and
// remove:
//
//	node-out     {"node": "192.0.2.52"}  out of service: still listed and shown with its last status,
//	                                     sent nothing (an apply, a rollout, a refresh, a config push
//	                                     skip it), not counted for the last-healthy-node rule
//	node-in      {"node": "192.0.2.52"}  back in service: it must answer and trust the controller's
//	                                     release key; it is sent, alone, what it missed (its config,
//	                                     the hosted zones, the overrides, its blocklist, newer
//	                                     firmware), each rollout a dry run first, as an apply's; only
//	                                     then is it counted again
//	node-remove  {"node": "192.0.2.52"}  out of settings.json: its config file is kept, the node
//	                                     itself isn't touched (it still answers DNS if it is up)
//	node-restore {"node": "192.0.2.52"}  one of your nodes installed again over USB (waiting.go): sent
//	                                     what it missed as node-in sends it, its service state left
//	                                     as it is (one out of service stays out); failed when it still
//	                                     runs on its board's settings after
//
// node-out and node-remove work while the node is offline: they change settings.json only.
// Neither is taken for a node in the key rotation under way; node-remove isn't queued while
// another job names the node or changes every node (an apply, a refresh, a key rotation), nor
// while a pending firmware update names it.

type serviceKind struct {
	apply applyKind
	// jobs are the runner's jobs (node-remove's refusal while one is on the node); nil: none.
	jobs func() []jobs.Job
	// relist has the node list read settings.json again (nil: within 10 s).
	relist func()
}

func (k serviceKind) kinds() map[string]jobs.Kind {
	return map[string]jobs.Kind{"node-out": k.out, "node-in": k.in, "node-remove": k.remove, "node-restore": k.restore}
}

func (k serviceKind) path() string { return settings.Path(k.apply.dataDir) }

func (k serviceKind) relisted() {
	if k.relist != nil {
		k.relist()
	}
}

// nodeParam reads a service job's params: {"node": "<address>"}.
func nodeParam(raw json.RawMessage) (string, error) {
	var p struct {
		Node string `json:"node"`
	}
	if err := decodeStrict(raw, &p); err != nil {
		return "", err
	}
	if p.Node == "" {
		return "", errors.New(`params: need "node", the node's address`)
	}
	return p.Node, nil
}

func (k serviceKind) out(raw json.RawMessage) (jobs.Func, error) {
	node, err := nodeParam(raw)
	if err != nil {
		return nil, err
	}
	check := func() error {
		s, err := settings.Load(k.path())
		if err != nil {
			return err
		}
		return service.CheckOut(k.apply.dataDir, s, node)
	}
	if err := check(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) {
		if err := check(); err != nil {
			return nil, err
		}
		if _, err := settings.SetOutOfService(k.path(), node, true); err != nil {
			return nil, err
		}
		k.relisted()
		run.Logf("%s: out of service: still listed and shown, sent nothing, not counted while another node is changed", node)
		return map[string]any{"node": node, "out_of_service": true}, nil
	}, nil
}

func (k serviceKind) remove(raw json.RawMessage) (jobs.Func, error) {
	node, err := nodeParam(raw)
	if err != nil {
		return nil, err
	}
	check := func() error {
		s, err := settings.Load(k.path())
		if err != nil {
			return err
		}
		pending, err := k.apply.store.List()
		if err != nil {
			return err
		}
		return service.CheckRemove(k.apply.dataDir, s, node, pending)
	}
	if err := check(); err != nil {
		return nil, err
	}
	if j, ok := k.busy(node); ok {
		return nil, fmt.Errorf("%s: the job %s (%s) is %s: wait for it to end", node, j.ID, j.Kind, j.State)
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) {
		if err := check(); err != nil {
			return nil, err
		}
		removed, err := settings.RemoveNode(k.path(), node)
		if err != nil {
			return nil, err
		}
		k.relisted()
		run.Logf("%s: removed from settings.json; its config file is kept. The node itself isn't touched: if it is up it "+
			"still answers DNS, so take it off your router's DNS servers", node)
		return map[string]any{"node": node, "removed": removed}, nil
	}, nil
}

// busy is a job queued or running that is on node: one whose params name it, or one that
// changes every node.
func (k serviceKind) busy(node string) (jobs.Job, bool) {
	if k.jobs == nil {
		return jobs.Job{}, false
	}
	for _, j := range k.jobs() {
		if j.State.Ended() {
			continue
		}
		if slices.Contains([]string{"apply", "refresh", "key-rotate"}, j.Kind) ||
			slices.ContainsFunc(j.Args, func(a string) bool { return strings.Contains(a, `"`+node+`"`) }) {
			return j, true
		}
	}
	return jobs.Job{}, false
}

func (k serviceKind) in(raw json.RawMessage) (jobs.Func, error) {
	node, err := nodeParam(raw)
	if err != nil {
		return nil, err
	}
	s, err := settings.Load(k.path())
	if err != nil {
		return nil, err
	}
	if err := service.CheckIn(s, node); err != nil {
		return nil, err
	}
	if _, err := k.apply.signer(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) { return k.returnNode(ctx, run, node, false) }, nil
}

func (k serviceKind) restore(raw json.RawMessage) (jobs.Func, error) {
	node, err := nodeParam(raw)
	if err != nil {
		return nil, err
	}
	s, err := settings.Load(k.path())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(s.Nodes, node) {
		return nil, fmt.Errorf("%s is not in settings.json", node)
	}
	if _, err := k.apply.signer(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) { return k.returnNode(ctx, run, node, true) }, nil
}

// returnNode brings node up to date, alone, as an apply's last steps do (the progress is an
// apply's), then counts it in service again. Anything short of every rollout done leaves it
// out of service. restore (node-restore): its service state is left as it is.
func (k serviceKind) returnNode(ctx context.Context, run *jobs.Run, node string, restore bool) (any, error) {
	res := &applyResult{Changes: []string{}}
	t := &applyTracker{run: run, p: applyProgress{Changes: []string{}, Rollouts: []*applyRollout{}}}
	for _, s := range []string{"check", "dry run", "push"} {
		t.p.Steps = append(t.p.Steps, applyStep{Name: s, State: "waiting"})
	}
	run.SetProgress(t.p)
	step := "check"
	err := k.catchUp(ctx, run, t, node, res, &step, restore)
	stays, again := node+" stays out of service", "return it again"
	if restore {
		stays, again = node+" doesn't have its settings back", "Retry on Nodes"
	}
	t.update(func(pp *applyProgress) {
		pp.Notes = res.Notes
		switch {
		case err == nil && restore:
			pp.Outcome = fmt.Sprintf("done: %s has its settings back", node)
		case err == nil:
			pp.Outcome = fmt.Sprintf("done: %s is up to date and back in service", node)
		case errors.Is(err, fleet.ErrStopped):
			pp.Outcome = "stopped on request: " + stays
		default:
			pp.Outcome = "stopped: " + err.Error()
		}
		if err != nil {
			for i := range pp.Steps {
				if pp.Steps[i].Name == step && pp.Steps[i].State == "running" {
					pp.Steps[i].State, pp.Steps[i].Detail = "failed", err.Error()
					if errors.Is(err, fleet.ErrStopped) {
						pp.Steps[i].State = "stopped"
					}
				}
			}
			pp.Hints = append(pp.Hints, stays+": fix what stopped it, then "+again+".")
		}
	})
	if err != nil || restore {
		res.Applied = err == nil
		return res, err
	}
	if _, err := settings.SetOutOfService(k.path(), node, false); err != nil {
		return res, err
	}
	k.relisted()
	res.Applied = true
	run.Logf("%s: back in service: it is sent changes, and counted while another node is changed", node)
	return res, nil
}

func (k serviceKind) catchUp(ctx context.Context, run *jobs.Run, t *applyTracker, node string, res *applyResult, step *string, restore bool) error {
	a := k.apply
	begin := func(s string) error {
		select {
		case <-run.Stop():
			return fleet.ErrStopped
		default:
		}
		*step = s
		t.step(s, "running", "")
		return nil
	}
	if err := begin("check"); err != nil {
		return err
	}
	s, err := settings.Load(k.path())
	if err != nil {
		return err
	}
	switch {
	case !restore:
		if err := service.CheckIn(s, node); err != nil {
			return err
		}
	case !slices.Contains(s.Nodes, node):
		return fmt.Errorf("%s is not in settings.json", node)
	}
	key, err := a.signer()
	if err != nil {
		return err
	}
	c := a.client()
	c.Logf = run.Logf
	c.Pusher = a.pusher(key, c.HTTP)
	fp := keys.Fingerprint(key)
	run.Logf("signing with the release key %s", fp)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := c.Status(sctx, node)
	cancel()
	switch {
	case !restore:
		if err := service.Ready(node, st, err, fp); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("%s: %w (%v)", node, service.ErrNotAnswering, err)
	case !c.Pusher.Trusts(st):
		return fmt.Errorf("%s doesn't trust your release key", node)
	}
	// Back on its board's settings, a node says no name: its config is the one last pushed to
	// the node pinned here.
	cfg := ""
	if restore {
		if id, err := pins.Open(a.dataDir).Pinned(node); err == nil {
			if p, _ := configs.LoadPushed(a.dataDir, id); p != nil {
				cfg = p.File
			}
		}
	}
	steps, notes, err := service.CatchUp(a.dataDir, a.catalog, node, cfg, st, a.store.ListSums)
	if err != nil {
		return err
	}
	res.Notes = append(res.Notes, notes...)
	for _, n := range notes {
		run.Logf("%s: %s", node, n)
	}
	var reqs []applyReq
	for _, sp := range steps {
		sp.Web.DryRun = true
		r, err := sp.Web.Request(a.dataDir, a.catalog)
		if err != nil {
			return fmt.Errorf("%s: %w", sp.What, err)
		}
		// A node restored from its board's settings serves clients nothing of theirs yet: it
		// may be the only node.
		r.AllowSingle = restore && st.Config != nil && st.Config.Source != "node"
		reqs = append(reqs, applyReq{what: sp.What, req: r})
	}
	t.step("check", "done", fmt.Sprintf("%s answers and trusts the release key; %d to send", node, len(reqs)))
	if len(reqs) == 0 {
		t.step("dry run", "skipped", "nothing to send")
		t.step("push", "skipped", "nothing to send")
	} else if err := a.roll(ctx, run, t, c, reqs, res, begin); err != nil {
		return err
	}
	if !restore {
		return nil
	}
	// Still on its board's settings after: said, and not sent again (the waiter fails it).
	sctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	st, err = c.Status(sctx, node)
	cancel()
	switch {
	case err != nil:
		return fmt.Errorf("%s: %w (%v)", node, service.ErrNotAnswering, err)
	case st.Config != nil && st.Config.Source != "node":
		why := ""
		if len(notes) > 0 {
			why = ": " + strings.Join(notes, "; ")
		}
		return fmt.Errorf("%s still runs on its board's settings after it was sent what it runs%s", node, why)
	}
	return nil
}
