package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The scheduled blocklist refresh: the lists the controller downloads (a list's URL
// sources) are checked for a new version on a schedule, settings.json's
// "blocklist_refresh" (every day by default; "off"), and a list that changed is compiled
// and sent to the nodes by the job "refresh", which is an apply of that list alone: the
// same compile and size-change check, every node read first, the dry run, then one node
// at a time, signed, never the last healthy node (apply.go).
//
// The check (refresher) runs in the background, outside the jobs: a conditional GET of each
// URL source (If-None-Match, If-Modified-Since: a 304 fetches nothing), the body otherwise
// only hashed and compared with the version the nodes were last sent (refresh.json,
// internal/blocking). Nothing changed: only the time it was checked is kept, nothing in the
// action log. Changed: a refresh job is queued, which fetches the list again as it compiles
// it, and its end is in the action log (Recent changes): sent, or refused by the size-change
// check (the version refused isn't tried again, only a newer one), or failed.
//
// A list a pending change edits (its definition, or a source file it reads) waits: the
// apply of that change compiles it, fetching its sources then, and the user's changes and a
// refresh are never sent together. The other lists are refreshed as usual.
//
// After a restart the schedule goes on from the times kept in refresh.json: a list is
// checked when its time comes (its last check, or its last compile before any check, plus
// the schedule and its jitter), the first pass a minute or two after the start.
//
//	POST /api/blocking/refresh           {"accept_change": false}: check every downloaded list
//	                                     now (202); accept_change: send the list refused by the
//	                                     size-change check this once (a refresh job, 202)
//	POST /api/blocking/refresh/schedule  {"every": "off" | "6h" | "12h" | "24h" | "168h" | ""}: the
//	                                     schedule, saved in settings.json at once
//
// Both need a login, and are in the action log by the user.

// Refresh choices the Blocking page offers (any time from 1 h to 30 days is taken).
var refreshChoices = []string{"off", "6h", "12h", "24h", "168h"}

const (
	refreshTick  = time.Minute // how often the schedule looks for a list due
	refreshFirst = time.Minute // the first look after a start, plus up to as much again
)

// runnerAPI is what the refresh needs of the job runner.
type runnerAPI interface {
	Start(kind, who string, params json.RawMessage) (jobs.Job, error)
	List() []jobs.Job
}

type refresher struct {
	dataDir string
	store   *changes.Store
	runner  runnerAPI
	fetch   *blocklist.Fetch // nil in use; a test's server
	now     func() time.Time
	rnd     func() float64
	kick    chan string // a check now, by whom

	mu       sync.Mutex
	checking bool
}

func newRefresher(dataDir string, store *changes.Store, runner runnerAPI) *refresher {
	return &refresher{dataDir: dataDir, store: store, runner: runner, now: time.Now, rnd: rand.Float64, kick: make(chan string, 1)}
}

// Run checks the lists due every tick, the first time after first, until ctx ends; and
// every list at once when asked (Now).
func (r *refresher) Run(ctx context.Context, first, tick time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case who := <-r.kick:
			r.pass(ctx, true, who)
		case <-t.C:
			r.pass(ctx, false, "")
			t.Reset(tick)
		}
	}
}

// Now asks for every downloaded list to be checked now; false if a check is running or
// asked for already.
func (r *refresher) Now(who string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checking {
		return false
	}
	select {
	case r.kick <- who:
		r.checking = true
		return true
	default:
		return false
	}
}

// Checking says whether a check is running (or asked for).
func (r *refresher) Checking() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checking
}

// inflight are the lists a refresh job queued or running sends.
func (r *refresher) inflight() map[string]bool {
	out := map[string]bool{}
	for _, j := range r.runner.List() {
		if j.Kind != "refresh" || j.State.Ended() || len(j.Args) == 0 {
			continue
		}
		var p refreshParams
		if json.Unmarshal([]byte(j.Args[0]), &p) == nil {
			for _, l := range p.Lists {
				out[l] = true
			}
		}
	}
	return out
}

