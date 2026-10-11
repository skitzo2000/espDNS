package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"slices"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// Blocking as one fleet setup (the Blocking page; internal/blocking fleet.go): the fleet's
// blocklists, the always allowed and blocked sites, each node's blocking in words, the list
// catalogue, and "check a site". Edits are pending changes ("collect, then Apply"): each a
// new text for blocking/lists.json or an overrides file, made here from the file as the
// pending changes make it, so the page sends what it means ("add this list"), not a file.
//
//	GET  /api/blocking/setup               the setup as the pending changes make it, with what the
//	                                       nodes run, the catalogue and each node's blocking
//	POST /api/blocking/setup               {"op": "add", "sources": [...]} | {"op": "remove"|"use"|"unuse",
//	                                       "source"} | {"op": "allow"|"block"|"unallow"|"unblock", "site"}:
//	                                       pending changes (201: {"changes": [...]})
//	GET  /api/blocking/check?name=<site>   blocked or not, by what (the source lists), on which nodes
//
// Every one needs a login (the lists' URLs may hold a feed's key), and each edit is in the
// action log by the user, as every pending change is.

type fleetBlocking struct {
	dataDir string
	store   *changes.Store
	nodes   func() []nodes.Node
	refresh *refresher // the schedule's state (nil: none runs)
	runner  runnerAPI  // the refresh jobs queued or running (nil: none)
}

func (f fleetBlocking) routes(mux *routes) {
	need := func(h http.HandlerFunc) http.HandlerFunc { return needLogin("blocking needs", h) }
	mux.HandleFunc("GET /api/blocking/setup", need(f.get))
	mux.HandleFunc("POST /api/blocking/setup", need(f.edit))
	mux.HandleFunc("GET /api/blocking/check", need(f.check))
}

// setupList is one of the fleet's blocklists.
type setupList struct {
	Source string `json:"source"` // the spec, as the definition holds it (what an edit names)
	Shown  string `json:"shown"`  // the spec with a URL's credentials hidden
	Title  string `json:"title"`
	// From the catalogue, when it is one of its lists.
	Catalog string `json:"catalog,omitempty"`
	What    string `json:"what,omitempty"`
	Group   string `json:"group,omitempty"`
	By      string `json:"by,omitempty"`
	On      bool   `json:"on"`    // in use
	Allow   bool   `json:"allow"` // an allow source (set up outside the page)
	// Waiting: a pending change adds it or turns it on or off; the nodes don't have that yet.
	Waiting bool `json:"waiting"`
	// Domains is what its last compile read from it (Counted: there is a count).
	Domains int  `json:"domains"`
	Counted bool `json:"counted"`
	// Download: a list fetched from its web address, which the schedule checks (refresh.go):
	// when the version the nodes have was first sent (Updated; before any refresh, the
	// last compile) and when it was last checked.
	Download bool      `json:"download"`
	Updated  time.Time `json:"updated,omitzero"`
	Checked  time.Time `json:"checked,omitzero"`
}

// setupSite is an always allowed or blocked site.
type setupSite struct {
	Site    string `json:"site"`
	Waiting bool   `json:"waiting"` // a pending change adds it
}

// setupNode is a node's blocking, in words the page shows.
type setupNode struct {
	Host   string `json:"host"`
	Online bool   `json:"online"`
	// State: "on", "paused", "off" (in its settings), "none" (no list yet), "loading",
	// "failed", "unknown" (not read, or firmware from before blocking).
	State   string `json:"state"`
	PausedS uint32 `json:"paused_s,omitempty"`
	// Current: it runs the fleet list as it was last compiled.
	Current bool   `json:"current"`
	Entries uint32 `json:"entries,omitempty"`
	Error   string `json:"error,omitempty"`
	// Room is about how many more domains fit in its memory for lists (-1: not known, or
	// the list is on its SD card).
	Room int64 `json:"room"`
}

