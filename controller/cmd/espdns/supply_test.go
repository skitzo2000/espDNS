package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/dist"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// signedBuild is a release of the catalog boards' chip images, version, signed with the key
// pem (on standard input, as release sign takes it), checked against pubFile.
func signedBuild(t *testing.T, version string, pem []byte, pubFile string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "dist")
	if _, err := runRelease(t, "", "build", "-version", version, "-images", chipImages(t, version), "-catalog", "../../../boards", "-out", out); err != nil {
		t.Fatal(err)
	}
	if _, err := runRelease(t, string(pem), "sign", "-dir", out, "-pub", pubFile); err != nil {
		t.Fatal(err)
	}
	return out
}

// release import takes a release signed by the project's key (-pub) or by yours (the
// controller's release key, in -data) and says which; any other is refused. The import's
// provenance is what a firmware rollout re-checks: a swapped app.bin is refused.
func TestReleaseImportSigners(t *testing.T) {
	projPEM, projPub := releaseKey(t)
	data := t.TempDir()
	images := filepath.Join(data, "firmware", "images")

	// The project's release
	out := signedBuild(t, "0.0.4", projPEM, projPub)
	got, err := runRelease(t, "", "import", "-dir", out, "-data", data, "-pub", projPub)
	if err != nil || !strings.Contains(got, "version 0.0.4, signed by the project's release key") {
		t.Fatalf("import: %v %s", err, got)
	}
	old := rolling.ProjectPub
	rolling.ProjectPub = projPub
	defer func() { rolling.ProjectPub = old }()
	img := filepath.Join(images, "esp32s3-octal")
	fw, err := rolling.LoadFirmware([]string{img}, "", data, t.Logf)
	if err != nil || len(fw) != 1 || fw[0].Check == nil || fw[0].Origin != "project release v0.0.4" {
		t.Fatalf("%+v %v", fw, err)
	}
	if err := fw[0].Check(fw[0].App); err != nil {
		t.Fatal("as imported:", err)
	}
	// app.bin swapped after the import: the rollout's check refuses it
	os.WriteFile(filepath.Join(img, "app.bin"), app("0.0.4-evil"), 0o600)
	fw, err = rolling.LoadFirmware([]string{img}, "", data, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw[0].Check(fw[0].App); err == nil || !strings.Contains(err.Error(), "not the one in the signed") {
		t.Fatalf("a swapped app.bin: %v", err)
	}

	// Your own build, signed with your release key: no -pub needed
	ownPEM, ownPub := releaseKey(t)
	k, err := keys.Parse("own", ownPEM)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(data, keys.Dir), 0o700)
	if _, err := keys.Import(keys.Path(data), k, false); err != nil {
		t.Fatal(err)
	}
	out = signedBuild(t, "0.0.5", ownPEM, ownPub)
	got, err = runRelease(t, "", "import", "-dir", out, "-data", data, "-pub", filepath.Join(t.TempDir(), "none.pub"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("a -pub that isn't there: %v", err)
	}
	got, err = runRelease(t, "", "import", "-dir", out, "-data", data)
	if err != nil || !strings.Contains(got, "version 0.0.5, signed by your release key") {
		t.Fatalf("own build: %v %s", err, got)
	}
	if o := dist.ReadOrigin(img, dist.Signers{Own: mustReadFile(t, ownPub)}); o.Label != "your build v0.0.5" {
		t.Errorf("origin %+v", o)
	}

	// Anyone else's: refused, nothing changed
	otherPEM, otherPub := releaseKey(t)
	out = signedBuild(t, "0.0.6", otherPEM, otherPub)
	before := mustReadFile(t, filepath.Join(img, "image.json"))
	if _, err := runRelease(t, "", "import", "-dir", out, "-data", data, "-pub", projPub); err == nil ||
		!strings.Contains(err.Error(), "signed by neither the project's release key") {
		t.Fatalf("another key's release: %v", err)
	}
	if !bytes.Equal(before, mustReadFile(t, filepath.Join(img, "image.json"))) {
		t.Error("another key's release changed the images")
	}
}

// release fetch downloads the chip images named (-image) of a published release (here from
// a test forge), checks them with the project's key, and imports them; with
// none named, those settings.json's nodes need (by their /status), with those imported when
// one doesn't answer; none, and nothing is fetched.
func TestReleaseFetch(t *testing.T) {
	projPEM, projPub := releaseKey(t)
	out := signedBuild(t, "0.0.4", projPEM, projPub)
	srv := httptest.NewServer(http.StripPrefix("/owner/espdns/releases/download/v0.0.4/", http.FileServer(http.Dir(out))))
	defer srv.Close()
	data := t.TempDir()
	fetch := func(more ...string) (string, error) {
		return runRelease(t, "", append([]string{"fetch", "-data", data, "-pub", projPub, "-url", srv.URL + "/owner/espdns",
			"-version", "v0.0.4", "-catalog", "../../../boards"}, more...)...)
	}
	if _, err := fetch(); err == nil || !strings.Contains(err.Error(), rolling.NothingToGet) {
		t.Fatalf("nothing needed: %v", err)
	}
	if _, err := fetch("-image", "../x"); err == nil || !strings.Contains(err.Error(), "not one of the chip images") {
		t.Fatalf("a bad name: %v", err)
	}
	got, err := fetch("-image", "esp32p4-rev1")
	if err != nil || !strings.Contains(got, "signed by the project's release key") || !strings.HasSuffix(got, ": esp32p4-rev1\n") {
		t.Fatalf("fetch: %v\n%s", err, got)
	}
	// None named: a listed node's board's (ws-s3-eth: esp32s3-eth), not the one imported
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"node_id":"aabbccddeeff","board":"ws-s3-eth","image":"esp32s3-octal"}`))
	}))
	defer node.Close()
	host := strings.TrimPrefix(node.URL, "http://")
	if err := settings.Save(settings.Path(data), settings.Settings{Nodes: []string{host}}); err != nil {
		t.Fatal(err)
	}
	got, err = fetch()
	if err != nil || !strings.Contains(got, "the chip images needed: esp32s3-eth\n") || strings.Contains(got, rolling.UnknownNeeds) ||
		!strings.HasSuffix(got, ": esp32s3-eth\n") {
		t.Fatalf("fetch those needed: %v\n%s", err, got)
	}
	// A node that doesn't answer: what it needs can't be told, so the ones imported too, said
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	if err := settings.Save(settings.Path(data), settings.Settings{Nodes: []string{host, strings.TrimPrefix(gone.URL, "http://")}}); err != nil {
		t.Fatal(err)
	}
	got, err = fetch()
	if err != nil || !strings.Contains(got, rolling.UnknownNeeds) || !strings.Contains(got, "the chip images needed: esp32p4-rev1, esp32s3-eth\n") ||
		!strings.HasSuffix(got, ": esp32p4-rev1, esp32s3-eth\n") {
		t.Fatalf("fetch with a node not answering: %v\n%s", err, got)
	}
	if _, err := fetch("-image", "esp32p4"); err == nil || !strings.Contains(err.Error(), "no chip image esp32p4") {
		t.Fatalf("one not in the release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(data, "firmware", "images", "esp32p4-rev1", dist.ProvenanceDir, dist.SignerFile)); err != nil {
		t.Error(err)
	}
	if b, err := os.ReadFile(filepath.Join(data, "log", "actions.jsonl")); err != nil || !strings.Contains(string(b), `"release fetch"`) {
		t.Errorf("fetch not in the action log: %v %s", err, b)
	}
	// Signed by another key than the project's: refused
	_, other := releaseKey(t)
	if _, err := runRelease(t, "", "fetch", "-data", t.TempDir(), "-pub", other, "-url", srv.URL+"/owner/espdns", "-version", "v0.0.4", "-image", "esp32p4-rev1"); err == nil ||
		!strings.Contains(err.Error(), "not the project's release key") {
		t.Fatalf("fetch with another project key: %v", err)
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
