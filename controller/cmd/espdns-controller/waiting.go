package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/filestore"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The boards waiting to be added: one just installed over USB from Nodes (its OK), by its
// chip MAC, its name and its address, in <data>/waiting.json. Not settings.json: rollouts
// count its nodes, and a write there would void the dry run's fingerprint.
//
// A node answers as one when its ID is that MAC and, for one whose node key the install read
// over USB, its node key is that key: then it is matched wherever the controller finds it.
// One without a node key (firmware that doesn't log it, or the line missed) is matched only
// at its fixed address, read there; never by an mDNS answer elsewhere, which anyone can give.
// A board on a fixed address is then added by itself, as + Add node does: the adopt jobs
// (the node key expected passed on, checked again on the node as the job reads it), the dry
// run then the adoption, added to settings.json, run as the user who installed it. A board
// on DHCP is only said found, with its address and its interface's MAC, to reserve in the
// router first: Add it (POST /api/nodes/waiting/{id}/add) adds it, reserved. Without a node
// key, one is said found only as mDNS names it, and so needs that click too. Added, it
// leaves the list (as does one in settings.json, or adopted, meanwhile); a failure stays,
// with its error, until it is tried again or removed.
//
// One of your nodes installed again over USB from here (its chip MAC a node pinned at an
// address settings.json lists) waits as a reinstall: no name or address of its own, it
// keeps them. It is matched by a /status read now, at its listed address, or, with the node
// key read over USB, wherever the controller finds it with that key. Then, as the user who
// installed it: its pinned node key set to the one read over USB (its Wi-Fi password is
// sealed to it), its settings entry moved when it answers at a new address (settings-move,
// with its rules: the old address silent), and node-restore sends it what it runs (service.go).
// Done, it leaves the list; a failure, or a node still on its board's settings after, stays
// failed with why, until Retry: never sent again by itself. Nothing is sent to a node on its
// board's settings with no entry here: /status is anyone's to answer.

// waitingNode is one board waiting.
type waitingNode struct {
	ID      string    `json:"id"` // its chip MAC, aa:bb:cc:dd:ee:ff: the node ID it reports
	Name    string    `json:"name"`
	Board   string    `json:"board,omitempty"`
	Address string    `json:"address"` // a.b.c.d/nn, or "dhcp" (reserved for it in the router)
	Gateway string    `json:"gateway,omitempty"`
	NodeKey string    `json:"node_key,omitempty"` // its public node key, as its console said it over USB
	User    string    `json:"user,omitempty"`     // who installed it: its adoption runs as them
	Since   time.Time `json:"since"`
	State   string    `json:"state"` // waiting, found (on DHCP: Add it), adding, failed
	Error   string    `json:"error,omitempty"`
	// Found and MAC: where a board on DHCP answers, and its interface's MAC (its /status),
	// to reserve that address for in the router.
	Found string `json:"found,omitempty"`
	MAC   string `json:"mac,omitempty"`
	// Reinstall: one of your nodes installed again, sent its settings back (not added);
	// Address is the one settings.json lists it at.
	Reinstall bool `json:"reinstall,omitempty"`
}

const (
	waitingWait   = "waiting"
	waitingFound  = "found"
	waitingAdding = "adding"
	waitingFailed = "failed"
	waitingEvery  = 10 * time.Second
)

func waitingPath(dataDir string) string { return filepath.Join(dataDir, "waiting.json") }

var waitingMu sync.Mutex

func loadWaiting(dataDir string) ([]waitingNode, error) {
	b, err := os.ReadFile(waitingPath(dataDir))
	if errors.Is(err, fs.ErrNotExist) {
		return []waitingNode{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []waitingNode{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", waitingPath(dataDir), err)
	}
	return out, nil
}

// changeWaiting changes the list with f and writes it whole (filestore.WriteFile); nothing
// is written when f fails.
func changeWaiting(dataDir string, f func(ws []waitingNode) ([]waitingNode, error)) error {
	waitingMu.Lock()
	defer waitingMu.Unlock()
	ws, err := loadWaiting(dataDir)
	if err != nil {
		return err
	}
	if ws, err = f(ws); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	return filestore.WriteFile(waitingPath(dataDir), append(b, '\n'))
}

// setWaiting changes the entry id with f, if it is still there.
func setWaiting(dataDir, id string, f func(e *waitingNode)) error {
	return changeWaiting(dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		for i := range ws {
			if ws[i].ID == id {
				f(&ws[i])
			}
		}
		return ws, nil
	})
}

func failWaiting(dataDir, id, why string) {
	setWaiting(dataDir, id, func(e *waitingNode) { e.State, e.Error = waitingFailed, why })
}

// waiter adds the boards waiting once they answer.
type waiter struct {
	adopt  adoptKind
	runner *jobs.Runner
	lookup nodeLookup
}

// Run checks the list every waitingEvery, after restart.
func (w waiter) Run(ctx context.Context) {
	w.restart()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitingEvery):
		}
		w.once(ctx)
	}
}

