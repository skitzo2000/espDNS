package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// setupEnv is a controller with no password and no settings: a fresh install. Nothing in
// it reaches a node or a primary.
type setupEnv struct {
	t      *testing.T
	h      http.Handler
	dir    string
	cookie string
	tok    string
	listed [][]string // what each save told the node list
}

func newSetupEnv(t *testing.T, opts ...func(*server)) *setupEnv {
	e := &setupEnv{t: t}
	e.h, _ = testServer(t, false, append([]func(*server){func(s *server) {
		e.dir = s.dataDir
		s.listed = func(n []string) { e.listed = append(e.listed, n) }
	}}, opts...)...)
	return e
}

// do sends a request for pattern with body, from this machine, in the session if there is one.
func (e *setupEnv) do(pattern, body string) (int, map[string]any) {
	e.t.Helper()
	r := fromHere(pattern, body)
	if e.cookie != "" {
		withSession(r, e.cookie, e.tok)
	}
	w := serve(e.h, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if strings.Contains(w.Body.String(), setupToken) {
		e.t.Errorf("%s: the token in the reply: %s", pattern, w.Body)
	}
	if out == nil {
		out = map[string]any{"raw": w.Body.String()}
	}
	return w.Code, out
}

// firstPassword sets the first password and keeps the session it logs in.
func (e *setupEnv) firstPassword() {
	e.t.Helper()
	r := fromHere("POST /api/setup/password", `{"password":"`+testPassword+`"}`)
	w := serve(e.h, r)
	if w.Code != http.StatusOK {
		e.t.Fatalf("first password: %d %s", w.Code, w.Body)
	}
	var reply struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &reply)
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.CookieName(r.Host) {
			e.cookie = c.Value
		}
	}
	e.tok = reply.Token
	if e.cookie == "" || e.tok == "" {
		e.t.Fatalf("first password: no session: %s", w.Body)
	}
}

// fromHere is request(pattern) with body, from a client on this machine.
func fromHere(pattern, body string) *http.Request {
	r := request(pattern)
	r.RemoteAddr = "127.0.0.1:40000"
	r.Body = io.NopCloser(strings.NewReader(body))
	return r
}

const setupToken = "first-run-token-0123456789"

// steps is /api/setup's steps as name: done.
func steps(out map[string]any) map[string]bool {
	m := map[string]bool{}
	for _, s := range out["steps"].([]any) {
		s := s.(map[string]any)
		m[s["step"].(string)] = s["done"].(bool)
	}
	return m
}

// A fresh install, set up from the browser: first_run until there is a password or a
// settings file; the password (from this machine, logged in at once); the zone primary
// (its address and the networks it allows); two nodes; each step then done.
func TestFirstRun(t *testing.T) {
	e := newSetupEnv(t)
	code, out := e.do("GET /api/setup", "")
	if code != http.StatusOK || out["first_run"] != true || out["password_set"] != false || out["settings_saved"] != false {
		t.Fatalf("fresh: %d %v", code, out)
	}
	for step, done := range steps(out) {
		if done {
			t.Errorf("fresh: %s done", step)
		}
	}
	// Read-only until the password: the settings are read, not saved.
	if code, out := e.do("GET /api/settings", ""); code != http.StatusOK || out["exists"] != false || out["version"] != "" {
		t.Fatalf("settings, fresh: %d %v", code, out)
	}
	if code, _ := e.do("POST /api/settings", `{"settings": {}, "version": ""}`); code != http.StatusForbidden {
		t.Fatalf("settings saved without a password: %d", code)
	}

	e.firstPassword()
	code, out = e.do("GET /api/setup", "")
	if code != http.StatusOK || out["first_run"] != false || !steps(out)["password"] {
		t.Fatalf("after the password: %d %v", code, out)
	}
	// The zone primary: its address and the networks it allows; the file made (version "").
	code, out = e.do("POST /api/settings", `{"settings": {"primary": {"address": "192.0.2.254", "allows": ["192.0.2.0/24"]}},
		"version": "", "primary_token": "`+setupToken+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("a token taken: %d %v", code, out)
	}
	code, out = e.do("POST /api/settings", `{"settings": {"primary": {"address": "192.0.2.254", "allows": ["192.0.2.0/24"]}},
		"version": ""}`)
	if code != http.StatusOK || out["exists"] != true || out["version"] == "" {
		t.Fatalf("primary: %d %v", code, out)
	}
	if p := out["primary"].(map[string]any); p["address"] != "192.0.2.254" || len(p["uncovered"].([]any)) != 0 {
		t.Fatalf("primary: %v", p)
	}
	// Two nodes, the first the canary: on the version just read.
	v := out["version"].(string)
	code, out = e.do("POST /api/settings", `{"settings": {"nodes": ["192.0.2.53", "192.0.2.52"], "canary": "192.0.2.52",
		"no_dhcp": ["192.0.2.0/24"], "dns_peers": ["192.0.2.254"],
		"primary": {"address": "192.0.2.254", "allows": ["192.0.2.0/24"]}}, "version": "`+v+`"}`)
	if code != http.StatusOK {
		t.Fatalf("nodes: %d %v", code, out)
	}
	s, err := settings.Load(settings.Path(e.dir))
	if err != nil || !slices.Equal(s.Nodes, []string{"192.0.2.53", "192.0.2.52"}) || s.Canary != "192.0.2.52" || s.ZonePrimary().Address != "192.0.2.254" {
		t.Fatal(s, err)
	}
	if len(e.listed) != 2 || !slices.Equal(e.listed[1], s.Nodes) {
		t.Fatalf("node list told %v", e.listed)
	}
	code, out = e.do("GET /api/setup", "")
	want := map[string]bool{"password": true, "keys": false, "primary": true, "first_node": true, "second_node": true, "router": false, "blocking": false}
	if code != http.StatusOK || out["nodes"] != 2.0 || out["primary"] != "192.0.2.254" || !mapsEqual(steps(out), want) {
		t.Fatalf("set up: %d %v", code, out)
	}
	// The first run's password is refused from now on, and the password stays.
	if code, _ := e.do("POST /api/setup/password", `{"password":"another password!"}`); code != http.StatusConflict {
		t.Fatalf("first password again: %d", code)
	}
	// In the action log: what changed.
	es, _ := actionlog.Tail(e.dir, 10)
	var args []string
	for _, a := range es {
		if a.Action == "settings" {
			args = append(args, strings.Join(a.Args, ","))
		}
	}
	if !slices.Equal(args, []string{"primary", "nodes,dns_peers,canary,no_dhcp"}) {
		t.Fatalf("action log: %q", args)
	}
}

