package keyspart

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const firmware = "../../../firmware"

type vectors struct {
	Keys         map[string]string `json:"keys"`
	Fingerprints map[string]string `json:"fingerprints"`
	Packs        []struct {
		Name       string  `json:"name"`
		Release    string  `json:"release"`
		Recovery   string  `json:"recovery"`
		Retired    *string `json:"retired"`
		Generation uint32  `json:"generation"`
		Source     uint8   `json:"source"`
		Seq        string  `json:"seq"`
		Record     string  `json:"record"`
		PartSHA    string  `json:"partition_sha256"`
	} `json:"packs"`
	Parses []struct {
		Name   string  `json:"name"`
		Record string  `json:"record"`
		Error  *string `json:"error"`
	} `json:"parses"`
	Picks []struct {
		Name       string  `json:"name"`
		A          string  `json:"a"`
		B          string  `json:"b"`
		State      string  `json:"state"`
		Slot       *int    `json:"slot"`
		Generation *uint32 `json:"generation"`
	} `json:"picks"`
}

func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(firmware, "tests", "keys_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Packs) == 0 || len(v.Parses) == 0 || len(v.Picks) == 0 {
		t.Fatal("keys_vectors.json: empty")
	}
	return v
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFingerprints(t *testing.T) {
	v := load(t)
	for k, pub := range v.Keys {
		if got := Fingerprint(unhex(t, pub)); got != v.Fingerprints[k] {
			t.Errorf("%s: %s, want %s", k, got, v.Fingerprints[k])
		}
	}
}

// Every pack gives keyspart.py's record and partition, byte for byte, and reads back.
func TestPacks(t *testing.T) {
	v := load(t)
	for _, p := range v.Packs {
		seq, err := strconv.ParseUint(p.Seq, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		r := Record{Generation: p.Generation, Source: p.Source, Seq: seq,
			Release: unhex(t, v.Keys[p.Release]), Recovery: unhex(t, v.Keys[p.Recovery])}
		if p.Retired != nil {
			r.Retired = unhex(t, v.Keys[*p.Retired])
		}
		rec, err := Pack(r)
		if err != nil {
			t.Fatalf("%s: %v", p.Name, err)
		}
		if hex.EncodeToString(rec) != p.Record {
			t.Errorf("%s: record differs from keyspart.py's", p.Name)
		}
		sum := sha256.Sum256(Partition(rec, nil))
		if hex.EncodeToString(sum[:]) != p.PartSHA {
			t.Errorf("%s: partition differs from keyspart.py's", p.Name)
		}
		got, err := Parse(rec)
		if err != nil || got.Generation != r.Generation || got.Source != r.Source || got.Seq != r.Seq ||
			!bytes.Equal(got.Release, r.Release) || !bytes.Equal(got.Recovery, r.Recovery) || !bytes.Equal(got.Retired, r.Retired) {
			t.Errorf("%s: read back %+v, %v", p.Name, got, err)
		}
	}
}

// Every record is read as keyspart.py reads it: valid, or refused for the same reason.
func TestParses(t *testing.T) {
	v := load(t)
	for _, p := range v.Parses {
		_, err := Parse(unhex(t, p.Record))
		switch {
		case p.Error == nil && err != nil:
			t.Errorf("%s: %v, want valid", p.Name, err)
		case p.Error != nil && (err == nil || err.Error() != *p.Error):
			t.Errorf("%s: %v, want %q", p.Name, err, *p.Error)
		}
	}
	if _, err := Parse(make([]byte, 100)); err == nil || err.Error() != "short slot" {
		t.Errorf("short: %v", err)
	}
}

// Every pair of slots is picked as keyspart.py picks it.
func TestPicks(t *testing.T) {
	v := load(t)
	slot := func(s string) []byte {
		if s == "" {
			return bytes.Repeat([]byte{0xFF}, SlotSize)
		}
		return Slot(unhex(t, s))
	}
	for _, p := range v.Picks {
		a, b := slot(p.A), slot(p.B)
		state, i, r := Pick(a, b)
		want := -1
		if p.Slot != nil {
			want = *p.Slot
		}
		if state != p.State || i != want {
			t.Errorf("%s: %s slot %d, want %s slot %d", p.Name, state, i, p.State, want)
			continue
		}
		if p.Generation != nil && r.Generation != *p.Generation {
			t.Errorf("%s: generation %d, want %d", p.Name, r.Generation, *p.Generation)
		}
		if s2, i2, _ := Read(append(append([]byte{}, a...), b...)); s2 != state || i2 != i {
			t.Errorf("%s: Read %s %d", p.Name, s2, i2)
		}
	}
}

func TestPackRefuses(t *testing.T) {
	v := load(t)
	k0, k1 := unhex(t, v.Keys["k0"]), unhex(t, v.Keys["k1"])
	bad := append(bytes.Clone(k0[:64]), k0[64]^1)
	for name, r := range map[string]Record{
		"same keys":          {Release: k0, Recovery: k0, Generation: 1, Source: SrcUSB},
		"release off curve":  {Release: bad, Recovery: k1, Generation: 1, Source: SrcUSB},
		"recovery short":     {Release: k0, Recovery: k1[:64], Generation: 1, Source: SrcUSB},
		"retired off curve":  {Release: k0, Recovery: k1, Retired: bad, Generation: 1, Source: SrcUSB},
		"invalid generation": {Release: k0, Recovery: k1, Generation: GenInvalid, Source: SrcUSB},
	} {
		if _, err := Pack(r); err == nil {
			t.Errorf("%s: packed", name)
		}
	}
}

// Install is what `python tools/keyspart.py release.pub recovery.pub keys.bin` writes.
func TestInstallMatchesKeyspartPy(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	v := load(t)
	dir := t.TempDir()
	rel, rec := filepath.Join(dir, "release.pub"), filepath.Join(dir, "recovery.pub")
	os.WriteFile(rel, unhex(t, v.Keys["k1"]), 0o644)
	os.WriteFile(rec, unhex(t, v.Keys["k2"]), 0o644)
	out := filepath.Join(dir, "keys.bin")
	if b, err := exec.Command(py, filepath.Join(firmware, "tools", "keyspart.py"), rel, rec, out).CombinedOutput(); err != nil {
		t.Fatalf("keyspart.py: %v: %s", err, b)
	}
	want, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Install(unhex(t, v.Keys["k1"]), unhex(t, v.Keys["k2"]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("Install differs from keyspart.py's partition image")
	}
}
