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
	"github.com/skitzo2000/espdns/controller/internal/release"
)

// The boards waiting to be added: one just installed over USB from Nodes (its OK), by its
// chip MAC, its name and its address, in <data>/waiting.json. Once a node answers as it
// (its ID that MAC, trusting this controller's release key, with the node key the install
// read over USB when it read one), the controller adds it as + Add node does: the adopt
// jobs, the dry run then the adoption, added to settings.json. Added, it leaves the list;
// a failure stays, with its error, until it is tried again or removed. Not settings.json:
// rollouts count its nodes, and a write there would void the dry run's fingerprint.

// waitingNode is one board waiting.
type waitingNode struct {
	ID      string    `json:"id"` // its chip MAC, aa:bb:cc:dd:ee:ff: the node ID it reports
	Name    string    `json:"name"`
	Board   string    `json:"board,omitempty"`
	Address string    `json:"address"` // a.b.c.d/nn, or "dhcp" (reserved for it in the router)
	Gateway string    `json:"gateway,omitempty"`
	NodeKey string    `json:"node_key,omitempty"` // its public node key, as its console said it over USB
	Since   time.Time `json:"since"`
	State   string    `json:"state"` // waiting, adding, failed
	Error   string    `json:"error,omitempty"`
}

const (
	waitingWait   = "waiting"
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

// setWaiting sets the state and error of the entry id, if it is still there.
func setWaiting(dataDir, id, state, why string) error {
	return changeWaiting(dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		for i := range ws {
			if ws[i].ID == id {
				ws[i].State, ws[i].Error = state, why
			}
		}
		return ws, nil
	})
}

// waiter adds the boards waiting once they answer.
type waiter struct {
	adopt  adoptKind
	runner *jobs.Runner
	lookup nodeLookup
}

// Run checks the list every waitingEvery. One being added when the controller stopped is
// waited for again.
func (w waiter) Run(ctx context.Context) {
	changeWaiting(w.adopt.dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		for i := range ws {
			if ws[i].State == waitingAdding {
				ws[i].State = waitingWait
			}
		}
		return ws, nil
	})
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(waitingEvery):
		}
		w.once(ctx)
	}
}

func (w waiter) once(ctx context.Context) {
	ws, err := loadWaiting(w.adopt.dataDir)
	if err != nil {
		log.Printf("waiting: %v", err)
		return
	}
	for _, e := range ws {
		if e.State != waitingWait || ctx.Err() != nil {
			continue
		}
		n, ok := w.find(ctx, e)
		st, read := status(n)
		if !ok || !read {
			continue
		}
		if err := w.check(e, n.Addr, st); err != nil {
			setWaiting(w.adopt.dataDir, e.ID, waitingFailed, err.Error())
			continue
		}
		w.add(ctx, e, n)
	}
}

// find is the node answering as e: by its ID among the nodes known, else, on a fixed
// address, read there.
func (w waiter) find(ctx context.Context, e waitingNode) (nodes.Node, bool) {
	for _, n := range w.adopt.nodes() {
		if n.Online && strings.EqualFold(n.ID, e.ID) {
			return n, true
		}
	}
	if e.Address == "dhcp" || w.lookup == nil {
		return nodes.Node{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, nodeLookupTimeout)
	defer cancel()
	n, err := w.lookup(ctx, strings.Split(e.Address, "/")[0])
	return n, err == nil && strings.EqualFold(n.ID, e.ID)
}

// check says why the node answering as e isn't the board installed: not trusting this
// controller's release key, or with another node key than the install read.
func (w waiter) check(e waitingNode, addr string, st release.NodeStatus) error {
	k, err := w.adopt.signer()
	if err != nil {
		return err
	}
	if !w.adopt.pusher(k, nil).Trusts(st) {
		return fmt.Errorf("%s answers as %s but doesn't trust your release key", addr, e.ID)
	}
	if e.NodeKey != "" && st.NodeKey != e.NodeKey {
		return fmt.Errorf("%s answers as %s with another node key than the board installed", addr, e.ID)
	}
	return nil
}

// add runs the adopt jobs for e at n: the dry run, then the adoption, added to settings.json.
func (w waiter) add(ctx context.Context, e waitingNode, n nodes.Node) {
	dir := w.adopt.dataDir
	setWaiting(dir, e.ID, waitingAdding, "")
	p := map[string]any{"node": n.Addr, "node_id": strings.ToLower(e.ID), "config": e.Name + ".json", "static": true, "address": e.Address}
	if e.Address == "dhcp" {
		p["reserved"] = true
	} else {
		p["gateway"] = e.Gateway
	}
	run := func(extra map[string]any) (jobs.Job, error) {
		for k, v := range extra {
			p[k] = v
		}
		b, _ := json.Marshal(p)
		j, err := w.runner.Start("adopt", "install", b)
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
			return j, errors.New(cmp.Or(j.Error, "the adoption "+string(j.State)))
		}
		return j, nil
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
		setWaiting(dir, e.ID, waitingFailed, err.Error())
		return
	}
	changeWaiting(dir, func(ws []waitingNode) ([]waitingNode, error) {
		return slices.DeleteFunc(ws, func(x waitingNode) bool { return x.ID == e.ID }), nil
	})
}
