package dist

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/release"
)

// The supply chain (docs/releasing.md, Using a release): a release is imported only if its
// SHA256SUMS is signed by the project's release key (/keys/project.pub) or by your own
// release key (the controller's, <data>/keys/release.pem), and each chip image imported keeps
// what vouched for it, in <image>/provenance/: the signed SHA256SUMS, its signature, the
// chip image's tarball as checked, and signer.json. Before every firmware rollout from it,
// CheckProvenance checks that chain again, signature, sums, tarball, and the app.bin in the
// tarball is the one pushed; only then does the controller sign the push with your release
// key. A chip image with no provenance/ (make -C controller firmware's copy of your own
// export, firmware/builds/<board>: the fleet's builds) is your own build: nothing signed it,
// and it is pushed as it is.

const (
	// ProjectPub is the project's release public key in the controller's image (keys/
	// project.pub at the top of the repository): it signs the project's releases' SHA256SUMS
	// and nothing else. The nodes never trust it.
	ProjectPub = "/keys/project.pub"
	// ProvenanceDir is where an imported chip image keeps its provenance.
	ProvenanceDir = "provenance"
	// SignerFile in ProvenanceDir says who signed the release (Signer).
	SignerFile = "signer.json"

	// Who signed: the project's release key, or your own release key. OriginBuild is a chip
	// image no release vouches for: your own build.
	SignerProject = "project"
	SignerOwn     = "own"
	OriginBuild   = "build"
)

// Signers are the keys a release may be signed with for the controller to import it.
type Signers struct {
	Project []byte // the project's release public key (ProjectPub); nil if there is none
	Own     []byte // your release key's public half (the controller's); nil if there is none
}

// LoadSigners reads the project's public key (projectPub; "" or a missing file: none) and
// the public half of the release key in dataDir (none set up: none). A key there that can't
// be used is an error.
func LoadSigners(projectPub, dataDir string) (Signers, error) {
	var s Signers
	if projectPub != "" {
		b, err := os.ReadFile(projectPub)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return s, err
		case len(b) != 65 || b[0] != 4:
			return s, fmt.Errorf("%s: %d bytes, not a raw P-256 public key (65)", projectPub, len(b))
		default:
			s.Project = b
		}
	}
	if dataDir != "" {
		k, err := keys.FileSource{Path: keys.Path(dataDir)}.Key()
		switch {
		case errors.Is(err, keys.ErrNoKey):
		case err != nil:
			return s, err
		default:
			s.Own = release.PublicRaw(k)
		}
	}
	return s, nil
}

// Who is who pub is: SignerProject, SignerOwn, or "" for neither. Your release key being
// the project's (the project's own controller) is the project's.
func (s Signers) Who(pub []byte) string {
	switch {
	case len(pub) == 0:
		return ""
	case bytes.Equal(pub, s.Project):
		return SignerProject
	case bytes.Equal(pub, s.Own):
		return SignerOwn
	}
	return ""
}

func (s Signers) candidates() [][]byte {
	var out [][]byte
	for _, k := range [][]byte{s.Project, s.Own} {
		if k != nil {
			out = append(out, k)
		}
	}
	return out
}

func fpOr(pub []byte, none string) string {
	if pub == nil {
		return none
	}
	return release.Fingerprint(pub)
}

// Signer is who signed an imported release (provenance/signer.json).
type Signer struct {
	Who         string `json:"who"`         // SignerProject or SignerOwn, as it was imported
	Fingerprint string `json:"fingerprint"` // the key's, as the nodes name keys
	PublicKey   string `json:"public_key"`  // the 65-byte point, hex
	Version     string `json:"version"`     // the release's version
	Imported    string `json:"imported"`    // when, RFC 3339
	From        string `json:"from,omitempty"`
}

// Imported is what an import did.
type Imported struct {
	Signer Signer
	Images []string
}

// Import checks the release in dir and puts each of its chip images in images (the data
// directory's firmware/images, where the builder and the Nodes page's updates find them),
// replacing one of the same name; every directory 0700 and file 0600 as the data
// directory's are. SHA256SUMS must be signed by one of s (the project's key, else yours):
// anything else is refused, and so is a release with any file not as signed. Nothing is
// imported unless the whole release checks. Each image keeps its provenance.
func Import(dir string, s Signers, images string) (Imported, error) {
	return importFrom(dir, s, images, "", nil)
}

// importFrom is Import; from is where the release came from (Signer.From), and need is
// verify's: nil, the whole release must be there; else only the files it names (a fetch's
// chip images), and only those chip images are imported.
func importFrom(dir string, s Signers, images, from string, need map[string]bool) (Imported, error) {
	var out Imported
	cands := s.candidates()
	if len(cands) == 0 {
		return out, errors.New("no key to check a release with: neither the project's public key (" + ProjectPub +
			") nor a release key of yours (the first run's Make your keys)")
	}
	var c Checked
	var pub []byte
	for _, k := range cands {
		got, err := verify(dir, k, need)
		if errors.Is(err, ErrSignature) {
			continue
		}
		if err != nil {
			return out, err
		}
		c, pub = got, k
		break
	}
	if pub == nil {
		return out, fmt.Errorf("%s is signed by neither the project's release key (%s) nor your release key (%s): not imported",
			SigFile, fpOr(s.Project, "none here"), fpOr(s.Own, "none set up"))
	}
	out.Signer = Signer{Who: s.Who(pub), Fingerprint: release.Fingerprint(pub), PublicKey: hex.EncodeToString(pub),
		Imported: time.Now().UTC().Format(time.RFC3339), From: from}
	var err error
	out.Images, err = importChecked(dir, c, images, &out.Signer)
	return out, err
}

