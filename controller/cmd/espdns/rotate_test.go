package main

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/rotation/rotationtest"
)

// espdns key rotate: prepare, sign (the recovery key as PKCS#8 or SEC1 PEM, or a line of
// base64 of either, on standard input or in a file; any other key refused), push.
func TestKeyRotateCLI(t *testing.T) {
	f := rotationtest.New(t, 2)
	var out bytes.Buffer
	if err := rotatePrepare([]string{"-data", f.Data, "-new-release", "generate"}, &out); err != nil {
		t.Fatal(err)
	}
	p := rotation.Pending(f.Data)
	if p == nil || !strings.Contains(out.String(), "--network none") || !strings.Contains(out.String(), p.ID) {
		t.Fatalf("prepare said:\n%s", out.String())
	}
	dir := filepath.Join(rotation.Root(f.Data), p.ID)

	pkcs8, err := keys.Encode(f.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(f.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	sec1 := append([]byte("-----BEGIN EC PARAMETERS-----\nBggqhkjOPQMBBw==\n-----END EC PARAMETERS-----\n"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})...)
	line := func(b []byte) []byte { return []byte(base64.StdEncoding.EncodeToString(b) + "\n") }
	for name, k := range map[string][]byte{"pkcs8": pkcs8, "sec1": sec1, "pkcs8 base64": line(pkcs8), "sec1 base64": line(sec1)} {
		out.Reset()
		if err := rotateSign([]string{"-dir", dir, "-key", "-"}, bytes.NewReader(k), &out); err != nil {
			t.Fatalf("%s on standard input: %v", name, err)
		}
		if !strings.Contains(out.String(), f.Addrs[0]) || !strings.Contains(out.String(), p.New.Release) {
			t.Errorf("%s: sign said %q", name, out.String())
		}
	}
	file := filepath.Join(t.TempDir(), "recovery.pem")
	os.WriteFile(file, line(sec1), 0o600)
	if err := rotateSign([]string{"-dir", dir, "-key", file}, nil, &out); err != nil {
		t.Fatalf("from a file: %v", err)
	}
	relPEM, _ := keys.Encode(f.Release)
	for name, k := range map[string][]byte{"the release key": relPEM, "another key": func() []byte { b, _ := keys.Encode(rotationtest.Key(t)); return b }(),
		"not a key": []byte("hello\n")} {
		if err := rotateSign([]string{"-dir", dir, "-key", "-"}, bytes.NewReader(k), &out); err == nil {
			t.Errorf("signed with %s", name)
		} else if strings.Contains(err.Error(), "MII") {
			t.Errorf("the error shows the key: %v", err)
		}
	}
	// The signing wrote nothing but the releases: no key in the rotation's directory.
	secret := f.Recovery.D.FillBytes(make([]byte, 32))
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if b, _ := os.ReadFile(path); bytes.Contains(b, []byte("PRIVATE KEY")) || bytes.Contains(b, secret) ||
			bytes.Contains(b, []byte(base64.StdEncoding.EncodeToString(der))) {
			t.Errorf("%s holds the recovery private key", path)
		}
		return nil
	})

	if err := rotatePush([]string{"-data", f.Data, "-dir", p.ID, "-healthy", "2s"}); err != nil {
		t.Fatal(err)
	}
	cur, err := keys.FileSource{Path: keys.Path(f.Data)}.Key()
	if err != nil || keys.Fingerprint(cur) != p.New.Release {
		t.Fatalf("release.pem after the push: %v", err)
	}
	if err := rotateDiscard([]string{"-data", f.Data, "-dir", p.ID}, &out); err == nil {
		t.Error("discarded a finished rotation")
	}
}