// pass checks the lists due (force: every list with a URL source), and queues one refresh
// job for those that changed. who asked for it ("" the schedule).
func (r *refresher) pass(ctx context.Context, force bool, who string) {
	if force {
		defer func() {
			r.mu.Lock()
			r.checking = false
			r.mu.Unlock()
		}()
	}
	st, err := settings.Load(settings.Path(r.dataDir))
	if err != nil {
		log.Printf("blocklist refresh: %v", err)
		return
	}
	every := st.Refresh()
	if every == 0 && !force {
		return
	}
	defs, _, err := blocking.Load(r.dataDir)
	if err != nil {
		log.Printf("blocklist refresh: %v", err)
		return
	}
	state := blocking.LoadRefresh(r.dataDir)
	busy := r.inflight()
	now := r.now()
	var due []blocking.List
	for _, l := range defs.Lists {
		if len(l.URLSources()) == 0 || busy[l.Name] {
			continue
		}
		ls := state.Lists[l.Name]
		if ls == nil {
			ls = &blocking.ListRefresh{}
		}
		if !force && !ls.Due(every, blocking.Compiled(r.dataDir, l.Name), now) {
			continue
		}
		why, err := pendingTouch(r.dataDir, r.store, l)
		if err != nil {
			log.Printf("blocklist refresh: %s: %v", l.Name, err)
			continue
		}
		if why != "" {
			if ls.Outcome != blocking.Waiting || ls.Message != why {
				r.record(func(rf *blocking.Refresh) { rf.Outcome(l.Name, blocking.Waiting, why, now, every, r.rnd()) })
			}
			continue
		}
		due = append(due, l)
	}
	var changed []string
	for _, l := range due {
		if ctx.Err() != nil {
			return
		}
		if r.check(ctx, l, state.List(l.Name), every) {
			changed = append(changed, l.Name)
		}
	}
	if len(changed) == 0 {
		return
	}
	// Updating before the job starts: the job's own outcome comes after it.
	now = r.now()
	r.record(func(rf *blocking.Refresh) {
		for _, name := range changed {
			rf.Outcome(name, blocking.Updating, "", now, every, r.rnd())
		}
	})
	b, _ := json.Marshal(refreshParams{Lists: changed})
	j, err := r.runner.Start("refresh", who, b)
	if err != nil {
		r.record(func(rf *blocking.Refresh) {
			for _, name := range changed {
				rf.Outcome(name, blocking.Failed, "changed, but not sent: "+err.Error(), now, every, r.rnd())
			}
		})
		log.Printf("blocklist refresh: %v changed, but the refresh wasn't started: %v", changed, err)
		return
	}
	log.Printf("blocklist refresh: %v changed: job %s sends them", changed, j.ID)
}

// check fetches the list's URL sources as the nodes have them (ls, refresh.json) would be
// asked about, and says whether one changed (and not to the version refused before). An
// unchanged list, a failed check and a version refused again are recorded here.
func (r *refresher) check(ctx context.Context, l blocking.List, ls *blocking.ListRefresh, every time.Duration) bool {
	red := l.Redactor()
	internal, err := l.Internal(r.dataDir)
	now := r.now()
	fail := func(err error) bool {
		msg := red.Replace(err.Error())
		log.Printf("blocklist refresh: %s: %s", l.Name, msg)
		r.record(func(rf *blocking.Refresh) { rf.Outcome(l.Name, blocking.Failed, msg, now, every, r.rnd()) })
		return false
	}
	if err != nil {
		return fail(err)
	}
	got := map[string]blocklist.Fetched{}
	sums := map[string]string{}
	changed := false
	for _, spec := range l.URLSources() {
		s, _ := blocklist.ParseSource(spec, false)
		var prev *blocklist.Fetched
		if p := ls.Sources[spec]; p != nil && p.SHA256 != "" {
			prev = &p.Fetched
		}
		f, same, err := blocklist.Check(ctx, r.fetch, internal, s.Path, prev)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", blocking.RedactURL(s.Path), err))
		}
		got[spec], sums[spec] = f, f.SHA256
		changed = changed || !same
	}
	now = r.now()
	if !changed || ls.Refused != "" && ls.Refused == blocking.SourcesSum(sums) {
		r.record(func(rf *blocking.Refresh) { rf.Checked(l, got, !changed, now, every, r.rnd()) })
		return false
	}
	return true
}

func (r *refresher) record(f func(*blocking.Refresh)) {
	if err := blocking.UpdateRefresh(r.dataDir, f); err != nil {
		log.Printf("blocklist refresh: %s: %v", blocking.RefreshFile, err)
	}
}

// pendingTouch says why a pending change keeps the list from a refresh ("" none): it edits
// the list's definition or a source file the list reads, or an apply that stopped wrote
// the definitions (every list is sent again by the next apply).
func pendingTouch(dataDir string, store *changes.Store, l blocking.List) (string, error) {
	cs, err := store.List()
	if err != nil {
		return "", err
	}
	defsEdited := false
	for _, c := range cs {
		switch c.Kind {
		case changes.Source:
			if l.Uses(c.Name) {
				return fmt.Sprintf("a change waiting to apply edits %s, which it reads: applying it fetches the list", c.Name), nil
			}
		case changes.Lists:
			if c.Written {
				return "a change waiting to apply edits the lists: applying it fetches them", nil
			}
			defsEdited = true
		}
	}
	if !defsEdited {
		return "", nil
	}
	eff, err := store.Effective(changes.Lists, blocking.DefsFile)
	if err != nil {
		return "", err
	}
	defs := blocking.Defs{}
	if eff.Exists {
		if defs, err = blocking.Parse(eff.Text); err != nil {
			return "", err
		}
	}
	nl, ok := defs.Lookup(dataDir, l.Name)
	a, _ := json.Marshal(nl)
	b, _ := json.Marshal(l)
	if !ok || !bytes.Equal(a, b) {
		return "a change waiting to apply edits this list: applying it fetches it", nil
	}
	return "", nil
}