func (f fleetBlocking) get(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"catalog": blocking.Catalog, "groups": blocking.Groups, "pause_max_s": pauseMax}
	eff, err := f.store.Effective(changes.Lists, blocking.DefsFile)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	defs, cur := blocking.Defs{Lists: []blocking.List{}}, blocking.Defs{Lists: []blocking.List{}}
	if eff.Exists {
		if defs, err = blocking.Parse(eff.Text); err != nil {
			out["defs_error"] = fmt.Sprintf("%s: %v", blocking.DefsFile, err)
		}
	}
	if c, _, err := blocking.Load(f.dataDir); err == nil {
		cur = c
	}
	fl, idx := defs.Fleet()
	was, _ := cur.Lookup(f.dataDir, fl.Name)
	out["list"], out["defined"], out["hash"] = fl.Name, idx >= 0, eff.Hash
	others := []string{}
	for _, l := range defs.Lists[min(len(defs.Lists), 1):] {
		others = append(others, l.Name)
	}
	out["others"] = others
	rep, rerr := blocking.ReadReport(f.dataDir, fl.Name)
	if rerr == nil {
		out["report"] = map[string]any{"time": rep.Time, "blocked": rep.Blocked, "allowed": rep.Allowed, "bytes": rep.Bytes}
	} else {
		out["report"] = nil
	}
	lists := []setupList{}
	rf := blocking.LoadRefresh(f.dataDir)
	ls := rf.Lists[fl.Name]
	if ls == nil {
		ls = &blocking.ListRefresh{}
	}
	add := func(spec string, on, allow bool, before []string) {
		sl := setupList{Source: spec, Shown: blocking.Redacted(spec), Title: blocking.SourceTitle(spec), On: on, Allow: allow,
			Waiting: !slices.Contains(before, spec)}
		if c, ok := blocking.CatalogFor(spec); ok {
			sl.Catalog, sl.What, sl.Group, sl.By = c.ID, c.What, c.Group, c.By
		}
		if rerr == nil && on {
			sl.Domains, sl.Counted = rep.Count(spec)
		}
		if s, err := blocklist.ParseSource(spec, false); err == nil && s.IsURL() {
			sl.Download = true
			if x := ls.Sources[spec]; x != nil {
				sl.Updated, sl.Checked = x.Updated, x.Checked
			}
			if sl.Updated.IsZero() && rerr == nil && on && !sl.Waiting {
				sl.Updated = rep.Time
			}
		}
		lists = append(lists, sl)
	}
	for _, s := range fl.Sources {
		add(s, true, false, was.Sources)
	}
	for _, s := range fl.Allow {
		add(s, true, true, was.Allow)
	}
	for _, s := range fl.Unused {
		add(s, false, false, was.Unused)
	}
	out["lists"] = lists
	out["refresh"] = f.refreshState(was, ls)

	for _, o := range []struct{ key, file string }{{"allowed", blocking.OverridesAllow}, {"blocked", blocking.OverridesBlock}} {
		v, err := f.store.Effective(changes.Source, o.file)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		c, _ := changes.Current(f.dataDir, changes.Source, o.file)
		now := blocking.Sites(c.Text)
		sites := []setupSite{}
		for _, s := range blocking.Sites(v.Text) {
			sites = append(sites, setupSite{Site: s, Waiting: !slices.Contains(now, s)})
		}
		out[o.key] = sites
	}
	pending := false
	if cs, err := f.store.List(); err == nil {
		for _, c := range cs {
			pending = pending || c.Kind == changes.Lists || c.Kind == changes.Source
		}
	}
	out["pending"] = pending
	out["nodes"] = f.nodeStates(fl.Name)
	writeJSON(w, out)
}

// refreshState is the schedule as the page shows it, for the fleet's list as the nodes
// have it (l): how often, when it was last checked and sent changed, when it is checked
// next, and the last check's outcome (a refusal, a failure, or waiting for a pending change).
func (f fleetBlocking) refreshState(l blocking.List, ls *blocking.ListRefresh) map[string]any {
	st, _ := settings.Load(settings.Path(f.dataDir))
	every := st.Refresh()
	out := map[string]any{"every": refreshWords(st), "choices": refreshChoices, "list": l.Name,
		"downloads": len(l.URLSources()) > 0, "checking": f.refresh != nil && f.refresh.Checking(), "sending": false,
		"checked": ls.Checked, "updated": ls.Updated, "outcome": ls.Outcome, "message": l.Redactor().Replace(ls.Message),
		"over": ls.Over}
	if ls.Over == nil {
		out["over"] = []string{}
	}
	if every > 0 && len(l.URLSources()) > 0 {
		next := ls.Next(every, blocking.Compiled(f.dataDir, l.Name))
		if now := time.Now(); next.Before(now) {
			next = now // due: the next look (within a minute)
		}
		out["next"] = next
	}
	if f.runner != nil {
		for _, j := range f.runner.List() {
			var p refreshParams
			if j.Kind == "refresh" && !j.State.Ended() && len(j.Args) > 0 && json.Unmarshal([]byte(j.Args[0]), &p) == nil &&
				slices.Contains(p.Lists, l.Name) {
				out["sending"], out["job"] = true, j.ID
			}
		}
	}
	return out
}

