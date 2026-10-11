package dist

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/image"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/release"
)

// signedRelease is a release of the catalog's chip images, version 0.0.4, with the
// controller image's reference and the user's compose.yaml as CI includes them, signed by a
// throwaway key, and that key's public half.
func signedRelease(t *testing.T) (dir string, pub []byte) {
	t.Helper()
	ref := filepath.Join(t.TempDir(), "espdns-0.0.4-controller-image.txt")
	if err := os.WriteFile(ref, []byte("registry.example.com/owner/espdns-controller:0.0.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(compose, []byte("services:\n  espdns-controller:\n    image: registry.example.com/owner/espdns-controller:0.0.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := build(t, "0.0.4", ref, compose)
	k := testKey(t)
	if err := Sign(out, k); err != nil {
		t.Fatal(err)
	}
	return out, release.PublicRaw(k)
}

// A release is imported if the project's key or yours signed it, and says which; one signed
// by any other key is refused, and so is any import with no key to check it with.
func TestImportSigners(t *testing.T) {
	dir, pub := signedRelease(t)
	other := release.PublicRaw(testKey(t))
	for _, c := range []struct {
		name string
		s    Signers
		who  string
	}{
		{"the project's key", Signers{Project: pub, Own: other}, SignerProject},
		{"your key", Signers{Project: other, Own: pub}, SignerOwn},
		{"your key, no project key here", Signers{Own: pub}, SignerOwn},
		{"yours the project's", Signers{Project: pub, Own: pub}, SignerProject},
	} {
		images := filepath.Join(t.TempDir(), "images")
		imp, err := Import(dir, c.s, images)
		if err != nil || imp.Signer.Who != c.who || imp.Signer.Version != "0.0.4" || len(imp.Images) != len(catalogImages) {
			t.Errorf("%s: %+v %v", c.name, imp, err)
			continue
		}
		sig, err := readSigner(filepath.Join(images, "esp32c6"))
		if err != nil || sig.Who != c.who || sig.PublicKey != hex.EncodeToString(pub) || sig.Version != "0.0.4" || sig.Imported == "" {
			t.Errorf("%s: signer.json %+v %v", c.name, sig, err)
		}
		if o := ReadOrigin(filepath.Join(images, "esp32c6"), c.s); o.Kind != c.who ||
			o.Label != map[string]string{SignerProject: "project release v0.0.4", SignerOwn: "your build v0.0.4"}[c.who] {
			t.Errorf("%s: origin %+v", c.name, o)
		}
	}
	for name, s := range map[string]Signers{"another key": {Project: other}, "other keys": {Project: other, Own: release.PublicRaw(testKey(t))},
		"no key": {}} {
		images := filepath.Join(t.TempDir(), "images")
		if _, err := Import(dir, s, images); err == nil {
			t.Errorf("%s: imported", name)
		}
		if _, err := os.Stat(images); err == nil {
			t.Errorf("%s: wrote %s", name, images)
		}
	}
}

// LoadSigners: the project's public key file, and your release key's public half from the
// data directory; either may be missing.
func TestLoadSigners(t *testing.T) {
	data := t.TempDir()
	proj := filepath.Join(t.TempDir(), "project.pub")
	if s, err := LoadSigners(proj, data); err != nil || s.Project != nil || s.Own != nil {
		t.Fatalf("nothing there: %+v %v", s, err)
	}
	k := testKey(t)
	os.WriteFile(proj, release.PublicRaw(k), 0o644)
	own := testKey(t)
	os.MkdirAll(filepath.Join(data, keys.Dir), 0o700)
	if _, err := keys.Import(keys.Path(data), own, false); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSigners(proj, data)
	if err != nil || !bytes.Equal(s.Project, release.PublicRaw(k)) || !bytes.Equal(s.Own, release.PublicRaw(own)) {
		t.Fatalf("%+v %v", s, err)
	}
	os.WriteFile(proj, []byte("short"), 0o644)
	if _, err := LoadSigners(proj, data); err == nil {
		t.Error("a project key file that isn't a key taken")
	}
}

// Before a push the chain is checked again: signature, sums, tarball, app.bin. Any link
// broken, or a signer no longer trusted, refuses the push; a chip image with no provenance is
// your own build, not checked.
func TestCheckProvenance(t *testing.T) {
	dir, pub := signedRelease(t)
	s := Signers{Project: pub}
	fresh := func() string {
		images := filepath.Join(t.TempDir(), "images")
		if _, err := Import(dir, s, images); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(images, "esp32s3-octal")
	}
	appOf := func(img string) []byte {
		b, err := os.ReadFile(filepath.Join(img, "app.bin"))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	img := fresh()
	o, err := CheckProvenance(img, appOf(img), s)
	if err != nil || o.Kind != SignerProject || o.Version != "0.0.4" || o.Label != "project release v0.0.4" {
		t.Fatalf("as imported: %+v %v", o, err)
	}
	prov := func(img string) string { return filepath.Join(img, ProvenanceDir) }
	tarball := ChipFile("0.0.4", "esp32s3-octal")
	for _, c := range []struct {
		name string
		do   func(img string) []byte // breaks it; the app to push
		want string
	}{
		{"a tampered tarball", func(img string) []byte {
			b, _ := os.ReadFile(filepath.Join(prov(img), tarball))
			b[len(b)/2] ^= 1
			os.WriteFile(filepath.Join(prov(img), tarball), b, 0o600)
			return appOf(img)
		}, "not the tarball signed"},
		{"a tarball repacked with another app.bin", func(img string) []byte {
			other := fakeChips(t, "0.0.4", "esp32s3-octal")
			c, _ := image.Open(filepath.Join(other, "esp32s3-octal"))
			os.WriteFile(filepath.Join(other, "esp32s3-octal", "app.bin"), appImage("0.0.4", "", "evil"), 0o644)
			b, _ := packChip(c)
			os.WriteFile(filepath.Join(prov(img), tarball), b, 0o600)
			return appImage("0.0.4", "", "evil")
		}, "not the tarball signed"},
		{"a swapped app.bin", func(img string) []byte {
			evil := appImage("0.0.4", "", "evil")
			os.WriteFile(filepath.Join(img, "app.bin"), evil, 0o600)
			return evil
		}, "app.bin to push is not the one"},
		{"SHA256SUMS changed", func(img string) []byte {
			b, _ := os.ReadFile(filepath.Join(prov(img), SumsFile))
			os.WriteFile(filepath.Join(prov(img), SumsFile), append(b, "0000000000000000000000000000000000000000000000000000000000000000  x.bin\n"...), 0o600)
			return appOf(img)
		}, "signature over"},
		{"no signature", func(img string) []byte {
			os.Remove(filepath.Join(prov(img), SigFile))
			return appOf(img)
		}, "no such file"},
		{"signed by another key, signer.json changed to it", func(img string) []byte {
			k := testKey(t)
			sig, _ := readSigner(img)
			sig.PublicKey = hex.EncodeToString(release.PublicRaw(k))
			b, _ := json.Marshal(sig)
			os.WriteFile(filepath.Join(prov(img), SignerFile), b, 0o600)
			return appOf(img)
		}, "neither the project's release key nor yours"},
		{"the tarball gone", func(img string) []byte {
			os.Remove(filepath.Join(prov(img), tarball))
			return appOf(img)
		}, "no such file"},
	} {
		img := fresh()
		app := c.do(img)
		if _, err := CheckProvenance(img, app, s); err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "nothing pushed") {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	// The project key it was imported with no longer the project's, nor yours
	img = fresh()
	if _, err := CheckProvenance(img, appOf(img), Signers{Own: release.PublicRaw(testKey(t))}); err == nil {
		t.Error("a signer no longer trusted passed")
	}
	if o := ReadOrigin(img, Signers{}); o.Kind != "" || o.Error == "" {
		t.Errorf("untrusted signer's origin: %+v", o)
	}
	// Your own build: no provenance, nothing to check
	own := filepath.Join(fakeChips(t, "0.0.4", "esp32c6"), "esp32c6")
	if o, err := CheckProvenance(own, []byte("anything"), Signers{}); err != nil || o.Kind != OriginBuild || o.Label != "your build" {
		t.Errorf("own build: %+v %v", o, err)
	}
	if o := ReadOrigin(own, s); o.Kind != OriginBuild {
		t.Errorf("own build's origin: %+v", o)
	}
}