// ---- the job ------------------------------------------------------------------------------

// refreshKind is the job "refresh": downloaded lists that changed, compiled and sent as an
// apply of them alone (apply.go: check, compile, dry run, push).
//
//	POST /api/jobs  {"kind": "refresh", "params": {"lists": ["<name>"], "accept_change": false}}
type refreshKind struct {
	apply applyKind
}

type refreshParams struct {
	Lists        []string `json:"lists"`
	AcceptChange bool     `json:"accept_change,omitempty"`
}

func (k refreshKind) kind(raw json.RawMessage) (jobs.Func, error) {
	var p refreshParams
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("params: %v", err)
	}
	if len(p.Lists) == 0 {
		return nil, errors.New(`params: need "lists", the lists to send again`)
	}
	defs, _, err := blocking.Load(k.apply.dataDir)
	if err != nil {
		return nil, err
	}
	for _, name := range p.Lists {
		l, ok := defs.Lookup(k.apply.dataDir, name)
		if !ok || l.IsOverrides() {
			return nil, fmt.Errorf("lists: no list %q in %s", name, blocking.DefsFile)
		}
		if len(l.URLSources()) == 0 {
			return nil, fmt.Errorf("lists: %s has no source to download", name)
		}
	}
	if err := k.apply.checkParams(applyParams{}); err != nil {
		return nil, err
	}
	if _, err := k.apply.signer(); err != nil {
		return nil, err
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) { return k.run(ctx, run, p) }, nil
}

func (k refreshKind) run(ctx context.Context, run *jobs.Run, p refreshParams) (any, error) {
	a := k.apply
	st, _ := settings.Load(settings.Path(a.dataDir))
	every := st.Refresh()
	defs, _, err := blocking.Load(a.dataDir)
	if err != nil {
		return nil, err
	}
	res := &applyResult{Changes: []string{}}
	var lists []string
	for _, name := range p.Lists {
		l, ok := defs.Lookup(a.dataDir, name)
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: no longer a list", name))
			continue
		}
		why, err := pendingTouch(a.dataDir, a.store, l)
		if err != nil {
			return nil, err
		}
		if why != "" {
			// The user's change goes first: its apply fetches the list.
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %s", name, why))
			now := a.clock()
			k.record(run, func(rf *blocking.Refresh) { rf.Outcome(name, blocking.Waiting, why, now, every, a.jitter()) })
			continue
		}
		lists = append(lists, name)
	}
	for _, n := range res.Notes {
		run.Logf("%s", n)
	}
	if len(lists) == 0 {
		return res, nil
	}
	run.Logf("sending again, as their sources changed: %s", strings.Join(lists, ", "))
	ap := applyParams{refresh: lists}
	if p.AcceptChange {
		ap.AcceptChange = lists
		run.Logf("the size-change check takes this version whatever its size (accepted once)")
	}
	t := &applyTracker{run: run, p: applyProgress{Changes: []string{}, Rollouts: []*applyRollout{}}}
	for _, s := range []string{"check", "compile", "dry run", "push"} {
		t.p.Steps = append(t.p.Steps, applyStep{Name: s, State: "waiting"})
	}
	run.SetProgress(t.p)
	step := "check"
	err = a.apply(ctx, run, t, ap, nil, res, &step)
	now := a.clock()
	var refused []string
	switch {
	case err == nil:
		if ferr := a.store.ForgetListSums(res.compiled); ferr != nil {
			run.Logf("the compiled lists' earlier files are still remembered: %v", ferr)
		}
		a.recordSent(run, res)
		res.Refreshed = lists
	case errors.As(err, new(askError)):
		k.record(run, func(rf *blocking.Refresh) {
			for _, q := range res.Questions {
				if l, ok := defs.Lookup(a.dataDir, q.List); ok {
					rf.Refuse(l, l.FetchedOf(res.built[q.List]), q.Over, now, every, a.jitter())
					refused = append(refused, fmt.Sprintf("%s (%s)", q.List, strings.Join(q.Over, ", ")))
				}
			}
			for _, name := range lists {
				if ls := rf.Lists[name]; ls == nil || ls.Outcome != blocking.Refused || !slices.ContainsFunc(res.Questions, func(q question) bool { return q.List == name }) {
					rf.Outcome(name, blocking.Failed, "not sent: another list in the same refresh changed by more than its limit", now, every, a.jitter())
				}
			}
		})
		err = fmt.Errorf("refused by the size-change check: %s; nothing was sent, the nodes keep the lists they have",
			strings.Join(refused, "; "))
	default:
		msg := err.Error()
		if errors.Is(err, fleet.ErrStopped) {
			msg = "stopped before every node had it"
		}
		k.record(run, func(rf *blocking.Refresh) {
			for _, name := range lists {
				rf.Outcome(name, blocking.Failed, msg, now, every, a.jitter())
			}
		})
	}
	t.update(func(pp *applyProgress) {
		pp.Notes, pp.Questions = res.Notes, res.Questions
		switch {
		case err == nil:
			pp.Outcome = fmt.Sprintf("done: %s sent to every node that runs it", strings.Join(lists, ", "))
		case len(refused) > 0:
			pp.Outcome = "refused by the size-change check: nothing was sent"
		case errors.Is(err, fleet.ErrStopped):
			pp.Outcome = "stopped on request"
		default:
			pp.Outcome = "stopped: " + err.Error()
		}
		for i := range pp.Steps {
			if err != nil && pp.Steps[i].Name == step && pp.Steps[i].State == "running" {
				pp.Steps[i].State, pp.Steps[i].Detail = "failed", err.Error()
			}
		}
	})
	return res, err
}