// nodeStates are the listed nodes' blocking.
func (f fleetBlocking) nodeStates(list string) []setupNode {
	var listed []string
	if s, err := settings.Load(settings.Path(f.dataDir)); err == nil {
		listed = s.Nodes
	}
	sums := f.store.ListSums(list)
	if sum := listSums.sum(blocking.Out(f.dataDir, list)); sum != "" {
		sums = append(sums, sum)
	}
	known := map[string]nodes.Node{}
	for _, n := range f.nodes() {
		known[n.Addr] = n
	}
	out := []setupNode{}
	for _, h := range listed {
		n, ok := known[h]
		sn := setupNode{Host: h, State: "unknown", Room: -1}
		if !ok {
			out = append(out, sn)
			continue
		}
		sn.Online = n.Online
		st, ok := status(n)
		switch {
		case !ok:
		case st.ServiceOff("blocking"):
			sn.State = "off"
		case st.Blocking == nil:
		default:
			b := st.Blocking
			sn.Entries, sn.Error = b.List.Entries, b.List.Error
			sn.Current = b.List.SHA256 != "" && slices.Contains(sums, b.List.SHA256)
			switch {
			case b.PausedS > 0:
				sn.State, sn.PausedS = "paused", b.PausedS
			case b.List.State == "on":
				sn.State = "on"
			case b.List.State == "loading", b.List.State == "failed":
				sn.State = b.List.State
			default:
				sn.State = "none"
			}
			sn.Room = room(st)
		}
		out = append(out, sn)
	}
	return out
}

// room is about how many more domains fit in a node's memory for lists: its board's share
// less what its lists take, at the bytes a domain takes in the list it runs. -1: not known
// (firmware from before the memory plan, no list on, or the list on its SD card).
func room(st release.NodeStatus) int64 {
	b := st.Blocking
	if st.Memory == nil || b == nil || b.List.State != "on" || b.List.Tier != "ram" || b.List.Entries == 0 || b.List.Bytes <= 0 {
		return -1
	}
	used := b.List.Bytes
	if b.Overrides.State == "on" {
		used += b.Overrides.Bytes
	}
	free := int64(st.Memory.Board.BlocklistKB)*1024 - used
	if free <= 0 {
		return 0
	}
	return free * int64(b.List.Entries) / b.List.Bytes
}

// setupEdit is POST /api/blocking/setup's body.
type setupEdit struct {
	Op      string   `json:"op"`
	Sources []string `json:"sources"`
	Source  string   `json:"source"`
	Site    string   `json:"site"`
}

func (f fleetBlocking) edit(w http.ResponseWriter, r *http.Request) {
	var b setupEdit
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("body: %v", err))
		return
	}
	who := auth.User(r.Context())
	var edits []changes.Edit
	switch b.Op {
	case "add", "remove", "use", "unuse":
		specs := b.Sources
		if b.Op != "add" {
			specs = []string{b.Source}
		}
		v, err := f.store.Effective(changes.Lists, blocking.DefsFile)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		defs := blocking.Defs{Lists: []blocking.List{}}
		if v.Exists {
			if defs, err = blocking.Parse(v.Text); err != nil {
				httpErr(w, http.StatusConflict, fmt.Errorf("%s: %v: fix it under Advanced first", blocking.DefsFile, err))
				return
			}
		}
		after, summary, err := blocking.EditFleet(defs, b.Op, specs)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		text, err := json.MarshalIndent(after, "", "  ")
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
		edits = append(edits, changes.Edit{Kind: changes.Lists, Name: blocking.DefsFile, Text: append(text, '\n'), Hash: v.Hash, Summary: summary, Who: who})
	case "allow", "block", "unallow", "unblock":
		site, err := blocking.Site(b.Site)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		file, other := blocking.OverridesAllow, blocking.OverridesBlock
		if b.Op == "block" || b.Op == "unblock" {
			file, other = other, file
		}
		add := b.Op == "allow" || b.Op == "block"
		summary := map[string]string{"allow": "Always allow ", "block": "Always block ", "unallow": "Stop always allowing ",
			"unblock": "Stop always blocking "}[b.Op] + site
		e, err := f.siteEdit(file, site, add, summary, who)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		edits = append(edits, e)
		// Allowed and blocked at once means nothing: allowing a blocked site takes it off
		// the blocked ones, and the other way round.
		if add {
			if o, err := f.siteEdit(other, site, false, "", who); err == nil {
				o.Summary = map[bool]string{true: "Stop always blocking ", false: "Stop always allowing "}[b.Op == "allow"] + site
				edits = append(edits, o)
			}
		}
	default:
		httpErr(w, http.StatusBadRequest, fmt.Errorf("op %q: add, remove, use, unuse, allow, block, unallow or unblock", b.Op))
		return
	}
	cs, err := f.store.AddAll(edits)
	if err != nil {
		httpErr(w, changeCode(err), err)
		return
	}
	logger := changesServer{dataDir: f.dataDir}
	for _, c := range cs {
		logger.log(r, "change add", c, c.Summary)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{"changes": cs})
}

