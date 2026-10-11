package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// feed is a list server (https, on loopback) whose lists can be changed: it answers a
// conditional GET with 304 while the ETag is the same, and counts what it was asked.
type feed struct {
	mu    sync.Mutex
	text  map[string]string
	gets  map[string]int // 200s
	notMd map[string]int // 304s
	srv   *httptest.Server
}

func (f *feed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.text[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := sha256.Sum256([]byte(t))
	etag := `"` + hex.EncodeToString(s[:8]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		f.notMd[r.URL.Path]++
		w.WriteHeader(http.StatusNotModified)
		return
	}
	f.gets[r.URL.Path]++
	w.Write([]byte(t))
}

func (f *feed) set(path, text string) {
	f.mu.Lock()
	f.text[path] = text
	f.mu.Unlock()
}

func (f *feed) asked(path string) (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets[path], f.notMd[path]
}

// refreshEnv is an apply's environment with a list server, the job "refresh" and a
// refresher on a clock the test moves.
type refreshEnv struct {
	*applyEnv
	feed  *feed
	fetch *blocklist.Fetch
	clk   time.Time
	r     *refresher
}

func newRefreshEnv(t *testing.T, n int) *refreshEnv {
	e := &refreshEnv{applyEnv: newApplyEnv(t, n), clk: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}
	e.feed = &feed{text: map[string]string{}, gets: map[string]int{}, notMd: map[string]int{}}
	e.feed.srv = httptest.NewTLSServer(e.feed)
	t.Cleanup(e.feed.srv.Close)
	e.fetch = &blocklist.Fetch{TLS: e.feed.srv.Client().Transport.(*http.Transport).TLSClientConfig,
		Allow: []netip.AddrPort{netip.MustParseAddrPort(e.feed.srv.Listener.Addr().String())}}
	e.apply.fetch = e.fetch
	e.apply.now, e.apply.rnd = func() time.Time { return e.clk }, func() float64 { return 0 }
	e.kinds["refresh"] = func(raw json.RawMessage) (jobs.Func, error) {
		return refreshKind{apply: *e.apply}.kind(raw)
	}
	e.r = e.refresher()
	return e
}

// refresher is a refresher as the controller makes one at its start, on the test's clock,
// with no jitter.
func (e *refreshEnv) refresher() *refresher {
	r := newRefresher(e.dir, e.store, e.runner)
	r.fetch, r.now, r.rnd = e.fetch, func() time.Time { return e.clk }, func() float64 { return 0 }
	return r
}

func (e *refreshEnv) url(path string) string { return e.feed.srv.URL + path }

// define saves the list definitions, each list one feed path.
func (e *refreshEnv) define(lists map[string]string) {
	e.t.Helper()
	var d blocking.Defs
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for k := range lists {
			if !yield(k) {
				return
			}
		}
	}) {
		d.Lists = append(d.Lists, blocking.List{Name: name, Sources: []string{"domains:" + e.url(lists[name])}})
	}
	_, hash, _ := blocking.Load(e.dir)
	if _, err := blocking.Save(e.dir, d, hash); err != nil {
		e.t.Fatal(err)
	}
}

// pass runs one pass of the schedule, then waits for the refresh job it started, if any.
func (e *refreshEnv) pass(force bool) *jobs.Job {
	e.t.Helper()
	before := e.refreshJobs()
	e.r.pass(context.Background(), force, "")
	after := e.refreshJobs()
	if len(after) == len(before) {
		return nil
	}
	j := e.wait(after[0], nil)
	return &j
}

func (e *refreshEnv) refreshJobs() []jobs.Job {
	var out []jobs.Job
	for _, j := range e.runner.List() {
		if j.Kind == "refresh" {
			out = append(out, j)
		}
	}
	return out
}

func (e *refreshEnv) state(list string) blocking.ListRefresh {
	rf := blocking.LoadRefresh(e.dir)
	if ls := rf.Lists[list]; ls != nil {
		return *ls
	}
	return blocking.ListRefresh{}
}

