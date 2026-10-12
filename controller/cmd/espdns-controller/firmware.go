package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/dist"
	"github.com/skitzo2000/espdns/controller/internal/image"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The firmware the controller has, and Get firmware (docs/releasing.md, Using a release):
//
//	GET  /api/firmware   the imported chip images, each with where it came from ("project
//	                     release v0.0.3", "your build v0.0.3", "your build"), and where a
//	                     release is fetched from
//	POST /api/jobs {"kind": "fetch-release", "params": {"version": "v0.0.3", "images": ["esp32s3-eth"]}}
//	                     the job: espdns release fetch (version optional: the newest;
//	                     images optional: the chip images the listed nodes need,
//	                     rolling.NeededImages)
//
// The read is open like /api/images (nothing it reads is a secret); the job needs a login,
// as every job.

type firmwareServer struct {
	dataDir string
	forge   string // the repository releases come from when settings.json names none
}

func (f firmwareServer) routes(mux *routes) {
	mux.HandleFunc("GET /api/firmware", f.list)
}

// firmwareImage is one imported chip image, as the pages show it.
type firmwareImage struct {
	Image   string `json:"image"`
	Version string `json:"version"`
	Built   string `json:"built,omitempty"`
	dist.Origin
}

// releaseFrom is the repository a release is fetched from: settings.json's "release_url",
// else the controller's -release-url (the project's).
func releaseFrom(dataDir, fallback string) (string, error) {
	s, err := settings.Load(settings.Path(dataDir))
	if err != nil {
		return "", err
	}
	if s.ReleaseURL != "" {
		return s.ReleaseURL, nil
	}
	return fallback, nil
}

func (f firmwareServer) list(w http.ResponseWriter, r *http.Request) {
	images := filepath.Join(f.dataDir, "firmware", "images")
	chips, err := image.List(images)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	s, serr := dist.LoadSigners(rolling.ProjectPub, f.dataDir)
	out := []firmwareImage{}
	for _, c := range chips {
		fi := firmwareImage{Image: c.Image, Version: c.Version, Built: c.Built, Origin: dist.ReadOrigin(c.Dir(), s)}
		if serr != nil && fi.Error == "" {
			fi.Error = serr.Error()
		}
		out = append(out, fi)
	}
	forge, err := releaseFrom(f.dataDir, f.forge)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	fp := ""
	if s.Project != nil {
		fp = release.Fingerprint(s.Project)
	}
	writeJSON(w, map[string]any{"images": out, "release_url": forge, "project_key": fp})
}

// fetchKind is the job "fetch-release": the chip images needed from a published release
// downloaded, checked with the project's key and imported, as espdns release fetch (the
// runner holds the fleet lock, as for every job).
type fetchKind struct {
	dataDir string
	forge   string              // as firmwareServer's
	catalog string              // the board catalog: the chip image each node's board runs
	nodes   func() []nodes.Node // the nodes as last polled; nil: none
	client  *http.Client        // nil: dist.Fetch's
}

type fetchParams struct {
	Version string `json:"version,omitempty"`
	// Images are the chip images to get; none: those needed (fetchKind.needed).
	Images []string `json:"images,omitempty"`
	// unknown: Images are those needed, and a listed node had no status (the job says so)
	unknown bool
}

// fetchResult is what the job imported.
type fetchResult struct {
	Version     string   `json:"version"`
	Images      []string `json:"images"`
	Label       string   `json:"label"`
	Fingerprint string   `json:"fingerprint"`
	From        string   `json:"from"`
}

