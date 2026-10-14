package dist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// DefaultForge is where the project's releases are published: its GitHub repository.
// Another (a fork's, or one on a GitHub Enterprise server) is settings.json's
// "release_url", or espdns release fetch -url.
const DefaultForge = "https://github.com/skitzo2000/espdns"

// FetchOptions say which release Fetch gets, from where.
type FetchOptions struct {
	Forge   string // the repository's page, https://<host>/<owner>/<repo> ("": DefaultForge)
	Version string // a tag (firmware-v0.0.3, or v0.0.2 from before), or a version alone (0.0.3: the firmware release the forge lists for it); "": the newest firmware release
	// ProjectPub is the project's release public key (keys/project.pub): the only key a
	// fetched release is taken with.
	ProjectPub []byte
	// Images are the chip images to get (esp32s3-eth), at least one: only their tarballs
	// are downloaded and imported, and the other chip images here stay as they are.
	Images []string
	Client *http.Client         // nil: one with a timeout
	Logf   func(string, ...any) // progress; nil: none
}

// Forge is a GitHub repository (github.com, or a GitHub Enterprise server), from its page's URL.
type Forge struct {
	Base        string // https://<host>/<owner>/<repo>
	host, owner string
	repo        string
}

var forgeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ParseForge reads a repository's URL: https, a host, and /<owner>/<repo>.
func ParseForge(s string) (Forge, error) {
	u, err := url.Parse(strings.TrimSuffix(strings.TrimSpace(s), "/"))
	if err != nil {
		return Forge{}, fmt.Errorf("release URL %q: %v", s, err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		len(parts) != 2 || !forgeName.MatchString(parts[0]) || !forgeName.MatchString(parts[1]) {
		return Forge{}, fmt.Errorf("release URL %q: not a repository's page, https://<host>/<owner>/<repo>", s)
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	return Forge{Base: "https://" + u.Host + "/" + parts[0] + "/" + repo, host: u.Host, owner: parts[0], repo: repo}, nil
}

// api is the forge's releases API: api.github.com, else the server's own (/api/v3).
func (f Forge) api() string {
	if strings.EqualFold(f.host, "github.com") {
		return "https://api.github.com/repos/" + f.owner + "/" + f.repo + "/releases?per_page=30"
	}
	return "https://" + f.host + "/api/v3/repos/" + f.owner + "/" + f.repo + "/releases?per_page=30"
}

// Download is a release file's URL, the same on github.com and a GitHub Enterprise server.
func (f Forge) Download(tag, file string) string {
	return f.Base + "/releases/download/" + url.PathEscape(tag) + "/" + url.PathEscape(file)
}

// Fetch downloads a release's SHA256SUMS and SHA256SUMS.sig, checks the signature with the
// project's key, then downloads the tarballs of the chip images asked for (o.Images), each
// checked against it as it arrives, into a new directory in work (removed after), and
// imports them into images (Import: signer the project, with their provenance). A chip image
// asked for that the release doesn't have is an error. The other files it lists (the other
// chip images, the controller image's reference) are not downloaded, and the chip images
// already in images that weren't asked for are left as they are. The caller holds the fleet
// lock, as for any import.
func Fetch(ctx context.Context, o FetchOptions, work, images string) (Imported, error) {
	var out Imported
	if len(o.ProjectPub) != 65 || o.ProjectPub[0] != 4 {
		return out, errors.New("no project public key (" + ProjectPub + "): a fetched release is checked with it")
	}
	if len(o.Images) == 0 {
		return out, errors.New("no chip images asked for")
	}
	want := map[string]bool{}
	for _, img := range o.Images {
		if !ImageName(img) {
			return out, fmt.Errorf("chip image %q: not one of the chip images", img)
		}
		want[img] = true
	}
	src := o.Forge
	if src == "" {
		src = DefaultForge
	}
	f, err := ParseForge(src)
	if err != nil {
		return out, err
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	c := o.Client
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Minute}
	}
	// A tag as it is; a version alone, or none (the newest), the firmware release the forge
	// lists for it
	tag := o.Version
	v, ok := FirmwareVersion(tag)
	if !ok {
		if tag != "" && !version.MatchString(tag) {
			return out, fmt.Errorf("version %q: not vMAJOR.MINOR.PATCH or firmware-vMAJOR.MINOR.PATCH", o.Version)
		}
		if tag, err = firmwareTag(ctx, c, f, tag); err != nil {
			return out, err
		}
		v, _ = FirmwareVersion(tag)
		if o.Version == "" {
			logf("%s: the newest firmware release is %s", f.Base, tag)
		}
	}

	if err := secfile.MkdirAll(work); err != nil {
		return out, err
	}
	dir, err := os.MkdirTemp(work, ".fetch-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(dir)
	get := func(name string, max int64, sum string) error {
		u := f.Download(tag, name)
		logf("downloading %s", u)
		n, got, err := download(ctx, c, u, filepath.Join(dir, name), max)
		if err != nil {
			return fmt.Errorf("%s: %w", u, err)
		}
		if sum != "" && got != sum {
			return fmt.Errorf("%s: SHA-256 %s, not the %s signed in %s: not imported", name, got, sum, SumsFile)
		}
		logf("%s: %d bytes", name, n)
		return nil
	}
	if err := get(SumsFile, 1<<20, ""); err != nil {
		return out, err
	}
	if err := get(SigFile, 1<<10, ""); err != nil {
		return out, err
	}
	sums, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		return out, err
	}
	sig, err := os.ReadFile(filepath.Join(dir, SigFile))
	if err != nil {
		return out, err
	}
	// The signature first: nothing more is downloaded for a release the key didn't sign.
	if !release.VerifySig(o.ProjectPub, sums, sig) {
		return out, fmt.Errorf("%s of %s is not the project's release key's (%s) signature: not imported",
			SigFile, tag, release.Fingerprint(o.ProjectPub))
	}
	logf("%s: signed by the project's release key %s", SumsFile, release.Fingerprint(o.ProjectPub))
	list, err := ParseSums(sums)
	if err != nil {
		return out, err
	}
	var has []string
	tarball := map[string]Sum{}
	for _, s := range list {
		cv, img, ok := chipOf(s.Name)
		if !ok {
			continue
		}
		if cv != v {
			return out, fmt.Errorf("%s lists %s, not version %s", SumsFile, s.Name, v)
		}
		has = append(has, img)
		tarball[img] = s
	}
	if len(has) == 0 {
		return out, fmt.Errorf("release %s has no chip images", tag)
	}
	// Every chip image asked for is in the release before any is downloaded
	var missing []string
	for _, img := range o.Images {
		if _, ok := tarball[img]; !ok && !slices.Contains(missing, img) {
			missing = append(missing, img)
		}
	}
	if len(missing) > 0 {
		return out, fmt.Errorf("release %s has no chip image %s; it has %s", tag, strings.Join(missing, ", "), strings.Join(has, ", "))
	}
	need := map[string]bool{}
	for _, img := range slices.Sorted(maps.Keys(want)) {
		s := tarball[img]
		if err := get(s.Name, maxTarball, s.SHA256); err != nil {
			return out, err
		}
		need[s.Name] = true
	}
	return importFrom(dir, Signers{Project: o.ProjectPub}, images, f.Base+"/releases/tag/"+tag, need)
}

// FirmwareVersion is the version a firmware release's tag names: firmware-vMAJOR.MINOR.PATCH,
// or vMAJOR.MINOR.PATCH (the firmware and the controller released together, before each had
// its own version). Any other tag, a controller release's (controller-v...) among them, is
// not one.
func FirmwareVersion(tag string) (string, bool) {
	v, ok := strings.CutPrefix(tag, "firmware-v")
	if !ok {
		v, ok = strings.CutPrefix(tag, "v")
	}
	return v, ok && version.MatchString(v)
}

// Latest is the tag of the newest published firmware release on the forge ("":
// DefaultForge): a controller release never makes a node out of date.
func Latest(ctx context.Context, c *http.Client, forge string) (string, error) {
	if forge == "" {
		forge = DefaultForge
	}
	f, err := ParseForge(forge)
	if err != nil {
		return "", err
	}
	return firmwareTag(ctx, c, f, "")
}

// firmwareTag is the tag of the published firmware release (FirmwareVersion; not a draft)
// of version v, or the newest one if v is "". A pre-release counts (every 0.x release is one).
func firmwareTag(ctx context.Context, c *http.Client, f Forge, v string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.api(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("the releases of %s: %w", f.Base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the releases of %s: %s", f.Base, resp.Status)
	}
	var rels []struct {
		Tag   string `json:"tag_name"`
		Draft bool   `json:"draft"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&rels); err != nil {
		return "", fmt.Errorf("the releases of %s: %v", f.Base, err)
	}
	for _, r := range rels {
		if rv, ok := FirmwareVersion(r.Tag); ok && !r.Draft && (v == "" || rv == v) {
			return r.Tag, nil
		}
	}
	if v != "" {
		return "", fmt.Errorf("%s has no published firmware release %s", f.Base, v)
	}
	return "", fmt.Errorf("%s has no published release yet", f.Base)
}

// download writes url's body to path, at most max bytes, and gives its size and SHA-256.
func download(ctx context.Context, c *http.Client, u, path string, max int64) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", errors.New(resp.Status)
	}
	if resp.ContentLength > max {
		return 0, "", fmt.Errorf("%d bytes, more than %d", resp.ContentLength, max)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, h), io.LimitReader(resp.Body, max+1))
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, "", err
	}
	if n > max {
		return n, "", fmt.Errorf("more than %d bytes", max)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