func (e *refreshEnv) pushes() int {
	n := 0
	for _, fn := range e.nodes {
		for _, ev := range fn.Events() {
			if ev == "push blocklist" {
				n++
			}
		}
	}
	return n
}

// A downloaded list is fetched on its schedule, and sent to the nodes when it changed,
// one node at a time as an apply sends it; when it didn't, only the time it was checked is
// kept (a conditional GET, nothing fetched), nothing in the action log, nothing sent.
func TestRefreshSchedule(t *testing.T) {
	e := newRefreshEnv(t, 2)
	e.feed.set("/ads.txt", nameLines("ads", 300))
	e.define(map[string]string{"fleet": "/ads.txt"})

	// Never fetched: due at once; changed (nothing known), so sent to every node.
	j := e.pass(false)
	if j == nil || j.State != jobs.Done || j.Who != "" {
		t.Fatalf("first refresh: %+v", j)
	}
	var r applyResult
	json.Unmarshal(j.Result, &r)
	if !slices.Equal(r.Refreshed, []string{"fleet"}) || len(r.Rollouts) != 1 || len(r.Rollouts[0].Done) != 2 {
		t.Errorf("result %+v", r)
	}
	if n := e.pushes(); n != 2 {
		t.Errorf("%d pushes, want one per node", n)
	}
	st := e.state("fleet")
	src := st.Sources["domains:"+e.url("/ads.txt")]
	if st.Outcome != blocking.Updated || !st.Updated.Equal(e.clk) || !st.Checked.Equal(e.clk) || src == nil || src.SHA256 == "" || src.ETag == "" {
		t.Fatalf("state %+v %+v", st, src)
	}
	gets, _ := e.feed.asked("/ads.txt")
	logged := logLen(t, e.dir)

	// An hour later: not due, nothing asked.
	e.clk = e.clk.Add(time.Hour)
	if j := e.pass(false); j != nil {
		t.Fatalf("not due, but %+v", j)
	}
	if g, nm := e.feed.asked("/ads.txt"); g != gets || nm != 0 {
		t.Errorf("not due, but asked: %d %d", g, nm)
	}
	// A day later: due; the server says not modified: checked, nothing else.
	e.clk = e.clk.Add(23 * time.Hour)
	if j := e.pass(false); j != nil {
		t.Fatalf("unchanged, but %+v", j)
	}
	if g, nm := e.feed.asked("/ads.txt"); g != gets || nm != 1 {
		t.Errorf("a conditional GET: %d 200s, %d 304s", g, nm)
	}
	st2 := e.state("fleet")
	if st2.Outcome != blocking.Unchanged || !st2.Checked.Equal(e.clk) || !st2.Updated.Equal(st.Updated) {
		t.Errorf("unchanged: %+v", st2)
	}
	if n := logLen(t, e.dir); n != logged {
		t.Errorf("an unchanged check is in the action log (%d entries, were %d)", n, logged)
	}
	if n := e.pushes(); n != 2 {
		t.Errorf("unchanged, but pushed (%d)", n)
	}

	// The list grows a little: sent again, its time updated.
	e.feed.set("/ads.txt", nameLines("ads", 320))
	e.clk = e.clk.Add(25 * time.Hour)
	j = e.pass(false)
	if j == nil || j.State != jobs.Done {
		t.Fatalf("changed: %+v", j)
	}
	if n := e.pushes(); n != 4 {
		t.Errorf("%d pushes, want 4", n)
	}
	if st3 := e.state("fleet"); st3.Outcome != blocking.Updated || !st3.Updated.Equal(e.clk) {
		t.Errorf("updated: %+v", st3)
	}
	es, _ := actionlog.Tail(e.dir, 0)
	if last := es[len(es)-1]; last.Action != "job refresh" || last.Result != "ok" {
		t.Errorf("action log %+v", last)
	}

	// Off: no check, even when due.
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: e.addrs, BlocklistRefresh: "off"}); err != nil {
		t.Fatal(err)
	}
	e.clk = e.clk.Add(30 * 24 * time.Hour)
	g0, nm0 := e.feed.asked("/ads.txt")
	if j := e.pass(false); j != nil {
		t.Fatalf("off, but %+v", j)
	}
	if g, nm := e.feed.asked("/ads.txt"); g != g0 || nm != nm0 {
		t.Errorf("off, but asked")
	}
	// Refresh now checks all the same.
	e.pass(true)
	if _, nm := e.feed.asked("/ads.txt"); nm != nm0+1 {
		t.Errorf("Refresh now didn't check")
	}
}