func readSigner(imgDir string) (Signer, error) {
	var sig Signer
	b, err := os.ReadFile(filepath.Join(imgDir, ProvenanceDir, SignerFile))
	if err != nil {
		return sig, err
	}
	if err := json.Unmarshal(b, &sig); err != nil {
		return sig, fmt.Errorf("%s: %w", SignerFile, err)
	}
	return sig, nil
}

// Origin is where a chip image came from, for the pages and a rollout's log.
type Origin struct {
	Kind        string `json:"origin"`                // SignerProject, SignerOwn, OriginBuild; "" if its signer isn't trusted now
	Version     string `json:"version,omitempty"`     // the release's
	Fingerprint string `json:"fingerprint,omitempty"` // the key that signed it
	Label       string `json:"label"`                 // "project release v0.0.3", "your build v0.0.3", "your build"
	Error       string `json:"error,omitempty"`
}

func originOf(sig Signer, who string) Origin {
	o := Origin{Kind: who, Version: sig.Version, Fingerprint: sig.Fingerprint}
	switch who {
	case SignerProject:
		o.Label = "project release v" + sig.Version
	case SignerOwn:
		o.Label = "your build v" + sig.Version
	default:
		o.Label = "release v" + sig.Version + " signed by " + sig.Fingerprint
		o.Error = "signed by " + sig.Fingerprint + ", which is neither the project's release key nor yours now: import it again"
	}
	return o
}

// ReadOrigin says where the chip image in imgDir came from, from its provenance's record
// alone (CheckProvenance checks it).
func ReadOrigin(imgDir string, s Signers) Origin {
	if _, err := os.Stat(filepath.Join(imgDir, ProvenanceDir)); errors.Is(err, fs.ErrNotExist) {
		return Origin{Kind: OriginBuild, Label: "your build"}
	}
	sig, err := readSigner(imgDir)
	if err != nil {
		return Origin{Label: "unreadable provenance", Error: err.Error()}
	}
	pub, _ := hex.DecodeString(sig.PublicKey)
	return originOf(sig, s.Who(pub))
}

// CheckProvenance checks the chip image in imgDir before app, its app.bin as read to push,
// is pushed: with provenance, SHA256SUMS.sig is the signature of the key recorded, which
// must be the project's or yours (s) now, over SHA256SUMS; the tarball kept is the one it
// lists for this chip image; and that tarball's app.bin is app, byte for byte. Without
// provenance it is your own build (OriginBuild), and nothing is checked. Any break in the
// chain is an error, and nothing is pushed.
func CheckProvenance(imgDir string, app []byte, s Signers) (Origin, error) {
	prov := filepath.Join(imgDir, ProvenanceDir)
	if _, err := os.Stat(prov); errors.Is(err, fs.ErrNotExist) {
		return Origin{Kind: OriginBuild, Label: "your build"}, nil
	}
	img := filepath.Base(imgDir)
	fail := func(f string, a ...any) (Origin, error) {
		return Origin{}, fmt.Errorf("chip image %s: its provenance doesn't check: %s; nothing pushed (import the release again)",
			img, fmt.Sprintf(f, a...))
	}
	sig, err := readSigner(imgDir)
	if err != nil {
		return fail("%v", err)
	}
	pub, err := hex.DecodeString(sig.PublicKey)
	if err != nil || len(pub) != 65 {
		return fail("%s: no public key", SignerFile)
	}
	o := originOf(sig, s.Who(pub))
	if o.Kind == "" {
		return fail("%s", o.Error)
	}
	sums, err := os.ReadFile(filepath.Join(prov, SumsFile))
	if err != nil {
		return fail("%v", err)
	}
	sigb, err := os.ReadFile(filepath.Join(prov, SigFile))
	if err != nil {
		return fail("%v", err)
	}
	if !release.VerifySig(pub, sums, sigb) {
		return fail("%s is not %s's signature over %s", SigFile, sig.Fingerprint, SumsFile)
	}
	list, err := ParseSums(sums)
	if err != nil {
		return fail("%v", err)
	}
	var name, want string
	for _, l := range list {
		if v, i, ok := chipOf(l.Name); ok && i == img {
			if name != "" {
				return fail("%s lists chip image %s twice", SumsFile, img)
			}
			name, want = l.Name, l.SHA256
			if v != sig.Version {
				return fail("%s is version %s, %s says %s", l.Name, v, SignerFile, sig.Version)
			}
		}
	}
	if name == "" {
		return fail("%s lists no tarball of chip image %s", SumsFile, img)
	}
	tgz, err := readMax(filepath.Join(prov, name), maxTarball)
	if err != nil {
		return fail("%v", err)
	}
	if sum := sha256.Sum256(tgz); hex.EncodeToString(sum[:]) != want {
		return fail("%s is not the tarball signed (SHA-256 %s)", name, hex.EncodeToString(sum[:]))
	}
	files, err := unpackChip(tgz, img)
	if err != nil {
		return fail("%s: %v", name, err)
	}
	if !bytes.Equal(files["app.bin"], app) {
		return fail("the app.bin to push is not the one in the signed %s", name)
	}
	return o, nil
}