// restart waits again for one being added when the controller stopped.
func (w waiter) restart() {
	setAll := func(ws []waitingNode) ([]waitingNode, error) {
		for i := range ws {
			if ws[i].State == waitingAdding {
				ws[i].State = waitingWait
			}
		}
		return ws, nil
	}
	changeWaiting(w.adopt.dataDir, setAll)
}

// added says whether node id is one of the nodes already: in settings.json, or adopted
// (pinned) meanwhile.
func (w waiter) added(id string) bool {
	if rs, _ := pins.Open(w.adopt.dataDir).List(); slices.ContainsFunc(rs, func(p pins.Record) bool { return p.ID == id }) {
		return true
	}
	s, err := settings.Load(settings.Path(w.adopt.dataDir))
	return err == nil && slices.ContainsFunc(w.adopt.nodes(), func(n nodes.Node) bool { return strings.EqualFold(n.ID, id) && listed(s, n.Addr) })
}

func (w waiter) once(ctx context.Context) {
	dir := w.adopt.dataDir
	ws, err := loadWaiting(dir)
	if err != nil {
		log.Printf("waiting: %v", err)
		return
	}
	for _, e := range ws {
		if ctx.Err() != nil {
			return
		}
		if !e.Reinstall && e.State != waitingAdding && w.added(e.ID) {
			changeWaiting(dir, func(ws []waitingNode) ([]waitingNode, error) {
				return slices.DeleteFunc(ws, func(x waitingNode) bool { return x.ID == e.ID && x.State != waitingAdding }), nil
			})
			continue
		}
		if e.State != waitingWait && e.State != waitingFound {
			continue
		}
		n, ok := w.find(ctx, e)
		st, read := status(n)
		if !ok || !read {
			if e.State == waitingFound {
				setWaiting(dir, e.ID, func(x *waitingNode) { x.State, x.Found, x.MAC = waitingWait, "", "" })
			}
			continue
		}
		if err := w.check(e, n.Addr, st); err != nil {
			failWaiting(dir, e.ID, err.Error())
			continue
		}
		switch {
		case e.Reinstall:
			w.restore(ctx, e, n.Addr)
		case e.Address == "dhcp":
			setWaiting(dir, e.ID, func(x *waitingNode) { x.State, x.Found, x.MAC = waitingFound, n.Addr, release.MAC(st.Net.MAC) })
		default:
			w.add(ctx, e, n.Addr)
		}
	}
}

// find is the node answering as e (see above).
func (w waiter) find(ctx context.Context, e waitingNode) (nodes.Node, bool) {
	if e.Reinstall {
		return w.reinstalled(ctx, e)
	}
	host := strings.Split(e.Address, "/")[0]
	for _, n := range w.adopt.nodes() {
		if !n.Online || !strings.EqualFold(n.ID, e.ID) {
			continue
		}
		st, _ := status(n)
		switch {
		case e.NodeKey != "":
			if st.NodeKey == e.NodeKey {
				return n, true
			}
		case e.Address == "dhcp" && n.Source == nodes.SourceMDNS, n.Addr == host:
			return n, true
		}
	}
	if e.Address == "dhcp" || w.lookup == nil {
		return nodes.Node{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, nodeLookupTimeout)
	defer cancel()
	n, err := w.lookup(ctx, host)
	st, _ := status(n)
	return n, err == nil && strings.EqualFold(n.ID, e.ID) && (e.NodeKey == "" || st.NodeKey == e.NodeKey)
}

// check says why the node answering as e isn't to be added: it doesn't trust this
// controller's release key.
func (w waiter) check(e waitingNode, addr string, st release.NodeStatus) error {
	k, err := w.adopt.signer()
	if err != nil {
		return err
	}
	if !w.adopt.pusher(k, nil).Trusts(st) {
		return fmt.Errorf("%s answers as %s but doesn't trust your release key", addr, e.ID)
	}
	return nil
}

// addFound adds a board on DHCP said found (Add it), its address reserved, as user: the
// adopt jobs run meanwhile.
func (w waiter) addFound(id, user string) error {
	var e waitingNode
	err := changeWaiting(w.adopt.dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		i := slices.IndexFunc(ws, func(x waitingNode) bool { return x.ID == id })
		switch {
		case i < 0:
			return nil, fs.ErrNotExist
		case ws[i].State != waitingFound:
			return nil, fmt.Errorf("%s isn't found on your network yet", ws[i].Name)
		}
		ws[i].State, ws[i].User = waitingAdding, user
		e = ws[i]
		return ws, nil
	})
	if err == nil {
		go w.add(context.Background(), e, e.Found)
	}
	return err
}