func logLen(t *testing.T, dir string) int {
	es, err := actionlog.Tail(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	return len(es)
}

// A version the size-change check refuses isn't sent: the refresh job fails saying so (in
// Recent changes), the nodes keep what they have, and the schedule doesn't try that same
// version again, only a newer one. Taking it once sends it.
func TestRefreshSizeLimit(t *testing.T) {
	e := newRefreshEnv(t, 2)
	e.feed.set("/ads.txt", nameLines("ads", 300))
	e.define(map[string]string{"fleet": "/ads.txt"})
	if j := e.pass(false); j == nil || j.State != jobs.Done {
		t.Fatalf("first: %+v", j)
	}
	e.feed.set("/ads.txt", nameLines("ads", 10)) // a download that came back nearly empty
	e.clk = e.clk.Add(25 * time.Hour)
	j := e.pass(false)
	if j == nil || j.State != jobs.Failed || !strings.Contains(j.Error, "refused by the size-change check") ||
		!strings.Contains(j.Error, "blocked 300 → 10") || !strings.Contains(j.Error, "the nodes keep the lists they have") {
		t.Fatalf("refused: %+v", j)
	}
	if n := e.pushes(); n != 2 {
		t.Errorf("refused, but pushed (%d)", n)
	}
	st := e.state("fleet")
	if st.Outcome != blocking.Refused || st.Refused == "" || len(st.Over) == 0 || !strings.Contains(st.Message, "size-change limit") {
		t.Fatalf("state %+v", st)
	}
	es, _ := actionlog.Tail(e.dir, 0)
	if last := es[len(es)-1]; last.Action != "job refresh" || last.Result != "failed" || !strings.Contains(last.Error, "size-change") {
		t.Errorf("not in the action log as refused: %+v", last)
	}

	// The same version, the next day: checked, no job, still refused.
	e.clk = e.clk.Add(25 * time.Hour)
	if j := e.pass(false); j != nil {
		t.Fatalf("the same version again: %+v", j)
	}
	if st2 := e.state("fleet"); st2.Outcome != blocking.Refused || !st2.Checked.Equal(e.clk) {
		t.Errorf("state %+v", st2)
	}
	// Another version, still too small: tried, refused again.
	e.feed.set("/ads.txt", nameLines("ads", 12))
	e.clk = e.clk.Add(25 * time.Hour)
	if j := e.pass(false); j == nil || j.State != jobs.Failed {
		t.Fatalf("a newer version: %+v", j)
	}
	// Taken once (the page's "Send it anyway"): sent.
	b, _ := json.Marshal(refreshParams{Lists: []string{"fleet"}, AcceptChange: true})
	j2, err := e.runner.Start("refresh", "admin", b)
	if err != nil {
		t.Fatal(err)
	}
	if j2 = e.wait(j2, nil); j2.State != jobs.Done {
		t.Fatalf("accepted: %+v", j2)
	}
	if st3 := e.state("fleet"); st3.Outcome != blocking.Updated || st3.Refused != "" || len(st3.Over) != 0 {
		t.Errorf("state %+v", st3)
	}
	if n := e.pushes(); n != 4 {
		t.Errorf("%d pushes, want 4", n)
	}
}

// A list a pending change edits waits for that change's apply (which fetches it); a
// pending change elsewhere (a zone) doesn't hold a refresh up, and stays pending: a refresh
// never sends the user's changes.
func TestRefreshPendingChanges(t *testing.T) {
	e := newRefreshEnv(t, 2)
	e.feed.set("/ads.txt", nameLines("ads", 300))
	e.feed.set("/more.txt", nameLines("more", 50))
	e.define(map[string]string{"fleet": "/ads.txt"})

	// The user adds a second source to the list, not applied yet.
	defs, _, _ := blocking.Load(e.dir)
	defs.Lists[0].Sources = append(defs.Lists[0].Sources, "domains:"+e.url("/more.txt"))
	text, _ := json.MarshalIndent(defs, "", "  ")
	ch := e.add(changes.Lists, blocking.DefsFile, string(text))
	if j := e.pass(true); j != nil {
		t.Fatalf("a pending change edits the list, but %+v", j)
	}
	if g, nm := e.feed.asked("/ads.txt"); g+nm != 0 {
		t.Errorf("fetched while a change waits: %d", g+nm)
	}
	if st := e.state("fleet"); st.Outcome != blocking.Waiting || !strings.Contains(st.Message, "waiting to apply") {
		t.Errorf("state %+v", st)
	}
	// A refresh job queued before the change was made drops the list when it runs.
	b, _ := json.Marshal(refreshParams{Lists: []string{"fleet"}})
	j, err := e.runner.Start("refresh", "", b)
	if err != nil {
		t.Fatal(err)
	}
	if j = e.wait(j, nil); j.State != jobs.Done || e.pushes() != 0 {
		t.Fatalf("a refresh sent a list a change edits: %+v", j)
	}
	// Discarded: the list is refreshed as usual.
	if _, err := e.store.Discard(ch.ID); err != nil {
		t.Fatal(err)
	}
	// A zone change waits meanwhile: not taken by the refresh.
	z := e.add(changes.Zone, "example.com.zone", zoneOther)
	j2 := e.pass(true)
	if j2 == nil || j2.State != jobs.Done || e.pushes() != 2 {
		t.Fatalf("refresh: %+v", j2)
	}
	if cs := e.pending(); len(cs) != 1 || cs[0].ID != z.ID {
		t.Errorf("pending %+v", cs)
	}
	for _, fn := range e.nodes {
		if slices.Contains(fn.Events(), "push zones") {
			t.Errorf("the zone change was sent by a refresh")
		}
	}

	// The user's apply of a change to the list fetches it: the next check finds it as
	// the nodes have it, and sends nothing.
	e.feed.set("/ads.txt", nameLines("ads", 310))
	defs, _, _ = blocking.Load(e.dir)
	defs.Lists[0].Allow = []string{"domains:" + e.url("/more.txt")}
	text, _ = json.MarshalIndent(defs, "", "  ")
	e.add(changes.Lists, blocking.DefsFile, string(text))
	if j, _, p := e.runApply(nil); j.State != jobs.Done {
		t.Fatalf("apply %+v %+v", j, p)
	}
	pushed := e.pushes()
	e.clk = e.clk.Add(25 * time.Hour)
	if j := e.pass(false); j != nil {
		t.Fatalf("checked after the apply fetched it, but %+v", j)
	}
	if st := e.state("fleet"); st.Outcome != blocking.Unchanged || len(st.Sources) != 2 || e.pushes() != pushed {
		t.Errorf("after the apply: %+v", st)
	}
}

// After a restart the schedule goes on from the times kept: a list checked lately isn't
// fetched again at the start, one whose time came is; the first pass waits a while.
func TestRefreshRestart(t *testing.T) {
	e := newRefreshEnv(t, 2)
	e.feed.set("/a.txt", nameLines("a", 300))
	e.feed.set("/b.txt", nameLines("b", 300))
	e.define(map[string]string{"a": "/a.txt", "b": "/b.txt"})
	start := e.clk
	if j := e.pass(false); j == nil || j.State != jobs.Done {
		t.Fatalf("first: %+v", j)
	}
	// b was checked earlier than a (as the file says after some days).
	if err := blocking.UpdateRefresh(e.dir, func(rf *blocking.Refresh) {
		rf.List("b").Checked = start.Add(-23 * time.Hour)
	}); err != nil {
		t.Fatal(err)
	}
	ga, _ := e.feed.asked("/a.txt")
	gb, _ := e.feed.asked("/b.txt")

	// The controller restarts two hours later: a new refresher, the same data directory.
	e.clk = start.Add(2 * time.Hour)
	e.r = e.refresher()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.r.Run(ctx, 20*time.Millisecond, time.Hour); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, nm := e.feed.asked("/b.txt"); nm == 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if g, nm := e.feed.asked("/a.txt"); g != ga || nm != 0 {
		t.Errorf("a, checked 2 h ago, fetched at the start: %d %d", g, nm)
	}
	if g, nm := e.feed.asked("/b.txt"); g != gb || nm != 1 {
		t.Errorf("b, due, not checked: %d %d", g, nm)
	}
	if st := e.state("b"); !st.Checked.Equal(e.clk) || st.Outcome != blocking.Unchanged {
		t.Errorf("b %+v", st)
	}
	if st := e.state("a"); !st.Checked.Equal(start) {
		t.Errorf("a %+v", st)
	}
}

// The API: the schedule saved in settings.json (24h, the default, as none), refused
// unless a choice; the setup shows it with the list's times.
func TestRefreshAPI(t *testing.T) {
	e := newBlockEnv(t)
	if err := settings.Save(settings.Path(e.dir), settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		every string
		code  int
		file  string
	}{{"12h", 200, "12h"}, {"off", 200, "off"}, {"24h", 200, ""}, {"5m", 400, ""}, {"monthly", 400, ""}} {
		code, body := e.do("POST", "/api/blocking/refresh/schedule", map[string]any{"every": c.every})
		st, _ := settings.Load(settings.Path(e.dir))
		if code != c.code || code == 200 && st.BlocklistRefresh != c.file {
			t.Errorf("%s: %d %s (file %q)", c.every, code, body, st.BlocklistRefresh)
		}
	}
	code, body := e.do("GET", "/api/blocking/setup", nil)
	var out struct {
		Refresh struct {
			Every   string   `json:"every"`
			Choices []string `json:"choices"`
		} `json:"refresh"`
	}
	if json.Unmarshal([]byte(body), &out); code != 200 || out.Refresh.Every != "24h" || len(out.Refresh.Choices) != 5 {
		t.Errorf("setup %d %s", code, body)
	}
	// No refresher runs here: Refresh now says so.
	if code, _ := e.do("POST", "/api/blocking/refresh", map[string]any{}); code != http.StatusServiceUnavailable {
		t.Errorf("refresh now: %d", code)
	}
}

// The due time: the schedule after the last check (or the last compile), plus the jitter;
// an hour after a failure; never when off.
func TestRefreshDue(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	ls := blocking.ListRefresh{Checked: t0, JitterS: blocking.Jitter(day, 1)}
	if j := time.Duration(ls.JitterS) * time.Second; j != 30*time.Minute {
		t.Errorf("jitter %v", j)
	}
	for _, c := range []struct {
		at  time.Duration
		due bool
	}{{23 * time.Hour, false}, {day, false}, {day + 30*time.Minute, true}} {
		if got := ls.Due(day, time.Time{}, t0.Add(c.at)); got != c.due {
			t.Errorf("at %v: due %v", c.at, got)
		}
	}
	if ls.Due(0, time.Time{}, t0.Add(100*day)) {
		t.Error("off, but due")
	}
	fail := blocking.ListRefresh{Checked: t0, Outcome: blocking.Failed}
	if !fail.Due(day, time.Time{}, t0.Add(time.Hour)) || fail.Due(day, time.Time{}, t0.Add(59*time.Minute)) {
		t.Error("a failure is tried again after an hour")
	}
	never := blocking.ListRefresh{}
	if !never.Due(day, time.Time{}, t0) {
		t.Error("never fetched: due now")
	}
	if never.Due(day, t0, t0.Add(time.Hour)) || !never.Due(day, t0, t0.Add(day)) {
		t.Error("compiled, never checked: due a schedule after the compile")
	}
	if j := blocking.Jitter(6*time.Hour, 1); time.Duration(j)*time.Second != 18*time.Minute {
		t.Errorf("6 h jitter %d s", j)
	}
}
