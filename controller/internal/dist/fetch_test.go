package dist

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/release"
)

// forge is a test forge (the /api/v1 API and download paths) serving releases from directories
// by tag, for owner/espdns; it records what was asked for.
type forge struct {
	*httptest.Server
	mu   sync.Mutex
	got  []string
	rels map[string]string // tag -> directory
	list []map[string]any  // the API's list, newest first
}

func newForge(t *testing.T) *forge {
	f := &forge{rels: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.got = append(f.got, r.URL.Path)
		f.mu.Unlock()
		if r.URL.Path == "/api/v1/repos/owner/espdns/releases" {
			json.NewEncoder(w).Encode(f.list)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, "/owner/espdns/releases/download/")
		tag, file, _ := strings.Cut(rest, "/")
		dir := f.rels[tag]
		if !ok || dir == "" || strings.Contains(file, "/") {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, filepath.Join(dir, file))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *forge) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.got)
}

// fetch downloads a release's signed sums and the chip tarballs asked for only, checks them
// with the project's key, and imports them with their provenance; the newest published
// release by default (drafts and other tags skipped).
func TestFetch(t *testing.T) {
	dir, pub := signedRelease(t)
	f := newForge(t)
	f.rels["v0.0.4"] = dir
	f.list = []map[string]any{{"tag_name": "v0.0.5", "draft": true}, {"tag_name": "nightly"}, {"tag_name": "v0.0.4"}, {"tag_name": "v0.0.3"}}
	work := t.TempDir()
	images := filepath.Join(work, "images")
	var log []string
	imp, err := Fetch(context.Background(), FetchOptions{Forge: f.URL + "/owner/espdns", ProjectPub: pub, Images: catalogImages,
		Logf: func(s string, a ...any) { log = append(log, s) }}, work, images)
	if err != nil || imp.Signer.Who != SignerProject || imp.Signer.Version != "0.0.4" || len(imp.Images) != len(catalogImages) ||
		imp.Signer.From != f.URL+"/owner/espdns/releases/tag/v0.0.4" {
		t.Fatalf("%+v %v", imp, err)
	}
	for _, p := range f.asked() {
		if strings.Contains(p, "controller-image") || strings.HasSuffix(p, "/compose.yaml") {
			t.Errorf("downloaded %s: only the chip images are", p)
		}
	}
	if o, err := CheckProvenance(filepath.Join(images, "esp32p4-rev1"), mustRead(t, filepath.Join(images, "esp32p4-rev1", "app.bin")),
		Signers{Project: pub}); err != nil || o.Label != "project release v0.0.4" {
		t.Errorf("provenance: %+v %v", o, err)
	}
	ents, _ := os.ReadDir(work)
	for _, e := range ents {
		if e.Name() != "images" {
			t.Errorf("left in the work directory: %s", e.Name())
		}
	}
	// A version asked for, with or without its v
	for _, v := range []string{"v0.0.4", "0.0.4"} {
		if _, err := Fetch(context.Background(), FetchOptions{Forge: f.URL + "/owner/espdns", Version: v, ProjectPub: pub, Images: []string{"esp32p4"}}, work,
			filepath.Join(t.TempDir(), "images")); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}

// A release the project's key didn't sign is refused after its sums, before any tarball is
// downloaded; a tarball not as signed is refused; nothing is imported either way.
func TestFetchRefuses(t *testing.T) {
	dir, _ := signedRelease(t) // signed by a key that isn't the project's
	project := release.PublicRaw(testKey(t))
	f := newForge(t)
	f.rels["v0.0.4"] = dir
	work := t.TempDir()
	images := filepath.Join(work, "images")
	opts := FetchOptions{Forge: f.URL + "/owner/espdns", Version: "v0.0.4", ProjectPub: project, Images: catalogImages}
	if _, err := Fetch(context.Background(), opts, work, images); err == nil || !strings.Contains(err.Error(), "not the project's release key") {
		t.Fatalf("another key's release: %v", err)
	}
	for _, p := range f.asked() {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Errorf("downloaded %s from a release with a bad signature", p)
		}
	}
	if _, err := os.Stat(images); err == nil {
		t.Error("imported")
	}

	// Signed by the project, a tarball changed on the forge
	dir2, pub := signedRelease(t)
	tb := filepath.Join(dir2, ChipFile("0.0.4", "esp32p4"))
	b, _ := os.ReadFile(tb)
	b[len(b)/2] ^= 1
	os.WriteFile(tb, b, 0o644)
	f.rels["v0.0.4"] = dir2
	opts.ProjectPub = pub
	if _, err := Fetch(context.Background(), opts, work, images); err == nil || !strings.Contains(err.Error(), "not the") {
		t.Fatalf("a changed tarball: %v", err)
	}
	if _, err := os.Stat(images); err == nil {
		t.Error("imported")
	}
	for _, c := range []struct {
		o    FetchOptions
		want string
	}{
		{FetchOptions{Forge: f.URL + "/owner/espdns", Version: "v9.9.9", ProjectPub: pub, Images: catalogImages}, "404"},
		{FetchOptions{Forge: f.URL + "/owner/espdns", ProjectPub: pub, Images: catalogImages}, "no published release"},
		{FetchOptions{Forge: f.URL + "/owner", ProjectPub: pub, Images: catalogImages}, "not a repository"},
		{FetchOptions{Forge: "ftp://example.com/a/b", ProjectPub: pub, Images: catalogImages}, "not a repository"},
		{FetchOptions{Forge: f.URL + "/owner/espdns", Version: "latest", ProjectPub: pub, Images: catalogImages}, "not vMAJOR"},
		{FetchOptions{Forge: f.URL + "/owner/espdns", Images: catalogImages}, "no project public key"},
		{FetchOptions{Forge: f.URL + "/owner/espdns", Version: "v0.0.4", ProjectPub: pub}, "no chip images asked for"},
		{FetchOptions{Forge: f.URL + "/owner/espdns", Version: "v0.0.4", ProjectPub: pub, Images: []string{"../x"}}, "not one of the chip images"},
	} {
		if _, err := Fetch(context.Background(), c.o, work, images); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.o, err, c.want)
		}
	}
}

