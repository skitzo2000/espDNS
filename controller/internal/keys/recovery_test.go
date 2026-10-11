package keys

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/release"
)

func TestSetRecovery(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadRecovery(dir); !errors.Is(err, ErrNoRecovery) {
		t.Fatalf("none yet: %v", err)
	}
	rel, _ := newKey(t)
	rec, _ := newKey(t)
	rec2, _ := newKey(t)
	relPub, recPub, rec2Pub := release.PublicRaw(rel), release.PublicRaw(rec), release.PublicRaw(rec2)

	bad := bytes.Clone(recPub)
	bad[64] ^= 1
	for name, pub := range map[string][]byte{"off the curve": bad, "short": recPub[:64], "compressed": append([]byte{2}, recPub[1:33]...), "empty": nil} {
		if _, err := SetRecovery(dir, pub, relPub, false, false); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	if _, err := SetRecovery(dir, relPub, relPub, false, false); err == nil {
		t.Error("the release key taken as the recovery key")
	}
	if same, err := SetRecovery(dir, recPub, relPub, false, false); same || err != nil {
		t.Fatalf("first: %v %v", same, err)
	}
	if got, err := ReadRecovery(dir); err != nil || !bytes.Equal(got, recPub) {
		t.Fatalf("read back: %v", err)
	}
	if same, err := SetRecovery(dir, recPub, relPub, false, true); !same || err != nil {
		t.Fatalf("the same again: %v %v", same, err)
	}
	if _, err := SetRecovery(dir, rec2Pub, relPub, false, false); !errors.Is(err, ErrRecoveryExists) {
		t.Fatalf("another without replace: %v", err)
	}
	if _, err := SetRecovery(dir, rec2Pub, relPub, true, true); !errors.Is(err, ErrRecoveryExists) {
		t.Fatalf("another with nodes listed: %v", err)
	}
	if got, _ := ReadRecovery(dir); !bytes.Equal(got, recPub) {
		t.Fatal("a refused one replaced it")
	}
	if _, err := SetRecovery(dir, rec2Pub, relPub, true, false); err != nil {
		t.Fatalf("replace, no nodes: %v", err)
	}
	if got, _ := ReadRecovery(dir); !bytes.Equal(got, rec2Pub) {
		t.Fatal("not replaced")
	}
	// A file that isn't a key: an error, not "none".
	os.WriteFile(RecoveryPath(dir), []byte("nonsense"), 0o600)
	if _, err := ReadRecovery(dir); err == nil || errors.Is(err, ErrNoRecovery) {
		t.Fatalf("nonsense: %v", err)
	}
}

func TestGenerate(t *testing.T) {
	path := Path(t.TempDir())
	k, err := Generate(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (FileSource{path}).Key()
	if err != nil || !got.Equal(k) {
		t.Fatalf("read back: %v", err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	if _, err := Generate(path); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("again: %v", err)
	}
	if got2, _ := (FileSource{path}).Key(); !got2.Equal(k) {
		t.Fatal("replaced")
	}
	// Anything there, even a file that isn't a key, is never replaced.
	other := filepath.Join(t.TempDir(), "release.pem")
	os.WriteFile(other, []byte("x"), 0o600)
	if _, err := Generate(other); !errors.Is(err, ErrKeyExists) {
		t.Fatalf("over a file: %v", err)
	}
}

func TestPublic(t *testing.T) {
	dir := t.TempDir()
	src := FileSource{Path(dir)}
	if _, _, err := Public(src, dir); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("none: %v", err)
	}
	k, err := Generate(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Public(src, dir); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no recovery: %v", err)
	}
	rec, _ := newKey(t)
	if _, err := SetRecovery(dir, release.PublicRaw(rec), release.PublicRaw(k), false, false); err != nil {
		t.Fatal(err)
	}
	rp, cp, err := Public(src, dir)
	if err != nil || !bytes.Equal(rp, release.PublicRaw(k)) || !bytes.Equal(cp, release.PublicRaw(rec)) {
		t.Fatalf("both: %v", err)
	}
	os.Remove(src.Path)
	if _, _, err := Public(src, dir); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no release key: %v", err)
	}
}