func mapsEqual(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// Every refusal says which field, and changes nothing.
func TestSettingsRefused(t *testing.T) {
	e := newSetupEnv(t)
	e.firstPassword()
	code, out := e.do("POST /api/settings", `{"settings": {"nodes": ["192.0.2.53"], "primary": {"address": "192.0.2.254"}}, "version": ""}`)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	v := out["version"].(string)
	before, _ := os.ReadFile(settings.Path(e.dir))
	for body, field := range map[string]string{
		`{"settings": {"nodes": ["192.0.2.53"], "canary": "192.0.2.9"}, "version": "V"}`: "canary",
		`{"settings": {"nodes": ["192.0.2.53", "192.0.2.53"]}, "version": "V"}`:          "nodes",
		`{"settings": {"no_dhcp": ["192.0.2.1/24"]}, "version": "V"}`:                    "no_dhcp",
		`{"settings": {"dns_peers": ["192.0.2.254:"]}, "version": "V"}`:                  "dns_peers",
		`{"settings": {"primary": {"address": "primary.example"}}, "version": "V"}`:      "primary",
		`{"settings": {"primary": {"allows": ["192.0.2.1/24"]}}, "version": "V"}`:        "primary",
		`{"settings": {"primary": {"token": "x"}}, "version": "V"}`:                      "settings",
		`{"settings": {"nodez": []}, "version": "V"}`:                                    "settings",
		`{"settings": null, "version": "V"}`:                                             "settings",
		`{"version": "V"}`:                                                               "settings",
		`{"settings": {}}`:                                                               "version",
	} {
		code, out := e.do("POST /api/settings", strings.ReplaceAll(body, `"V"`, `"`+v+`"`))
		if code != http.StatusBadRequest || out["field"] != field {
			t.Errorf("%s: %d %v, want field %s", body, code, out, field)
		}
	}
	// Not JSON at all, or a field the request doesn't have (the old token's among them).
	for _, body := range []string{`{"settings": {}, "version": "", "token": "x"}`, `[`,
		`{"settings": {}, "version": "", "primary_token": "` + setupToken + `"}`, `{"settings": {}, "version": "", "remove_primary_token": true}`} {
		if code, _ := e.do("POST /api/settings", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, code)
		}
	}
	if after, _ := os.ReadFile(settings.Path(e.dir)); string(after) != string(before) {
		t.Fatal("a refused save changed settings.json")
	}
	if len(e.listed) != 1 {
		t.Fatal("a refused save told the node list:", e.listed)
	}
}