// Only the chip images asked for are downloaded and imported, after the signature; the
// chip images already imported that weren't asked for stay as they were, provenance and
// all, and each one's provenance still checks. A chip image the release doesn't have is
// refused before any tarball is downloaded, naming the ones it has.
func TestFetchOnly(t *testing.T) {
	dir, pub := signedRelease(t)
	f := newForge(t)
	f.rels["v0.0.4"] = dir
	work := t.TempDir()
	images := filepath.Join(work, "images")
	opts := FetchOptions{Forge: f.URL + "/owner/espdns", Version: "v0.0.4", ProjectPub: pub}

	// Two chip images first, from a full fetch of them
	opts.Images = []string{"esp32p4-rev1", "esp32p4"}
	if imp, err := Fetch(context.Background(), opts, work, images); err != nil || !slices.Equal(imp.Images, []string{"esp32p4-rev1", "esp32p4"}) {
		t.Fatalf("%+v %v", imp, err)
	}
	p4 := filepath.Join(images, "esp32p4-rev1")
	before := mustRead(t, filepath.Join(p4, ProvenanceDir, SignerFile))

	// Then one other: only its tarball is downloaded, after the sums and their signature
	f.mu.Lock()
	f.got = nil
	f.mu.Unlock()
	opts.Images = []string{"esp32s3-eth"}
	imp, err := Fetch(context.Background(), opts, work, images)
	if err != nil || !slices.Equal(imp.Images, []string{"esp32s3-eth"}) {
		t.Fatalf("%+v %v", imp, err)
	}
	var got []string
	for _, p := range f.asked() {
		got = append(got, filepath.Base(p))
	}
	if want := []string{SumsFile, SigFile, ChipFile("0.0.4", "esp32s3-eth")}; !slices.Equal(got, want) {
		t.Errorf("downloaded %v, want %v", got, want)
	}
	ents, _ := os.ReadDir(images)
	var have []string
	for _, e := range ents {
		have = append(have, e.Name())
	}
	if want := []string{"esp32p4", "esp32p4-rev1", "esp32s3-eth"}; !slices.Equal(have, want) {
		t.Errorf("images %v, want %v", have, want)
	}
	if !bytes.Equal(before, mustRead(t, filepath.Join(p4, ProvenanceDir, SignerFile))) {
		t.Error("a chip image not asked for was imported again")
	}
	// Each one's provenance checks: the signed sums list tarballs that were never downloaded
	for _, img := range have {
		d := filepath.Join(images, img)
		if o, err := CheckProvenance(d, mustRead(t, filepath.Join(d, "app.bin")), Signers{Project: pub}); err != nil ||
			o.Label != "project release v0.0.4" {
			t.Errorf("%s: %+v %v", img, o, err)
		}
		prov, _ := os.ReadDir(filepath.Join(d, ProvenanceDir))
		if len(prov) != 4 {
			t.Errorf("%s: provenance %v", img, prov)
		}
	}

	// One the release doesn't have: refused, nothing more downloaded or imported
	sub, k := filepath.Join(t.TempDir(), "dist"), testKey(t)
	if _, err := Build(Options{Version: "0.0.4", Images: fakeChips(t, "0.0.4", catalogImages[:4]...), Catalog: catalog, Out: sub}); err != nil {
		t.Fatal(err)
	}
	if err := Sign(sub, k); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.got, f.rels["v0.0.4"] = nil, sub
	f.mu.Unlock()
	opts.Images, opts.ProjectPub = []string{"esp32s3-eth", "esp32p4"}, release.PublicRaw(k)
	if _, err := Fetch(context.Background(), opts, work, filepath.Join(t.TempDir(), "images")); err == nil ||
		!strings.Contains(err.Error(), "no chip image esp32p4; it has esp32p4-rev1, esp32s3-eth, esp32s3-octal, esp32s3-quad") {
		t.Fatalf("an image not in the release: %v", err)
	}
	for _, p := range f.asked() {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Errorf("downloaded %s for a fetch that was refused", p)
		}
	}

	// The signature is still checked first: another key's release, one image asked for,
	// downloads no tarball
	f.mu.Lock()
	f.got = nil
	f.mu.Unlock()
	opts.Images = []string{"esp32p4"}
	opts.ProjectPub = release.PublicRaw(testKey(t))
	if _, err := Fetch(context.Background(), opts, work, images); err == nil || !strings.Contains(err.Error(), "not the project's release key") {
		t.Fatalf("another key's release: %v", err)
	}
	for _, p := range f.asked() {
		if strings.HasSuffix(p, ".tar.gz") {
			t.Errorf("downloaded %s from a release with a bad signature", p)
		}
	}
}

// The forge's URLs: GitHub's API, else /api/v1; downloads the same everywhere.
func TestForgeURLs(t *testing.T) {
	g, err := ParseForge(DefaultForge)
	if err != nil || g.api() != "https://api.github.com/repos/skitzo2000/espdns/releases?per_page=30" ||
		g.Download("v0.0.4", "SHA256SUMS") != "https://github.com/skitzo2000/espdns/releases/download/v0.0.4/SHA256SUMS" {
		t.Errorf("%+v %v %s", g, err, g.api())
	}
	m, err := ParseForge("https://git.example.com/owner/espdns.git/")
	if err != nil || m.api() != "https://git.example.com/api/v1/repos/owner/espdns/releases?limit=30" || m.Base != "https://git.example.com/owner/espdns" {
		t.Errorf("%+v %v", m, err)
	}
	for _, bad := range []string{"https://example.com/a", "https://example.com/a/b/c", "https://user@example.com/a/b", "file:///a/b", "https://example.com/a/b?x=1"} {
		if _, err := ParseForge(bad); err == nil {
			t.Errorf("%s taken", bad)
		}
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