func (k fetchKind) kind(raw json.RawMessage) (jobs.Func, error) {
	var p fetchParams
	if len(bytes.TrimSpace(raw)) > 0 {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&p); err != nil {
			return nil, fmt.Errorf("params: %v", err)
		}
	}
	if v := strings.TrimPrefix(p.Version, "v"); p.Version != "" && !validVersion(v) {
		return nil, fmt.Errorf(`params: "version" %q: v and MAJOR.MINOR.PATCH`, p.Version)
	}
	for _, img := range p.Images {
		if !dist.ImageName(img) {
			return nil, fmt.Errorf(`params: "images": %q is not a chip image's name (esp32s3-eth)`, img)
		}
	}
	if _, err := k.projectKey(); err != nil {
		return nil, err
	}
	if _, err := releaseFrom(k.dataDir, k.forge); err != nil {
		return nil, err
	}
	if len(p.Images) == 0 {
		if p.Images, p.unknown = k.needed(); len(p.Images) == 0 {
			return nil, errors.New(rolling.NothingToGet)
		}
	}
	return func(ctx context.Context, run *jobs.Run) (any, error) { return k.run(ctx, run, p) }, nil
}

func validVersion(v string) bool {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return false
	}
	for _, x := range parts {
		if x == "" || len(x) > 9 || strings.Trim(x, "0123456789") != "" || (len(x) > 1 && x[0] == '0') {
			return false
		}
	}
	return true
}

// needed are the chip images the controller needs (rolling.NeededImages): its listed nodes'
// (settings.json's, by their last /status); with those already imported if one of them has
// no status yet (unknown).
func (k fetchKind) needed() (imgs []string, unknown bool) {
	var sts []release.NodeStatus
	if k.nodes != nil {
		for _, n := range k.nodes() {
			if n.Source != nodes.SourceSettings {
				continue
			}
			if n.Status == nil {
				unknown = true
				continue
			}
			b, _ := n.Status["board"].(string)
			i, _ := n.Status["image"].(string)
			sts = append(sts, release.NodeStatus{Board: b, Image: i})
		}
	}
	return rolling.NeededImages(k.dataDir, k.catalog, sts, unknown), unknown
}

// projectKey is the project's release public key, the only one a fetched release is taken with.
func (k fetchKind) projectKey() ([]byte, error) {
	s, err := dist.LoadSigners(rolling.ProjectPub, "")
	if err != nil {
		return nil, err
	}
	if s.Project == nil {
		return nil, fmt.Errorf("no project public key (%s): a fetched release is checked with it. Run the controller's image, "+
			"or import a release you have with espdns release import", rolling.ProjectPub)
	}
	return s.Project, nil
}

func (k fetchKind) run(ctx context.Context, run *jobs.Run, p fetchParams) (any, error) {
	pub, err := k.projectKey()
	if err != nil {
		return nil, err
	}
	forge, err := releaseFrom(k.dataDir, k.forge)
	if err != nil {
		return nil, err
	}
	if p.unknown {
		run.Logf("%s", rolling.UnknownNeeds)
	}
	run.Logf("getting %s of %s from %s, checked with the project's release key %s", strings.Join(p.Images, ", "),
		map[bool]string{true: "the newest release", false: p.Version}[p.Version == ""], forge, release.Fingerprint(pub))
	fw := filepath.Join(k.dataDir, "firmware")
	imp, err := dist.Fetch(ctx, dist.FetchOptions{Forge: forge, Version: p.Version, ProjectPub: pub, Images: p.Images, Client: k.client, Logf: run.Logf},
		fw, filepath.Join(fw, "images"))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, errors.New("stopped: nothing imported")
		}
		return nil, err
	}
	res := fetchResult{Version: imp.Signer.Version, Images: imp.Images, Label: "project release v" + imp.Signer.Version,
		Fingerprint: imp.Signer.Fingerprint, From: imp.Signer.From}
	run.Logf("imported %s: %s", res.Label, strings.Join(imp.Images, ", "))
	return res, nil
}

// projectPubThere says, at startup, whether the project's public key is there.
func projectPubThere(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 65 {
		return fmt.Sprintf("%s: none (%v): only releases signed with your own release key can be imported", path, err)
	}
	return fmt.Sprintf("%s: the project's release key %s (it checks the project's releases; the nodes never trust it)", path, release.Fingerprint(b))
}
