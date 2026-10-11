package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/dist"
	"github.com/skitzo2000/espdns/controller/internal/dist/disttest"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
)

// Get firmware: the job fetches a published release from the forge (settings.json's
// "release_url", else the controller's), checks it with the project's key and imports it;
// /api/firmware then says each chip image is the project's release. A release another key
// signed is refused, nothing imported; no project key, and the job isn't queued.
func TestFetchReleaseJob(t *testing.T) {
	k := newTestKey(t)
	rel := t.TempDir()
	if err := disttest.Release(rel, "0.0.9", k); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.StripPrefix("/owner/espdns/releases/download/v0.0.9/", http.FileServer(http.Dir(rel))))
	defer srv.Close()
	proj := filepath.Join(t.TempDir(), "project.pub")
	old := rolling.ProjectPub
	rolling.ProjectPub = proj
	defer func() { rolling.ProjectPub = old }()

	dir := t.TempDir()
	fk := fetchKind{dataDir: dir, forge: srv.URL + "/owner/espdns"}
	r := jobs.New(dir, map[string]jobs.Kind{"fetch-release": fk.kind})
	r.Logf = t.Logf
	runJobs(t, r)
	run := func(params string) jobs.Job {
		t.Helper()
		j, err := r.Start("fetch-release", "admin", json.RawMessage(params))
		if err != nil {
			t.Fatal(err)
		}
		for range 2000 {
			if j, _ = r.Get(j.ID); j.State.Ended() {
				return j
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the job didn't end")
		return j
	}
	if _, err := r.Start("fetch-release", "admin", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "no project public key") {
		t.Fatalf("without the project's key: %v", err)
	}
	for _, p := range []string{`{"version":"latest"}`, `{"version":"v1.2"}`, `{"nonsense":1}`} {
		os.WriteFile(proj, release.PublicRaw(newTestKey(t)), 0o644)
		if _, err := r.Start("fetch-release", "admin", json.RawMessage(p)); err == nil {
			t.Errorf("%s queued", p)
		}
	}
	// Another key is the project's: refused, nothing imported
	if j := run(`{"version":"v0.0.9"}`); j.State != jobs.Failed || !strings.Contains(j.Error, "not the project's release key") {
		t.Fatalf("another key's release: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, "firmware", "images")); err == nil {
		t.Fatal("imported")
	}
	// The project's
	os.WriteFile(proj, release.PublicRaw(k), 0o644)
	j := run(`{"version":"v0.0.9"}`)
	var res fetchResult
	if json.Unmarshal(j.Result, &res); j.State != jobs.Done || res.Label != "project release v0.0.9" || len(res.Images) != 3 {
		t.Fatalf("%+v %s", j, j.Result)
	}

	h, _ := testServer(t, false, func(s *server) { s.dataDir = dir; s.releaseURL = srv.URL + "/owner/espdns" })
	w := serve(h, request("GET /api/firmware"))
	var out struct {
		Images []struct {
			Image, Version, Label, Origin string
		}
		ReleaseURL string `json:"release_url"`
		ProjectKey string `json:"project_key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || len(out.Images) != 3 ||
		out.ReleaseURL != srv.URL+"/owner/espdns" || out.ProjectKey != release.Fingerprint(release.PublicRaw(k)) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	for _, i := range out.Images {
		if i.Origin != dist.SignerProject || i.Label != "project release v0.0.9" || i.Version != "0.0.9" {
			t.Errorf("%+v", i)
		}
	}
}