// add runs the adopt jobs for e at addr, as its user: the dry run, then the adoption, added
// to settings.json.
func (w waiter) add(ctx context.Context, e waitingNode, addr string) {
	dir := w.adopt.dataDir
	setWaiting(dir, e.ID, func(x *waitingNode) { x.State, x.Error = waitingAdding, "" })
	p := map[string]any{"node": addr, "node_id": strings.ToLower(e.ID), "config": e.Name + ".json", "static": true, "address": e.Address}
	if e.Address == "dhcp" {
		p["reserved"] = true // Add it: the user reserved it
	} else {
		p["gateway"] = e.Gateway
	}
	if e.NodeKey != "" {
		p["node_key"] = e.NodeKey
	}
	run := func(extra map[string]any) (jobs.Job, error) {
		for k, v := range extra {
			p[k] = v
		}
		return w.run(ctx, e.User, "adopt", p)
	}
	dry, err := run(map[string]any{"dry_run": true})
	if err == nil {
		delete(p, "dry_run")
		_, err = run(map[string]any{"after": dry.ID, "add_to_settings": true})
	}
	if ctx.Err() != nil {
		return // waited for again at the next start
	}
	if err != nil {
		failWaiting(dir, e.ID, err.Error())
		return
	}
	changeWaiting(dir, func(ws []waitingNode) ([]waitingNode, error) {
		return slices.DeleteFunc(ws, func(x waitingNode) bool { return x.ID == e.ID }), nil
	})
}

// run runs a job of kind with params as user ("install" for none) to its end: an error
// unless it is done.
func (w waiter) run(ctx context.Context, user, kind string, params map[string]any) (jobs.Job, error) {
	b, _ := json.Marshal(params)
	j, err := w.runner.Start(kind, cmp.Or(user, "install"), b)
	if err != nil {
		return j, err
	}
	for !j.State.Ended() {
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		j, _ = w.runner.Get(j.ID)
	}
	if j.State != jobs.Done {
		return j, errors.New(cmp.Or(j.Error, kind+" "+string(j.State)))
	}
	return j, nil
}

// reinstalled is the node answering as reinstall e, read now: at its listed address, or,
// with its node key, wherever the controller found its ID.
func (w waiter) reinstalled(ctx context.Context, e waitingNode) (nodes.Node, bool) {
	if w.lookup == nil {
		return nodes.Node{}, false
	}
	addrs := []string{e.Address}
	for _, n := range w.adopt.nodes() {
		if e.NodeKey != "" && strings.EqualFold(n.ID, e.ID) && !slices.Contains(addrs, n.Addr) {
			addrs = append(addrs, n.Addr)
		}
	}
	for _, a := range addrs {
		lctx, cancel := context.WithTimeout(ctx, nodeLookupTimeout)
		n, err := w.lookup(lctx, a)
		cancel()
		if st, ok := status(n); err == nil && ok && strings.EqualFold(n.ID, e.ID) && (e.NodeKey == "" || st.NodeKey == e.NodeKey) {
			return n, true
		}
	}
	return nodes.Node{}, false
}

// restore sends reinstall e, answering at addr, its settings back, as its user: its node key
// pinned anew (the one read over USB), its entry moved to addr if it answers there
// (settings-move), then node-restore. Done, it leaves the list; else it fails, with why.
func (w waiter) restore(ctx context.Context, e waitingNode, addr string) {
	dir := w.adopt.dataDir
	setWaiting(dir, e.ID, func(x *waitingNode) { x.State, x.Error = waitingAdding, "" })
	err := func() error {
		if e.NodeKey != "" {
			if err := pins.Open(dir).SetNodeKey(e.ID, e.NodeKey); err != nil {
				return err
			}
		}
		if addr != e.Address {
			if _, err := w.run(ctx, e.User, "settings-move", map[string]any{"node": addr, "from": e.Address}); err != nil {
				return err
			}
		}
		_, err := w.run(ctx, e.User, "node-restore", map[string]any{"node": addr})
		return err
	}()
	if ctx.Err() != nil {
		return // waited for again at the next start
	}
	if err != nil {
		failWaiting(dir, e.ID, err.Error())
		return
	}
	changeWaiting(dir, func(ws []waitingNode) ([]waitingNode, error) {
		return slices.DeleteFunc(ws, func(x waitingNode) bool { return x.ID == e.ID }), nil
	})
}
