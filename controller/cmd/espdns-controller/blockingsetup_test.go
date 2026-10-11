package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// fleetSetup is GET /api/blocking/setup.
type fleetSetup struct {
	List    string                 `json:"list"`
	Defined bool                   `json:"defined"`
	Lists   []setupList            `json:"lists"`
	Allowed []setupSite            `json:"allowed"`
	Blocked []setupSite            `json:"blocked"`
	Pending bool                   `json:"pending"`
	Catalog []blocking.CatalogList `json:"catalog"`
	Groups  []string               `json:"groups"`
	Nodes   []setupNode            `json:"nodes"`
	Report  *struct {
		Blocked int `json:"blocked"`
	} `json:"report"`
}

func (e *blockEnv) setup() fleetSetup {
	e.t.Helper()
	code, b := e.do("GET", "/api/blocking/setup", nil)
	var s fleetSetup
	if code != 200 || json.Unmarshal([]byte(b), &s) != nil {
		e.t.Fatalf("setup: %d %s", code, b)
	}
	return s
}

func (e *blockEnv) setupEdit(body map[string]any, want int) []changes.Change {
	e.t.Helper()
	code, b := e.do("POST", "/api/blocking/setup", body)
	if code != want {
		e.t.Fatalf("%v: %d %s", body, code, b)
	}
	var out struct {
		Changes []changes.Change `json:"changes"`
	}
	json.Unmarshal([]byte(b), &out)
	return out.Changes
}

// Blocking as one fleet setup: lists added from the catalogue or by a source, turned off
// and on, the last in use kept, all as pending changes of lists.json named in words; the
// always allowed and blocked sites as changes of the overrides' files, a site moved from
// one to the other; what the nodes have told apart from what waits.
func TestFleetSetupEdits(t *testing.T) {
	e := newBlockEnv(t)
	s := e.setup()
	if s.List != blocking.FleetName || s.Defined || len(s.Lists) != 0 || len(s.Catalog) != len(blocking.Catalog) || len(s.Groups) == 0 || s.Pending {
		t.Fatalf("empty: %+v", s)
	}
	pro := blocking.Catalog[2]
	cs := e.setupEdit(map[string]any{"op": "add", "sources": []string{pro.Source, "domains:local.txt"}}, http.StatusCreated)
	if len(cs) != 1 || cs[0].Kind != changes.Lists || cs[0].Name != blocking.DefsFile || cs[0].Summary != "Add 2 blocklists: HaGeZi Pro, local.txt" || cs[0].Who != "admin" {
		t.Fatalf("add: %+v", cs)
	}
	s = e.setup()
	if !s.Defined || len(s.Lists) != 2 || !s.Lists[0].On || !s.Lists[0].Waiting || s.Lists[0].Title != pro.Name || s.Lists[0].Catalog != pro.ID ||
		s.Lists[0].Group != pro.Group || s.Lists[1].Title != "local.txt" || !s.Pending {
		t.Fatalf("after add: %+v", s)
	}
	// Nothing written yet: the file is as it was.
	if _, err := os.Stat(filepath.Join(e.dir, "blocking", blocking.DefsFile)); !os.IsNotExist(err) {
		t.Errorf("lists.json written: %v", err)
	}
	e.setupEdit(map[string]any{"op": "add", "sources": []string{pro.Source}}, http.StatusBadRequest)
	cs = e.setupEdit(map[string]any{"op": "unuse", "source": pro.Source}, http.StatusCreated)
	if cs[0].Summary != "Stop using blocklist HaGeZi Pro" {
		t.Errorf("unuse: %+v", cs)
	}
	if s = e.setup(); s.Lists[0].Source != "domains:local.txt" || s.Lists[1].On || s.Lists[1].Source != pro.Source {
		t.Errorf("after unuse: %+v", s.Lists)
	}
	if _, b := e.do("POST", "/api/blocking/setup", map[string]any{"op": "unuse", "source": "domains:local.txt"}); !strings.Contains(b, "last list in use") {
		t.Errorf("the last one off: %s", b)
	}
	e.setupEdit(map[string]any{"op": "use", "source": pro.Source}, http.StatusCreated)
	e.setupEdit(map[string]any{"op": "remove", "source": "domains:local.txt"}, http.StatusCreated)
	if s = e.setup(); len(s.Lists) != 1 || s.Lists[0].Source != pro.Source || !s.Lists[0].On {
		t.Errorf("after remove: %+v", s.Lists)
	}
	for _, bad := range []map[string]any{{"op": "add"}, {"op": "add", "sources": []string{"domains:../x"}}, {"op": "remove", "source": "domains:none.txt"},
		{"op": "rename"}, {"op": "allow", "site": "localhost"}, {"op": "add", "sources": []string{pro.Source}, "x": 1}} {
		if code, b := e.do("POST", "/api/blocking/setup", bad); code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, code, b)
		}
	}

	// The sites.
	cs = e.setupEdit(map[string]any{"op": "allow", "site": "https://Ads.Example.com/page"}, http.StatusCreated)
	if len(cs) != 1 || cs[0].Kind != changes.Source || cs[0].Name != blocking.OverridesAllow || cs[0].Summary != "Always allow ads.example.com" {
		t.Fatalf("allow: %+v", cs)
	}
	e.setupEdit(map[string]any{"op": "allow", "site": "ads.example.com"}, http.StatusBadRequest)
	cs = e.setupEdit(map[string]any{"op": "block", "site": "ads.example.com"}, http.StatusCreated)
	if len(cs) != 2 || cs[0].Name != blocking.OverridesBlock || cs[0].Summary != "Always block ads.example.com" ||
		cs[1].Name != blocking.OverridesAllow || cs[1].Summary != "Stop always allowing ads.example.com" {
		t.Fatalf("block an allowed one: %+v", cs)
	}
	if s = e.setup(); len(s.Allowed) != 0 || len(s.Blocked) != 1 || s.Blocked[0].Site != "ads.example.com" || !s.Blocked[0].Waiting {
		t.Errorf("sites: %+v %+v", s.Allowed, s.Blocked)
	}
	e.setupEdit(map[string]any{"op": "unblock", "site": "ads.example.com"}, http.StatusCreated)
	if s = e.setup(); len(s.Blocked) != 0 {
		t.Errorf("unblocked: %+v", s.Blocked)
	}
	es, _ := actionlog.Tail(e.dir, 50)
	if !slices.ContainsFunc(es, func(x actionlog.Entry) bool {
		return x.Action == "change add" && slices.Contains(x.Args, "Always allow ads.example.com")
	}) {
		t.Errorf("action log %+v", es)
	}
	// Each a pending change, which the header lists.
	code, b := e.do("GET", "/api/changes", nil)
	if code != 200 || !strings.Contains(b, "Stop using blocklist HaGeZi Pro") || !strings.Contains(b, "Blocking: compiled again") {
		t.Errorf("changes: %d %s", code, b)
	}
}