// siteEdit is the change of an overrides file that adds or takes out a site, from the file
// as the pending changes make it.
func (f fleetBlocking) siteEdit(file, site string, add bool, summary, who string) (changes.Edit, error) {
	v, err := f.store.Effective(changes.Source, file)
	if err != nil {
		return changes.Edit{}, err
	}
	text, err := blocking.EditSites(v.Text, site, add)
	if err != nil {
		if errors.Is(err, blocking.ErrNoChange) {
			if add {
				return changes.Edit{}, fmt.Errorf("%s is %s already", site, map[string]string{blocking.OverridesAllow: "always allowed", blocking.OverridesBlock: "always blocked"}[file])
			}
			return changes.Edit{}, fmt.Errorf("%s is not %s", site, map[string]string{blocking.OverridesAllow: "always allowed", blocking.OverridesBlock: "always blocked"}[file])
		}
		return changes.Edit{}, err
	}
	return changes.Edit{Kind: changes.Source, Name: file, Text: text, Hash: v.Hash, Summary: summary, Who: who}, nil
}

// checkNode is a node in a check's answer: whether it blocks the name now, and why not.
type checkNode struct {
	Host   string `json:"host"`
	Blocks bool   `json:"blocks"`
	Why    string `json:"why,omitempty"` // paused, off, older list, no list, unknown
}

func (f fleetBlocking) check(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	defs, _, _ := blocking.Load(f.dataDir)
	fl, idx := defs.Fleet()
	list := fl.Name
	if idx < 0 {
		if _, err := blocking.ReadReport(f.dataDir, list); errors.Is(err, fs.ErrNotExist) {
			list = ""
		}
	}
	c, err := blocking.CheckName(f.dataDir, list, name)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	type source struct {
		Spec  string `json:"spec"`
		Title string `json:"title"`
	}
	sources := []source{}
	if c.By == "list" {
		for _, e := range c.Entries {
			if e.Source >= 0 && e.Source < len(c.Specs) && !slices.ContainsFunc(sources, func(s source) bool { return s.Spec == c.Specs[e.Source] }) {
				sources = append(sources, source{Spec: c.Specs[e.Source], Title: blocking.SourceTitle(c.Specs[e.Source])})
			}
		}
	}
	// The nodes: the overrides block on every node with blocking on, paused or not; the
	// list only where it is on, not paused, and the one compiled.
	ns := []checkNode{}
	if c.Result == blocking.Blocked {
		for _, n := range f.nodeStates(fl.Name) {
			cn := checkNode{Host: n.Host}
			switch {
			case n.State == "off":
				cn.Why = "off"
			case n.State == "unknown":
				cn.Why = "unknown"
			case c.By == blocking.OverridesName:
				cn.Blocks = true
			case n.State == "paused":
				cn.Why = "paused"
			case n.State != "on":
				cn.Why = "no list"
			case !n.Current:
				cn.Why = "older list"
			default:
				cn.Blocks = true
			}
			ns = append(ns, cn)
		}
	}
	out := map[string]any{"check": c, "sources": sources, "nodes": ns}
	if !c.Compiled.IsZero() {
		out["compiled"] = c.Compiled.Format(time.RFC3339)
	}
	writeJSON(w, out)
}