func (k refreshKind) record(run *jobs.Run, f func(*blocking.Refresh)) {
	if err := blocking.UpdateRefresh(k.apply.dataDir, f); err != nil {
		run.Logf("the blocklists' refresh state (%s) wasn't written: %v", blocking.RefreshFile, err)
	}
}

// ---- the API ------------------------------------------------------------------------------

type refreshServer struct {
	dataDir string
	refresh *refresher // nil: no schedule runs (a test's)
	runner  runnerAPI
}

func (s refreshServer) routes(mux *routes) {
	mux.HandleFunc("POST /api/blocking/refresh", needLogin("blocking needs", s.now))
	mux.HandleFunc("POST /api/blocking/refresh/schedule", needLogin("blocking needs", s.schedule))
}

func (s refreshServer) now(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AcceptChange bool   `json:"accept_change"`
		List         string `json:"list"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("body: %v", err))
		return
	}
	user := auth.User(r.Context())
	if body.AcceptChange {
		// The list refused, sent this once whatever its size.
		if body.List == "" {
			httpErr(w, http.StatusBadRequest, errors.New(`"list": the list refused`))
			return
		}
		b, _ := json.Marshal(refreshParams{Lists: []string{body.List}, AcceptChange: true})
		j, err := s.runner.Start("refresh", user, b)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]any{"job": j})
		return
	}
	if s.refresh == nil {
		httpErr(w, http.StatusServiceUnavailable, errors.New("the blocklist refresh isn't running"))
		return
	}
	if !s.refresh.Now(user) {
		httpErr(w, http.StatusConflict, errors.New("a check is running already"))
		return
	}
	log.Printf("blocklist refresh: a check now, asked by %s", user)
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"checking": true})
}

// writeJSONStatus is writeJSON with a status other than 200.
func writeJSONStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s refreshServer) schedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Every string `json:"every"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, fmt.Errorf("body: %v", err))
		return
	}
	if _, err := settings.ParseRefresh(body.Every); err != nil {
		fieldErr(w, "every", err)
		return
	}
	path := settings.Path(s.dataDir)
	st, version, _, err := settings.Read(path)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Every == "24h" {
		body.Every = "" // the default, as the file had it
	}
	st.BlocklistRefresh = body.Every
	switch err := settings.SaveIf(path, version, st, nil, nil); {
	case errors.Is(err, settings.ErrChanged):
		httpErr(w, http.StatusConflict, err)
		return
	case err != nil:
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	user := auth.User(r.Context())
	if err := actionlog.Append(s.dataDir, actionlog.Entry{Event: "save", ID: "settings-" + time.Now().UTC().Format("20060102-150405"),
		Source: "controller", Who: user, Action: "settings", Args: []string{"blocklist_refresh"}, Result: "ok"}); err != nil {
		log.Printf("settings: saved, but not in the action log: %v", err)
	}
	log.Printf("settings: blocklist_refresh %q saved by %s", body.Every, user)
	writeJSON(w, map[string]any{"every": refreshWords(st)})
}

// refreshWords is the schedule as the page's control has it: one of refreshChoices, or the
// time set by hand.
func refreshWords(st settings.Settings) string {
	v := st.BlocklistRefresh
	d, err := settings.ParseRefresh(v)
	if err != nil {
		return v
	}
	for _, c := range refreshChoices {
		if cd, _ := settings.ParseRefresh(c); cd == d {
			return c // "" is "24h"; "1440m" is "24h"
		}
	}
	return v
}
