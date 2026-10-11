package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/rotation/rotationtest"
)

// Rotate keys from the controller: prepared over the API (the nodes' /status read then),
// signed offline (here, rotation.Sign, as espdns key rotate sign), one node's signed
// release taken back over the API, pushed as the job "key-rotate", which stops at a node
// that isn't healthy and goes on from it the next time; meanwhile the node actions sign each
// node with the key it trusts; at the end the controller's keys are the new ones.
func TestRotateKeysFromTheController(t *testing.T) {
	f := rotationtest.New(t, 3)
	if err := auth.SetPassword(auth.Path(f.Data), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	src := keys.FileSource{Path: keys.Path(f.Data)}
	a := actions{dataDir: f.Data, key: src, known: func() []string { return f.Addrs }, healthy: 300 * time.Millisecond,
		client: func() *fleet.Client { return &fleet.Client{Poll: 10 * time.Millisecond} }}
	runner := jobs.New(f.Data, a.kinds())
	runner.Logf = t.Logf
	runJobs(t, runner)
	s := &server{dataDir: f.Data, nodes: func() []nodes.Node { return nil }, auth: auth.New(auth.Path(f.Data)), runner: runner,
		key: src, builder: newBuilder(t.TempDir(), t.TempDir(), t.TempDir())}
	s.push = pushKind{actions: a, secret: newSecret(), nodes: s.nodes, job: func(string) (jobs.Job, bool) { return jobs.Job{}, false }}
	s.adopt = adoptKind{actions: a, secret: newSecret(), nodes: s.nodes, used: &usedRuns{}, job: func(string) (jobs.Job, bool) { return jobs.Job{}, false }}
	h, _ := s.handler()
	cookie, tok := login(t, h)
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "http://127.0.0.1:8480"+path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
		return serve(h, withSession(r, cookie, tok))
	}

	// A private key in the request is refused, not ignored.
	if w := call("POST", "/api/keys/rotation", map[string]any{"new_release": "generate", "private_key": "x"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a private key field: %d %s", w.Code, w.Body)
	}
	w := call("POST", "/api/keys/rotation", map[string]any{"new_release": "generate"})
	if w.Code != http.StatusCreated {
		t.Fatalf("prepare: %d %s", w.Code, w.Body)
	}
	var v struct {
		ID          string `json:"id"`
		SignCommand string `json:"sign_command"`
		SignedAll   bool   `json:"signed_all"`
		New         rotation.Keys
		Nodes       []rotation.Node
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	if v.ID == "" || v.SignedAll || len(v.Nodes) != 3 || !strings.Contains(v.SignCommand, "--network none") ||
		!strings.Contains(v.SignCommand, "keys/rotation/"+v.ID) {
		t.Fatalf("prepared: %s", w.Body)
	}
	// Not signed: the push isn't queued.
	if _, err := runner.Start("key-rotate", "admin", json.RawMessage(`{"id":"`+v.ID+`"}`)); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("push unsigned: %v", err)
	}

	// Signed offline, elsewhere: one node's release.bin brought back through the page.
	dir := filepath.Join(rotation.Root(f.Data), v.ID)
	other := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(other, os.DirFS(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := rotation.Sign(other, f.Recovery); err != nil {
		t.Fatal(err)
	}
	signed, _ := os.ReadFile(filepath.Join(other, v.Nodes[0].Dir, rotation.SignedFile))
	if w := call("POST", "/api/keys/rotation/"+v.ID+"/signed", map[string]any{"release": base64.StdEncoding.EncodeToString(signed[:100])}); w.Code != http.StatusBadRequest {
		t.Errorf("a cut release: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "/api/keys/rotation/"+v.ID+"/signed", map[string]any{"release": base64.StdEncoding.EncodeToString(signed)}); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"`+v.Nodes[0].NodeID+`":true`) {
		t.Fatalf("signed release taken: %d %s", w.Code, w.Body)
	}
	// The rest signed here, as the CLI does in the controller's directory.
	if _, err := rotation.Sign(dir, f.Recovery); err != nil {
		t.Fatal(err)
	}

	wait := func(j jobs.Job) jobs.Job {
		for range 2000 {
			j, _ = runner.Get(j.ID)
			if j.State.Ended() {
				return j
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("job %s didn't end", j.ID)
		return j
	}
	f.Nodes[1].Break(true)
	j, err := runner.Start("key-rotate", "admin", json.RawMessage(`{"id":"`+v.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if j = wait(j); j.State != jobs.Failed || !strings.Contains(j.Error, f.Addrs[1]) {
		t.Fatalf("job: %s %s", j.State, j.Error)
	}
	var prog struct{ Nodes []rotation.Node }
	json.Unmarshal(j.Progress, &prog)
	if len(prog.Nodes) != 3 || prog.Nodes[0].State != rotation.StateDone || prog.Nodes[1].State != rotation.StateKeys {
		t.Fatalf("progress: %s", j.Progress)
	}
	// Part way: a flush to the node with its new keys and to the one with its old keys.
	for _, addr := range []string{f.Addrs[0], f.Addrs[2]} {
		j, err := runner.Start("flush", "admin", json.RawMessage(`{"node":"`+addr+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if j = wait(j); j.State != jobs.Done {
			t.Errorf("flush %s: %s %s", addr, j.State, j.Error)
		}
	}
	f.Nodes[1].Break(false)
	if j, err = runner.Start("key-rotate", "admin", json.RawMessage(`{"id":"`+v.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if j = wait(j); j.State != jobs.Done {
		t.Fatalf("job again: %s %s", j.State, j.Error)
	}
	cur, err := src.Key()
	if err != nil || keys.Fingerprint(cur) != v.New.Release {
		t.Fatalf("release.pem: %v", err)
	}
	for _, a := range f.Addrs {
		st, _ := (&fleet.Client{}).Status(t.Context(), a)
		if st.Keys[0] != v.New.Release {
			t.Errorf("%s: %v", a, st.Keys)
		}
	}
	// Nothing pending; the finished one is listed.
	w = call("GET", "/api/keys/rotation", nil)
	var list struct {
		Pending   *json.RawMessage `json:"pending"`
		Rotations []struct {
			ID       string    `json:"id"`
			Finished time.Time `json:"finished"`
		}
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if list.Pending != nil || len(list.Rotations) != 1 || list.Rotations[0].Finished.IsZero() {
		t.Errorf("after: %s", w.Body)
	}
}