// An edit on a version that is no longer the file's (an adoption added a node meanwhile) is
// refused (409); read again, it saves.
func TestSettingsConflict(t *testing.T) {
	e := newSetupEnv(t)
	e.firstPassword()
	_, out := e.do("POST /api/settings", `{"settings": {"nodes": ["192.0.2.53"]}, "version": ""}`)
	v := out["version"].(string)
	if _, err := settings.AddNode(settings.Path(e.dir), "192.0.2.52"); err != nil {
		t.Fatal(err)
	}
	body := `{"settings": {"nodes": ["192.0.2.53"], "primary": {"address": "192.0.2.254"}}, "version": "%s"}`
	if code, out := e.do("POST /api/settings", strings.Replace(body, "%s", v, 1)); code != http.StatusConflict {
		t.Fatalf("stale: %d %v", code, out)
	}
	if s, _ := settings.Load(settings.Path(e.dir)); len(s.Nodes) != 2 {
		t.Fatal("the adoption's node lost:", s.Nodes)
	}
	_, out = e.do("GET /api/settings", "")
	form := out["settings"].(map[string]any)
	if n := form["nodes"].([]any); len(n) != 2 || form["canary"] != "" || form["primary"] != nil || len(form["dns_peers"].([]any)) != 0 {
		t.Fatalf("form: %v", form)
	}
	if code, out := e.do("POST /api/settings", strings.Replace(body, "%s", out["version"].(string), 1)); code != http.StatusOK {
		t.Fatalf("on the version read: %d %v", code, out)
	}
}

// A settings.json that doesn't parse (edited by hand): the form says why and gives its
// version, so the browser can replace it whole; /api/setup says it too.
func TestSettingsUnreadable(t *testing.T) {
	e := newSetupEnv(t)
	e.firstPassword()
	os.WriteFile(settings.Path(e.dir), []byte(`{"nodez": ["192.0.2.53"]}`), 0o600)
	code, out := e.do("GET /api/settings", "")
	if code != http.StatusOK || out["exists"] != true || out["version"] == "" || !strings.Contains(out["error"].(string), "unknown field") {
		t.Fatalf("%d %v", code, out)
	}
	if _, out := e.do("GET /api/setup", ""); out["settings_saved"] != true || out["settings_error"] == nil {
		t.Fatalf("setup: %v", out)
	}
	if code, out := e.do("POST /api/settings", `{"settings": {"nodes": ["192.0.2.53"]}, "version": "`+out["version"].(string)+`"}`); code != http.StatusOK || out["error"] != nil {
		t.Fatalf("replaced: %d %v", code, out)
	}
}

// The steps the browser marks: refused before a login and for a step the controller sees
// itself; each marked one done from then on (kept in setup.json, 0600); blocking done by a
// list defined too; "done" once every step is.
func TestSetupMarks(t *testing.T) {
	e := newSetupEnv(t)
	if code, _ := e.do("POST /api/setup/step", `{"step": "router"}`); code != http.StatusForbidden {
		t.Fatalf("marked without a login: %d", code)
	}
	e.firstPassword()
	_, out := e.do("GET /api/setup", "")
	if s := steps(out); s["router"] || s["blocking"] || s["primary"] || out["done"] != false {
		t.Fatalf("fresh: %v", out)
	}
	for _, bad := range []string{`{"step": "password"}`, `{"step": "second_node"}`, `{"step": ""}`, `{"step": "router", "x": 1}`, `nope`} {
		if code, _ := e.do("POST /api/setup/step", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	code, out := e.do("POST /api/setup/step", `{"step": "router"}`)
	if code != http.StatusOK || !steps(out)["router"] || steps(out)["primary"] {
		t.Fatalf("router: %d %v", code, out)
	}
	if code, out = e.do("POST /api/setup/step", `{"step": "primary"}`); code != http.StatusOK || !steps(out)["primary"] || !steps(out)["router"] {
		t.Fatalf("primary skipped: %d %v", code, out)
	}
	if fi, err := os.Stat(marksPath(e.dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("setup.json: %v %v", fi, err)
	}
	// Blocking: done once a list is defined, without a mark.
	if err := os.MkdirAll(blocking.Path(e.dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := blocking.Save(e.dir, blocking.Defs{Lists: []blocking.List{{Name: "main", Sources: []string{"domains:ads.txt"}}}}, ""); err != nil {
		t.Fatal(err)
	}
	_, out = e.do("GET /api/setup", "")
	if s := steps(out); !s["blocking"] || s["second_node"] || out["done"] != false {
		t.Fatalf("a list defined: %v", out)
	}
	// Every step: the key, two nodes.
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"192.0.2.53:80", "192.0.2.54:80"}}); err != nil {
		t.Fatal(err)
	}
	_, out = e.do("GET /api/setup", "")
	if s := steps(out); !s["first_node"] || !s["second_node"] || !s["primary"] {
		t.Fatalf("two nodes: %v", out)
	}
	if out["done"] != steps(out)["keys"] {
		t.Fatalf("done %v with the keys %v", out["done"], steps(out)["keys"])
	}
}
