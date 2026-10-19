package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/adoption"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// Adoption (the Adopt page): a node the controller found and that isn't adopted, given a
// config from <data>/configs and its address, as make fleet-adopt does it, through the same
// code: the page's request becomes the request the CLI's flags make (internal/adoption,
// Web), built and run by the same functions (fleet.AdoptNode: the address checked free, the
// config checked for the node, a word on the zone primary when the node's address is in
// none of the networks it allows, the config pushed, the node confirmed on its address), then the config it runs recorded and, if asked, its address
// added to settings.json.
//
//	GET  /api/adopt              the nodes found (unadopted ones to adopt; adopted ones not in settings.json), the configs, the zone primary
//	POST /api/adopt/preview      {Web}: the address it would get, the config checked for the node, the zones (read-only, no job)
//	POST /api/jobs               {"kind": "adopt", "params": {Web, "dry_run": true}}: the dry run
//	POST /api/jobs               {"kind": "adopt", "params": {Web, "after": "<the dry run's job ID>", ...}}: the adoption
//	POST /api/jobs               {"kind": "settings-add", "params": {"node": "<address>"}}: an adopted node added to settings.json
//
// An adoption signs a config for a node found over mDNS, not one in settings.json (that is
// what adopting is): so only a node that says it isn't adopted, under an ID no other node
// the controller knows has, as the dry run found it (the same ID, the same files), within
// 15 minutes of it; one adoption per dry run. Nothing is changed on the zone primary: it is
// set up once to allow zone transfers from the nodes' networks, and when the node's address
// is in none of those settings.json names ("primary" "allows"), the dry run and the
// adoption say so, once, and go on.

type adoptKind struct {
	actions
	catalog string
	job     func(id string) (jobs.Job, bool)
	secret  []byte
	nodes   func() []nodes.Node
	used    *usedRuns
	// adjust, in tests only, shortens the adoption's waits after it is built.
	adjust func(*fleet.Adopt)
}

// adoptResult is an adopt job's result.
type adoptResult struct {
	Node            string   `json:"node"`
	NodeID          string   `json:"node_id"`
	Config          string   `json:"config"`
	DryRun          bool     `json:"dry_run"`
	Fingerprint     string   `json:"fingerprint"`
	Address         string   `json:"address,omitempty"` // where it is confirmed
	Seq             uint64   `json:"seq,omitempty"`
	Recorded        bool     `json:"recorded,omitempty"`
	AddedToSettings bool     `json:"added_to_settings,omitempty"`
	InSettings      bool     `json:"in_settings,omitempty"`
	Servers         []string `json:"servers,omitempty"`
}

// adoptStep is one step of the adoption, for the page.
type adoptStep struct {
	N      int    `json:"n"`
	Title  string `json:"title"`
	State  string `json:"state"` // waiting, running, done, failed, skipped
	Detail string `json:"detail,omitempty"`
}

// adoptProgress is an adopt job's progress.
type adoptProgress struct {
	DryRun  bool         `json:"dry_run"`
	Node    string       `json:"node"`
	NodeID  string       `json:"node_id"`
	Config  string       `json:"config"`
	Address string       `json:"address,omitempty"`
	Steps   []adoptStep  `json:"steps"`
	Primary primaryState `json:"primary"`
	Outcome string       `json:"outcome,omitempty"`
	Hints   []string     `json:"hints,omitempty"`
}

// primaryState is step 4 as the page shows it: the primary the config copies its zones
// from, the zones, and what to set up on the primary once, if anything (internal/primary,
// Advice).
type primaryState struct {
	Primary string   `json:"primary,omitempty"`
	Zones   []string `json:"zones,omitempty"`
	Advice  string   `json:"advice,omitempty"`
}

var stepTitles = map[int]string{2: "Find the node", 3: "The address and the config", 4: "The zone primary",
	5: "Push the config", 6: "Confirm it on its address", 7: "The clients' DNS servers"}

type adoptTracker struct {
	mu  sync.Mutex
	run *jobs.Run
	p   adoptProgress
}

func (t *adoptTracker) update(f func(p *adoptProgress)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f(&t.p)
	t.run.SetProgress(t.p)
}

func (t *adoptTracker) step(n int, what string) {
	t.update(func(p *adoptProgress) {
		for i := range p.Steps {
			s := &p.Steps[i]
			switch {
			case s.N < n && (s.State == "running" || s.State == "waiting"):
				s.State = "done"
			case s.N == n:
				s.State, s.Detail = "running", what
			}
		}
	})
}

// fingerprint keys what the dry run checked: the request, the config's bytes, the settings.
func (p adoptKind) fingerprint(w adoption.Web, text, set []byte) string {
	m := hmac.New(sha256.New, p.secret)
	m.Write([]byte(w.Key() + "\x00"))
	m.Write(text)
	m.Write([]byte{0})
	m.Write(set)
	return hex.EncodeToString(m.Sum(nil))
}