// What the nodes have, after a compile: each list's domains, the nodes on the list as
// compiled (or paused, or off in their settings), the room left; check a site naming the
// list that blocks it and the nodes that do, the overrides first.
func TestFleetSetupCheck(t *testing.T) {
	e := newBlockEnv(t)
	feed := names("feed", 300)
	srv := e.serveFeed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, feed) }))
	e.saveSource("local.txt", names("local", 50), "")
	e.saveSource("wild.txt", "tracker.example.net\n", "")
	feedSpec := "domains:" + srv.URL + "/feed.txt?key=secret"
	if code, b := e.do("POST", "/api/blocking/defs", map[string]any{"lists": []map[string]any{{"name": "main",
		"sources": []string{"domains:local.txt", feedSpec, "wildcard:wild.txt"}}}, "hash": ""}); code != 200 {
		t.Fatalf("defs: %d %s", code, b)
	}
	if j := e.compile("main", false); j.State != jobs.Done {
		t.Fatalf("compile: %+v", j)
	}
	sum := listSums.sum(blocking.Out(e.dir, "main"))
	nodeAt := func(addr string, blk map[string]any, services ...map[string]any) nodes.Node {
		st := map[string]any{"blocking": blk, "memory": map[string]any{"board": map[string]any{"blocklist_kb": 64}}}
		if services != nil {
			st["services"] = services
		}
		return nodes.Node{Addr: addr, Online: true, Status: st}
	}
	on := map[string]any{"list": map[string]any{"state": "on", "sha256": sum, "entries": 351, "tier": "ram", "bytes": 4096}, "paused_s": 0}
	e.ns = []nodes.Node{
		nodeAt("192.0.2.52", on),
		nodeAt("192.0.2.53", map[string]any{"list": map[string]any{"state": "on", "sha256": strings.Repeat("0", 64), "entries": 9, "tier": "ram", "bytes": 100}, "paused_s": 120}),
		nodeAt("192.0.2.54", on, map[string]any{"name": "blocking", "state": "off"}),
		nodeAt("192.0.2.55", map[string]any{"list": map[string]any{"state": "on", "sha256": strings.Repeat("1", 64), "entries": 9, "tier": "sd", "bytes": 100}}),
	}
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"192.0.2.52", "192.0.2.53", "192.0.2.54", "192.0.2.55", "192.0.2.56"}}); err != nil {
		t.Fatal(err)
	}
	s := e.setup()
	if len(s.Lists) != 3 || s.Lists[0].Domains != 50 || !s.Lists[0].Counted || s.Lists[1].Domains != 300 || s.Lists[0].Waiting ||
		s.Report == nil || s.Report.Blocked != 351 || s.Pending {
		t.Fatalf("lists: %+v %+v", s.Lists, s.Report)
	}
	if strings.Contains(s.Lists[1].Shown, "secret") || s.Lists[1].Source != feedSpec {
		t.Errorf("the feed's key: %+v", s.Lists[1])
	}
	want := []setupNode{
		{Host: "192.0.2.52", Online: true, State: "on", Current: true, Entries: 351, Room: (64*1024 - 4096) * 351 / 4096},
		{Host: "192.0.2.53", Online: true, State: "paused", PausedS: 120, Entries: 9, Room: (64*1024 - 100) * 9 / 100},
		{Host: "192.0.2.54", Online: true, State: "off", Room: -1},
		{Host: "192.0.2.55", Online: true, State: "on", Entries: 9, Room: -1},
		{Host: "192.0.2.56", State: "unknown", Room: -1},
	}
	if !slices.Equal(s.Nodes, want) {
		t.Errorf("nodes:\n%+v\nwant\n%+v", s.Nodes, want)
	}

	check := func(name string) (blocking.Check, []string, []checkNode) {
		t.Helper()
		code, b := e.do("GET", "/api/blocking/check?name="+name, nil)
		var out struct {
			Check   blocking.Check `json:"check"`
			Sources []struct {
				Title string `json:"title"`
			} `json:"sources"`
			Nodes []checkNode `json:"nodes"`
		}
		if code != 200 || json.Unmarshal([]byte(b), &out) != nil {
			t.Fatalf("check %s: %d %s", name, code, b)
		}
		if strings.Contains(b, "secret") {
			t.Errorf("the feed's key in %s", b)
		}
		var titles []string
		for _, s := range out.Sources {
			titles = append(titles, s.Title)
		}
		return out.Check, titles, out.Nodes
	}
	c, titles, ns := check("feed7.example.com")
	if c.Result != blocking.Blocked || c.By != "list" || !slices.Equal(titles, []string{"127.0.0.1"}) || len(ns) != 5 ||
		!ns[0].Blocks || ns[1].Why != "paused" || ns[2].Why != "off" || ns[3].Why != "older list" || ns[4].Why != "unknown" {
		t.Errorf("feed7: %+v %v %+v", c, titles, ns)
	}
	if c, titles, _ = check("a.b.tracker.example.net"); c.Result != blocking.Blocked || !slices.Equal(titles, []string{"wild.txt"}) {
		t.Errorf("a subdomain of a wildcard entry: %+v %v", c, titles)
	}
	if c, _, ns = check("a.local3.example.com"); c.Result != blocking.NotBlocked || len(ns) != 0 {
		t.Errorf("a subdomain of a name-only entry: %+v", c)
	}
	// The overrides as the nodes have them: an allowed site, a blocked one blocking while
	// paused.
	e.saveSource(blocking.OverridesAllow, "feed7.example.com\n", "")
	e.saveSource(blocking.OverridesBlock, "*.extra.example.org\n", "")
	if c, _, _ = check("feed7.example.com"); c.Result != blocking.Allowed || c.By != blocking.OverridesName {
		t.Errorf("allowed: %+v", c)
	}
	if c, _, ns = check("x.extra.example.org"); c.Result != blocking.Blocked || c.By != blocking.OverridesName || !ns[1].Blocks || ns[2].Blocks {
		t.Errorf("blocked by the overrides: %+v %+v", c, ns)
	}
	if code, _ := e.do("GET", "/api/blocking/check?name=bad..name", nil); code != http.StatusBadRequest {
		t.Errorf("a bad name: %d", code)
	}
}

// Every one needs a login: the session and its token.
func TestFleetSetupLogin(t *testing.T) {
	h, _ := testServer(t, true)
	cookie, _ := login(t, h)
	for _, p := range []string{"GET /api/blocking/setup", "POST /api/blocking/setup", "GET /api/blocking/check?name=a.example.com"} {
		if w := serve(h, request(p)); !refusedByLogin(w) {
			t.Errorf("%s without a session: %d", p, w.Code)
		}
		if w := serve(h, withSession(request(p), cookie, "")); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without the token: %d", p, w.Code)
		}
	}
}