// inputs are the config's and the settings' bytes, read once.
func (p adoptKind) inputs(w adoption.Web) (text, set []byte, err error) {
	f, err := configs.Read(p.dataDir, w.Config)
	if err != nil {
		return nil, nil, err
	}
	if f.Error != "" {
		return nil, nil, fmt.Errorf("%s: %s", w.Config, f.Error)
	}
	set, err = os.ReadFile(settings.Path(p.dataDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return f.Text, set, nil
}

// candidate checks the node is one the page may adopt: found, saying it isn't adopted, as
// the ID the page showed, and an ID no other node the controller knows has.
func (p adoptKind) candidate(w adoption.Web) error {
	all := p.nodes()
	i := slices.IndexFunc(all, func(n nodes.Node) bool { return n.Addr == w.Node })
	if i < 0 {
		return fmt.Errorf("%s is not a node the controller has found (mDNS or settings.json)", w.Node)
	}
	st, ok := status(all[i])
	switch {
	case !ok:
		return fmt.Errorf("%s: no /status read yet", w.Node)
	case !strings.EqualFold(st.NodeID, w.NodeID):
		return fmt.Errorf("%s is node %s now, not %s: look again", w.Node, st.NodeID, w.NodeID)
	case st.Config == nil:
		return fmt.Errorf("%s: its firmware takes no node configs: update it first", w.Node)
	case p.otherKey(st):
		// Adopting it signs its config with this controller's key, which it would refuse.
		return fmt.Errorf("%s: %s", w.Node, otherKeyRefusal)
	}
	if why := adoptedBefore(st); why != "" {
		return fmt.Errorf("%s: %s", w.Node, why)
	}
	// An ID a config was pushed to before is a node adopted before: one that answers as it,
	// saying it isn't adopted, could be it wiped, or something else claiming its ID (mDNS
	// and /status are unauthenticated) to get a config signed for it.
	if pr, _ := configs.LoadPushed(p.dataDir, strings.ToLower(st.NodeID)); pr != nil {
		return fmt.Errorf("%s says it is node %s, which was given %s at %s (%s): not adopted from here; if it is that node, "+
			"wiped, adopt it with espdns adopt (its dry run first; docs/reference/cli.md#adopt)", w.Node, st.NodeID, pr.File, pr.Host,
			pr.Time.Format("2006-01-02"))
	}
	for _, n := range all {
		if n.Addr != w.Node && strings.EqualFold(n.ID, w.NodeID) {
			return fmt.Errorf("%s says it is node %s, which the controller knows at %s: not adopted (a release signed for it "+
				"would be good on that node too)", w.Node, w.NodeID, n.Addr)
		}
	}
	return nil
}

// adoptedBefore says why a node whose /status is st is no node for the Adopt page: one
// that runs a pushed config, or one that took a config before and runs its board's settings
// now (a node in service whose config was refused or failed its trial looks unadopted, and
// adopting it here could give it another config and address). "" if it never took one.
func adoptedBefore(st release.NodeStatus) string {
	switch {
	case st.Config == nil:
		return ""
	case st.Config.Source == "node":
		return "adopted already (it runs a pushed config): change its address or settings on its node page"
	case st.Seq["config"] > 0:
		why := ""
		if st.Config.Error != "" {
			why = " (" + st.Config.Error + ")"
		}
		return fmt.Sprintf("it took a node config before (config seq %d) and runs its board's settings now%s: it may be a "+
			"node in service that fell back, so it isn't adopted from here; adopt it again with espdns adopt (its dry run "+
			"first; docs/reference/cli.md#adopt), once you know which node it is", st.Seq["config"], why)
	}
	return ""
}

func (p adoptKind) kind(raw json.RawMessage) (jobs.Func, error) {
	w, err := adoption.ParseWeb(raw)
	if err != nil {
		return nil, err
	}
	_, err = w.Request(p.dataDir, p.catalog)
	if err != nil {
		return nil, err
	}
	if err := p.candidate(w); err != nil {
		return nil, err
	}
	text, set, err := p.inputs(w)
	if err != nil {
		return nil, err
	}
	if w.DryRun {
		if w.After != "" || w.AddToSettings {
			return nil, errors.New(`params: "after" and "add_to_settings" are for the adoption after a dry run`)
		}
	} else {
		if _, err := p.signer(); err != nil {
			return nil, err
		}
		if err := p.dryRunFor(w, text, set); err != nil {
			return nil, err
		}
		if !p.used.claim(w.After) {
			return nil, fmt.Errorf("the dry run %s was already followed by an adoption: run the dry run again", w.After)
		}
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) { return p.run(ctx, run, w) }, nil
}

func zonesOf(c *nodecfg.Config) []string {
	if c == nil || c.Secondary == nil || c.Secondary.Zones == nil {
		return nil
	}
	return *c.Secondary.Zones
}

// dryRunFor checks w.After is an adopt dry run that passed, not long ago, of this request
// with these files.
func (p adoptKind) dryRunFor(w adoption.Web, text, set []byte) error {
	if w.After == "" {
		return errors.New(`an adoption needs a dry run first: run it with "dry_run": true, then adopt with "after": its job ID`)
	}
	j, ok := p.job(w.After)
	var res adoptResult
	switch {
	case !ok || j.Kind != "adopt":
		return fmt.Errorf("no adopt dry run %s", w.After)
	case j.State != jobs.Done:
		return fmt.Errorf("the dry run %s didn't pass (%s): fix what it says and run it again", w.After, j.State)
	case json.Unmarshal(j.Result, &res) != nil || !res.DryRun:
		return fmt.Errorf("%s is not a dry run", w.After)
	case time.Since(j.Ended) > dryRunValid:
		return fmt.Errorf("the dry run %s is over %v old: run it again", w.After, dryRunValid)
	case !hmac.Equal([]byte(res.Fingerprint), []byte(p.fingerprint(w, text, set))):
		return fmt.Errorf("the adoption isn't the one the dry run %s checked: the node, the address, the config or "+
			"settings.json changed since (or the controller restarted): run the dry run again", w.After)
	}
	return nil
}

func (p adoptKind) run(ctx context.Context, run *jobs.Run, w adoption.Web) (any, error) {
	// Everything again, as it is now: the node, the files, the dry run, the key.
	req, err := w.Request(p.dataDir, p.catalog)
	if err != nil {
		return nil, err
	}
	if err := p.candidate(w); err != nil {
		return nil, err
	}
	text, set, err := p.inputs(w)
	if err != nil {
		return nil, err
	}
	res := adoptResult{Node: w.Node, NodeID: w.NodeID, Config: w.Config, DryRun: w.DryRun, Fingerprint: p.fingerprint(w, text, set)}
	if !w.DryRun {
		if err := p.dryRunFor(w, text, set); err != nil {
			return nil, err
		}
	}
	req.ConfigText = text
	c := p.client()
	c.Logf = run.Logf
	if k, err := p.signer(); err == nil {
		c.Pusher = p.pusher(k, c.HTTP)
		run.Logf("signing with the release key %s", keys.Fingerprint(k))
	} else if !w.DryRun {
		return nil, err
	} else {
		run.Logf("no release key (%v): a dry run goes on without one", err)
	}
	t := &adoptTracker{run: run, p: adoptProgress{DryRun: w.DryRun, Node: w.Node, NodeID: w.NodeID, Config: w.Config}}
	for n := 2; n <= 7; n++ {
		t.p.Steps = append(t.p.Steps, adoptStep{N: n, Title: stepTitles[n], State: "waiting"})
	}
	run.SetProgress(t.p)

	b, err := adoption.Build(ctx, c, req, run.Logf)
	if err != nil {
		t.update(func(pp *adoptProgress) { pp.Outcome = "not started: " + err.Error() })
		return res, err
	}
	t.update(func(pp *adoptProgress) {
		pp.Primary = primaryState{Primary: b.Primary, Zones: b.Zones}
	})
	// The node as step 2 reads it now, not as the registry last polled it: one adopted (or
	// one that took a config) since is not adopted from here, before anything changes.
	check := b.Adopt.Check
	b.Adopt.Check = func(cfg *nodecfg.Config, st release.NodeStatus) error {
		if why := adoptedBefore(st); why != "" {
			return errors.New(why)
		}
		return check(cfg, st)
	}
	// Build read settings.json again: still the bytes the dry run checked.
	if !w.DryRun {
		if text2, set2, err := p.inputs(w); err != nil || string(text2) != string(text) || string(set2) != string(set) {
			err = errors.New("the config or settings.json changed while the adoption started: run the dry run again")
			t.update(func(pp *adoptProgress) { pp.Outcome = "not started: " + err.Error() })
			return res, err
		}
	}
	b.Adopt.Step = t.step
	b.Adopt.Advice = func(a string) { t.update(func(pp *adoptProgress) { pp.Primary.Advice = a }) }
	b.Adopt.Resolved = func(addr string) {
		t.update(func(pp *adoptProgress) {
			pp.Address = addr
			for i := range pp.Steps {
				if pp.Steps[i].N == 3 {
					pp.Steps[i].Detail = "the node is adopted on " + addr + "; the config checked for it with that address"
				}
			}
		})
	}
	if p.adjust != nil {
		p.adjust(&b.Adopt)
	}
	r, err := adoption.Run(ctx, c, b, "controller", run.Logf)
	res.Address, res.Seq, res.Recorded, res.AddedToSettings, res.InSettings, res.Servers =
		r.Addr, r.Seq, r.Recorded, r.AddedToSettings, r.InSettings, r.Servers
	p.finish(t, w, b, r, err)
	if err == nil && w.DryRun {
		run.Logf("dry run passed: nothing was changed")
	}
	return res, err
}

// finish sets each step's last state, the outcome and what to do next.
func (p adoptKind) finish(t *adoptTracker, w adoption.Web, b adoption.Built, r adoption.Result, err error) {
	failed := 0
	if err != nil {
		if n, rest, ok := strings.Cut(err.Error(), ". "); ok && len(n) == 1 && rest != "" {
			failed, _ = strconv.Atoi(n)
		}
	}
	t.update(func(pp *adoptProgress) {
		for i := range pp.Steps {
			s := &pp.Steps[i]
			switch {
			case err == nil && w.DryRun && s.N >= 5:
				s.State = "skipped"
				if s.N == 5 {
					s.State, s.Detail = "done", "checked: the config would be pushed now (dry run: not pushed)"
				}
			case err == nil:
				if s.State != "skipped" {
					s.State = "done"
				}
			case s.N == failed || failed == 0 && s.State == "running":
				s.State, s.Detail = "failed", err.Error()
			case s.State == "running":
				s.State = "done"
			}
		}
		var hints []string
		switch {
		case err == nil && w.DryRun:
			pp.Outcome = "dry run passed: every step checked; nothing was changed"
			if pp.Primary.Advice != "" {
				hints = append(hints, pp.Primary.Advice)
			}
		case err == nil:
			pp.Outcome = fmt.Sprintf("adopted: node %s on %s, config seq %d", r.NodeID, r.Addr, r.Seq)
			if len(r.Servers) >= 2 {
				hints = append(hints, "Hand out "+strings.Join(r.Servers, ", ")+" as the clients' DNS servers (the router's "+
					"DHCP DNS setting, on the networks that use DHCP).")
			} else {
				hints = append(hints, "Adopt a second node, then hand out both as the clients' DNS servers.")
			}
			if !r.InSettings {
				hints = append(hints, r.Addr+" is not in settings.json: add it (below) so rollouts count it and change it.")
			}
			if pp.Primary.Advice != "" {
				hints = append(hints, pp.Primary.Advice)
			}
		case errors.Is(err, fleet.ErrStopped):
			pp.Outcome = "stopped on request"
		default:
			pp.Outcome = "stopped: " + err.Error()
			if failed >= 6 {
				hints = append(hints, "The node took the config but wasn't confirmed on its address: on trial, it goes back to "+
					"its previous config within its trial window. Check it on the Nodes page before trying again.")
			} else if failed > 0 && failed <= 4 {
				hints = append(hints, "Nothing was pushed to the node.")
			}
		}
		pp.Hints = hints
	})
}

// ---- the page's reads ------------------------------------------------------------------

type adoptNode struct {
	Host     string   `json:"host"`
	ID       string   `json:"id,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Source   string   `json:"source"` // nodes.SourceSettings, SourceMDNS or SourceLookup
	Online   bool     `json:"online"`
	Read     bool     `json:"read"`           // its /status is read: only then is it known whether it is adopted
	Name     string   `json:"name,omitempty"` // its config's name, as it runs it
	Board    string   `json:"board,omitempty"`
	Image    string   `json:"image,omitempty"`
	Version  string   `json:"version,omitempty"`
	Net      string   `json:"net,omitempty"`
	MAC      string   `json:"mac,omitempty"` // its interface's (release.MAC): for a DHCP reservation, or a static address
	State    string   `json:"state,omitempty"`
	Adopted  bool     `json:"adopted"`
	Listed   bool     `json:"listed"`            // in settings.json
	Address  string   `json:"address,omitempty"` // the static address it runs on, with its prefix
	Gateway  string   `json:"gateway,omitempty"`
	From     string   `json:"from,omitempty"` // where its address comes from
	Configs  bool     `json:"configs"`        // its firmware takes node configs
	Refusals []string `json:"refusals,omitempty"`
	Config   string   `json:"config,omitempty"` // a config file that names it (its name or address)
	// KeysSource is its /status keys_source (firmware/main/keys_node.h): "none" or "corrupt" is
	// setup mode, which takes nothing over the network: its keys are written over USB
	// (Install over USB, Write keys only). "" for firmware from before the keys partition.
	KeysSource string `json:"keys_source,omitempty"`
}

type adoptConfig struct {
	Name    string   `json:"name"`
	Config  string   `json:"config_name,omitempty"`
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	Primary string   `json:"primary,omitempty"`
	Zones   []string `json:"zones,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func (p adoptKind) routes(mux *routes) {
	mux.HandleFunc("GET /api/adopt", needLogin("adoption needs", p.list))
	mux.HandleFunc("POST /api/adopt/preview", needLogin("adoption needs", p.preview))
}

func (p adoptKind) list(w http.ResponseWriter, r *http.Request) {
	s, err := settings.Load(settings.Path(p.dataDir))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	files, _ := configs.List(p.dataDir)
	all := p.nodes()
	out := []adoptNode{}
	for _, n := range all {
		out = append(out, p.describe(n, all, s, files))
	}
	cs := []adoptConfig{}
	for _, f := range files {
		c := adoptConfig{Name: f.Name, Error: f.Error}
		if f.Config != nil {
			c.Config = f.Config.Name
			if f.Config.Network != nil {
				c.Address, c.Gateway = f.Config.Network.Address, f.Config.Network.Gateway
			}
			if sec := f.Config.Secondary; sec != nil {
				c.Primary, c.Zones = sec.Primary, zonesOf(f.Config)
			}
		}
		cs = append(cs, c)
	}
	_, kerr := p.signer()
	out2 := map[string]any{"nodes": out, "configs": cs, "primary": primaryFacts(s), "key": kerr == nil, "no_dhcp": append([]string{}, s.NoDHCP...),
		"dry_run_valid_s": dryRunValid.Seconds(), "dns_peers": s.DNSPeers}
	if kerr != nil {
		out2["key_error"] = kerr.Error()
	}
	writeJSON(w, out2)
}

// describe is node n as the Adopt page (and Add node: addnode.go) shows it: what it is, from
// its last /status, whether it is adopted and listed, and why it can't be adopted from here
// (Refusals). all is every node the controller knows, s settings.json, files the configs.
func (p adoptKind) describe(n nodes.Node, all []nodes.Node, s settings.Settings, files []configs.File) adoptNode {
	an := adoptNode{Host: n.Addr, ID: n.ID, Hostname: n.Hostname, Source: n.Source, Online: n.Online,
		Listed: slices.Contains(s.Nodes, n.Addr)}
	st, ok := status(n)
	an.Read = ok
	if ok {
		an.Board, an.Image, an.Version, an.Net, an.MAC = st.Board, st.Image, st.Version, st.Net.Kind, release.MAC(st.Net.MAC)
		an.KeysSource = st.KeysSource
		if st.Health != nil {
			an.State = st.Health.State
		}
		if st.Config != nil {
			an.Name, an.Configs = st.Config.Name, true
			an.Adopted = st.Config.Source == "node"
			an.From = st.Config.AddressFrom
			if st.Config.Address == "static" {
				an.Address, an.Gateway = st.Config.IP, st.Config.Gateway
			} else {
				an.Address = st.Config.Address
			}
		}
		for _, f := range files {
			if configs.Matches(f.Config, n.Addr, st) {
				an.Config = f.Name
				break
			}
		}
	}
	switch {
	case !ok:
		an.Refusals = append(an.Refusals, "no /status read yet")
	case an.KeysSource == "none" || an.KeysSource == "corrupt":
		an.Refusals = append(an.Refusals, "it has no keys (setup mode): write them over USB first (Install over USB, Write keys only)")
	case !an.Configs:
		an.Refusals = append(an.Refusals, "its firmware takes no node configs: update it first")
	case !an.Listed && p.otherKey(st):
		an.Refusals = append(an.Refusals, otherKeyRefusal)
	case !an.Adopted && adoptedBefore(st) != "":
		an.Refusals = append(an.Refusals, adoptedBefore(st))
	case !an.Adopted:
		if pr, _ := configs.LoadPushed(p.dataDir, strings.ToLower(st.NodeID)); pr != nil {
			an.Refusals = append(an.Refusals, fmt.Sprintf("node %s was given %s at %s before: adopt it again with make "+
				"fleet-adopt, if it is that node wiped", st.NodeID, pr.File, pr.Host))
		}
	}
	for _, o := range all {
		if o.Addr != n.Addr && o.ID != "" && strings.EqualFold(o.ID, n.ID) {
			// The same node at a new address: the entry settings.json lists it at (n not listed
			// itself, the old address silent) is a move, not a dead end -- view() marks it (Move/Next), so nothing
			// refuses here. Any other same-ID node keeps the plain refusal.
			if !an.Listed && !o.Online && slices.Contains(s.Nodes, o.Addr) {
				continue
			}
			an.Refusals = append(an.Refusals, "another node the controller knows ("+o.Addr+") has the same ID")
		}
	}
	return an
}

// otherKeyRefusal is why a node that trusts another release key isn't added, or adopted.
const otherKeyRefusal = "it trusts another controller's release key: install it again with Install over USB, which writes your keys"

// otherKey says whether the node whose /status is st trusts another release key than this
// controller's; false when that isn't known (no release key here, firmware with no keys).
func (p adoptKind) otherKey(st release.NodeStatus) bool {
	k, err := p.signer()
	return err == nil && len(st.Keys) > 0 && !p.pusher(k, nil).Trusts(st)
}

// adoptPreview is what the page shows before the dry run.
type adoptPreview struct {
	Address string         `json:"address,omitempty"` // with its prefix, or "dhcp"
	Gateway string         `json:"gateway,omitempty"`
	How     string         `json:"how,omitempty"` // kept (from where) or new
	From    string         `json:"from,omitempty"`
	Check   configs.Result `json:"check"`
	Refusal string         `json:"refusal,omitempty"` // why it can't be adopted so
	Notes   []string       `json:"notes,omitempty"`
	Primary string         `json:"primary,omitempty"`
	Zones   []string       `json:"zones,omitempty"`
	// Advice is what to set up once on the zone primary for the node's address, when it is
	// in none of the networks the primary allows (internal/primary); "" otherwise.
	Advice string `json:"advice,omitempty"`
}

// preview reads the node's /status now and works out the address and the config check as
// the adoption would (fleet.Adopt.Resolve, adoption.CheckConfig), changing nothing.
func (p adoptKind) preview(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := body(w, r, &raw); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	web, err := adoption.ParseWeb(raw)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	web.DryRun, web.After, web.AddToSettings = true, "", false
	req, err := web.Request(p.dataDir, p.catalog)
	if err == nil {
		err = p.candidate(web)
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	text, _, err := p.inputs(web)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := nodecfg.Parse(text)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, err := p.client().Status(ctx, web.Node)
	if err != nil {
		httpErr(w, http.StatusBadGateway, fmt.Errorf("%s: %v", web.Node, err))
		return
	}
	var out adoptPreview
	out.Primary, out.Zones = "", zonesOf(cfg)
	if cfg.Secondary != nil {
		out.Primary = cfg.Secondary.Primary
	}
	// The address as Build and step 3 work it out.
	addr, gw := req.Address, req.Gateway
	out.From = "the address it runs on"
	switch {
	case addr != "":
		out.From = "chosen here"
	case cfg.Network != nil:
		addr, out.From = cfg.Network.Address, "the config's network"
	}
	if gw == "" && cfg.Network != nil {
		gw = cfg.Network.Gateway
	}
	a := fleet.Adopt{DHCP: addr == "dhcp"}
	if addr != "" && !a.DHCP {
		a.Address, _ = netip.ParsePrefix(addr)
	}
	if gw != "" && !a.DHCP {
		a.Gateway, _ = netip.ParseAddr(gw)
	}
	at := adoption.HostOnly(web.Node)
	refuse := func(e string) {
		if out.Refusal == "" {
			out.Refusal = e
		}
	}
	if a.DHCP {
		out.Address, out.How = "dhcp", "dhcp"
		cfg.Network = &nodecfg.Network{Address: "dhcp"}
		s, serr := settings.Load(req.SettingsPath())
		h, herr := netip.ParseAddr(at)
		n, none := netip.Prefix{}, false
		if serr == nil && herr == nil {
			n, none = s.NoDHCPAt(h)
		}
		if serr != nil {
			refuse(serr.Error())
		} else if none {
			refuse(fmt.Sprintf("%s has no DHCP server (settings.json, no_dhcp): a node there gets a static address", n))
		} else if !web.Reserved {
			out.Notes = append(out.Notes, fmt.Sprintf("Reserve %s for MAC %s in that network's DHCP server first, then tick that it is reserved.", at, st.Net.MAC))
		}
	} else if err := a.Resolve(st); err != nil {
		refuse(err.Error())
	} else {
		out.Address, out.Gateway, out.How = a.Address.String(), a.Gateway.String(), a.AddressHow(st)
		cfg.Network = &nodecfg.Network{Address: a.Address.String(), Gateway: a.Gateway.String()}
		at = a.Address.Addr().String()
		if a.Address.Addr().String() != adoption.HostOnly(web.Node) {
			out.Notes = append(out.Notes, "A new address: the dry run checks nothing else answers there; the node moves there "+
				"with a reboot and keeps it only once it is reached there.")
		}
	}
	if f, err := configs.Read(p.dataDir, web.Config); err == nil && f.Config != nil && out.Address != "" {
		switch n := f.Config.Network; {
		case n == nil:
			out.Notes = append(out.Notes, fmt.Sprintf("%s has no network: the node gets %s in the config pushed, but a later push "+
				"of the file as it is would be refused (it would drop the node off its address): add the network to the file.",
				web.Config, out.Address))
		case n.Address != out.Address:
			out.Notes = append(out.Notes, fmt.Sprintf("%s says %s: the node gets %s in the config pushed; change the file to it, "+
				"or a later push of the file moves the node back.", web.Config, n.Address, out.Address))
		}
	}
	out.Check = configs.Check{Name: web.Config, Text: mustPayload(cfg), Catalog: p.catalog, Host: web.Node, Status: &st,
		DataDir: p.dataDir}.Run()
	if !out.Check.OK {
		refuse("the config doesn't pass its checks for this node: " +
			adoption.CheckConfig(web.Config, cfg, web.Node, st, p.catalog, p.dataDir).Error())
	}
	if len(out.Zones) > 0 {
		if s, err := settings.Load(req.SettingsPath()); err != nil {
			refuse(err.Error())
		} else {
			out.Advice = s.ZonePrimary().Uncovered(at, a.Address.Bits())
		}
	}
	writeJSON(w, out)
}

func mustPayload(c *nodecfg.Config) []byte {
	b, err := c.Payload()
	if err != nil {
		b, _ = json.Marshal(c)
	}
	return b
}

// settingsAdd is the job kind "settings-add": a node the controller found, adopted (it
// runs a pushed config), added to settings.json, written whole.
func (p adoptKind) settingsAdd(raw json.RawMessage) (jobs.Func, error) {
	var np struct {
		Node string `json:"node"`
	}
	if err := decodeStrict(raw, &np); err != nil {
		return nil, err
	}
	check := func() error {
		all := p.nodes()
		i := slices.IndexFunc(all, func(n nodes.Node) bool { return n.Addr == np.Node })
		if i < 0 {
			return fmt.Errorf("%s is not a node the controller has found", np.Node)
		}
		st, ok := status(all[i])
		if !ok || st.Config == nil || st.Config.Source != "node" {
			return fmt.Errorf("%s is not adopted (no pushed config): adopt it first", np.Node)
		}
		if p.otherKey(st) {
			return fmt.Errorf("%s: %s", np.Node, otherKeyRefusal)
		}
		s, err := settings.Load(settings.Path(p.dataDir))
		if err != nil {
			return err
		}
		if slices.Contains(s.Nodes, np.Node) {
			return fmt.Errorf("%s is in settings.json already", np.Node)
		}
		for _, n := range all {
			if n.Addr != np.Node && strings.EqualFold(n.ID, st.NodeID) && slices.Contains(s.Nodes, n.Addr) {
				return fmt.Errorf("%s says it is node %s, which settings.json has at %s: change that entry by hand if it moved",
					np.Node, st.NodeID, n.Addr)
			}
		}
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) {
		if err := check(); err != nil {
			return nil, err
		}
		added, err := settings.AddNode(settings.Path(p.dataDir), np.Node)
		if err != nil {
			return nil, err
		}
		run.Logf("added %s to settings.json: it counts as a peer in rollouts, and the controller's actions take it", np.Node)
		return map[string]any{"node": np.Node, "added": added}, nil
	}, nil
}

// settingsMove is the job kind "settings-move": a node of yours reinstalled at a new address,
// its settings.json entry and its pin moved from the address it was listed at to the one it
// answers on now. Nothing over the network proves a node is the one it says (its node key in
// /status is public), so this is the logged-in user's click, and only while the old address
// is silent, as a reinstall leaves it. The facts are re-checked at the click and again when
// it runs: it answers at `node` as the ID the entry's node has, trusting this controller's
// release key and reporting the node key recorded for that ID (catching an honest
// mismatch); `from` is listed and silent; `node` is neither listed nor pinned to another
// node. The entry moves in place (settings.MoveNode: the canary and out_of_service with it),
// then the pin; a pin that fails puts the entry back. node_key, for a node installed again
// over USB from here (waiting.go), is the node key read over USB: a node reporting it is the
// one expected, rather than the key pinned (pinned only once it has its settings back).
func (p adoptKind) settingsMove(raw json.RawMessage) (jobs.Func, error) {
	var np struct {
		Node    string `json:"node"`
		From    string `json:"from"`
		NodeKey string `json:"node_key,omitempty"`
	}
	if err := decodeStrict(raw, &np); err != nil {
		return nil, err
	}
	if np.Node == np.From {
		return nil, fmt.Errorf("the address it moved to and from are the same (%s)", np.Node)
	}
	id, name := "", "" // the node's ID and name, as the last check found them
	check := func() error {
		s, err := settings.Load(settings.Path(p.dataDir))
		if err != nil {
			return err
		}
		if !slices.Contains(s.Nodes, np.From) {
			return fmt.Errorf("%s is not in settings.json (it moved already, or the entry is not there): nothing to move", np.From)
		}
		if slices.Contains(s.Nodes, np.Node) {
			return fmt.Errorf("%s is in settings.json already: not moved", np.Node)
		}
		all := p.nodes()
		i := slices.IndexFunc(all, func(n nodes.Node) bool { return n.Addr == np.Node })
		if i < 0 {
			return fmt.Errorf("%s is not a node the controller has found", np.Node)
		}
		st, ok := status(all[i])
		if !ok {
			return fmt.Errorf("%s: no /status read yet", np.Node)
		}
		// The entry's own node: it has to have the same ID (the ID the settings entry is a
		// node of), or this would move one node's entry onto another.
		j := slices.IndexFunc(all, func(n nodes.Node) bool { return n.Addr == np.From })
		if j < 0 || all[j].ID == "" || !strings.EqualFold(all[j].ID, st.NodeID) {
			return fmt.Errorf("%s says it is node %s, which is not the node settings.json lists at %s (same ID): not moved",
				np.Node, st.NodeID, np.From)
		}
		name = st.NodeID
		if st.Config != nil && st.Config.Name != "" {
			name = st.Config.Name
		}
		if all[j].Online {
			return fmt.Errorf("%s still answers at %s, so this isn't it moved", name, np.From)
		}
		k, err := p.signer()
		if err != nil {
			return err
		}
		if len(st.Keys) == 0 || !p.pusher(k, nil).Trusts(st) {
			return fmt.Errorf("%s doesn't trust your release key: not moved", np.Node)
		}
		if pid, err := pins.Open(p.dataDir).Pinned(np.Node); err == nil && !strings.EqualFold(pid, st.NodeID) {
			return fmt.Errorf("%s is pinned to node %s: not moved", np.Node, pid)
		} else if err != nil && !errors.Is(err, pins.ErrNotPinned) {
			return err
		}
		if why := p.nodeKeyRefusal(st); why != "" && (np.NodeKey == "" || st.NodeKey != np.NodeKey) {
			return fmt.Errorf("%s: %s", np.Node, why)
		}
		id = st.NodeID
		return nil
	}
	if err := check(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) {
		if err := check(); err != nil {
			return nil, err
		}
		// The old address read now, not only as the registry last saw it.
		rctx, cancel := context.WithTimeout(ctx, nodeLookupTimeout)
		_, err := p.client().Status(rctx, np.From)
		cancel()
		if err == nil {
			return nil, fmt.Errorf("%s still answers at %s, so this isn't it moved", name, np.From)
		}
		path := settings.Path(p.dataDir)
		moved, err := settings.MoveNode(path, np.From, np.Node)
		if err != nil {
			return nil, err
		}
		if !moved {
			return nil, fmt.Errorf("%s is not in settings.json (it moved already): nothing to move", np.From)
		}
		replaced, err := pins.Open(p.dataDir).Pin(id, np.Node)
		if err != nil {
			if _, berr := settings.MoveNode(path, np.Node, np.From); berr != nil {
				return nil, fmt.Errorf("node %s not pinned to %s (%v), and its entry not put back at %s: %w", id, np.Node, err, np.From, berr)
			}
			return nil, fmt.Errorf("node %s not pinned to %s, so not moved: %w", id, np.Node, err)
		}
		run.Logf("moved %s to %s in settings.json: the node keeps its name and config, and the controller's actions take it there", np.From, np.Node)
		run.Logf("node %s pinned to %s now", id, np.Node)
		if replaced != "" {
			run.Logf("(node %s, pinned there before, is pinned to no address now)", replaced)
		}
		return map[string]any{"node": np.Node, "from": np.From, "moved": moved}, nil
	}, nil
}

// nodeKeyRefusal is why the node whose /status is st reports another node key than the one
// recorded (pinned) for its ID: "" when it reports that key, or when none is recorded
// (firmware from before the node key). /status is public, so a match proves nothing: it
// only catches an honest mismatch, such as a board erased and installed again, which makes
// a new node key.
func (p adoptKind) nodeKeyRefusal(st release.NodeStatus) string {
	key, err := pins.Open(p.dataDir).NodeKey(st.NodeID)
	switch {
	case errors.Is(err, pins.ErrNotPinned):
		return ""
	case err != nil:
		return err.Error()
	case key != "" && st.NodeKey != key:
		return "it reports a different node key than the one this controller recorded for node " + st.NodeID +
			", so it isn't moved. To bring it back, install it with Reinstall over USB from its node page"
	}
	return ""
}

// decodeStrict reads a job's params, unknown fields refused.
func decodeStrict(raw json.RawMessage, v any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("params: %v", err)
	}
	return nil
}
