package image

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/keyspart"
)

// firmwareKeys is the keys partition for firmware/keys (your public keys there, git-ignored:
// make factory's default), as a USB install with those keys writes it; skipped without them.
func firmwareKeys(t *testing.T) []byte {
	t.Helper()
	rel, err := os.ReadFile(filepath.Join(firmware, "keys", "release.pub"))
	if err != nil {
		t.Skipf("no firmware/keys: %v", err)
	}
	rec, err := os.ReadFile(filepath.Join(firmware, "keys", "recovery.pub"))
	if err != nil {
		t.Skipf("no firmware/keys: %v", err)
	}
	p, err := keyspart.Install(rel, rec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// testKeys is a keys directory (release.pub, recovery.pub) of two test keys (the host
// tests' vectors, firmware/tests/keys_vectors.json), and its keys partition.
func testKeys(t *testing.T) (string, []byte) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(firmware, "tests", "keys_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Keys map[string]string `json:"keys"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var pubs [][]byte
	for i, k := range []string{"k0", "k1"} {
		pub, err := hex.DecodeString(v.Keys[k])
		if err != nil {
			t.Fatal(err)
		}
		pubs = append(pubs, pub)
		os.WriteFile(filepath.Join(dir, []string{"release.pub", "recovery.pub"}[i]), pub, 0o644)
	}
	p, err := keyspart.Install(pubs[0], pubs[1])
	if err != nil {
		t.Fatal(err)
	}
	return dir, p
}

// csvTable is a partition table CSV (firmware/partitions-*.csv) as gen_esp32part.py writes
// its binary: the entries, then the MD5 entry (its digest left 0xFF here: ParseTable doesn't
// read it), then 0xFF.
func csvTable(t *testing.T, path string) ([]byte, []Partition) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	types := map[string]byte{"app": 0, "data": 1}
	subs := map[string]byte{"nvs": 2, "ota": 0, "phy": 1, "factory": 0, "ota_0": 0x10, "ota_1": 0x11}
	num := func(s string) int {
		v, err := strconv.ParseInt(s, 0, 64)
		if err != nil {
			t.Fatalf("%s: %q: %v", path, s, err)
		}
		return int(v)
	}
	var out []byte
	var ps []Partition
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fl := strings.Split(line, ",")
		for i := range fl {
			fl[i] = strings.TrimSpace(fl[i])
		}
		ty, ok := types[fl[1]]
		if !ok {
			ty = byte(num(fl[1]))
		}
		st, ok := subs[fl[2]]
		if !ok {
			st = byte(num(fl[2]))
		}
		p := Partition{Name: fl[0], Type: ty, SubType: st, Offset: num(fl[3]), Size: num(fl[4])}
		ps = append(ps, p)
		e := make([]byte, 32)
		e[0], e[1], e[2], e[3] = 0xAA, 0x50, ty, st
		binary.LittleEndian.PutUint32(e[4:], uint32(p.Offset))
		binary.LittleEndian.PutUint32(e[8:], uint32(p.Size))
		copy(e[12:28], p.Name)
		out = append(out, e...)
	}
	md5 := bytes.Repeat([]byte{0xFF}, 32)
	md5[0], md5[1] = 0xEB, 0xEB
	out = append(out, md5...)
	return append(out, bytes.Repeat([]byte{0xFF}, 0xC00-len(out))...), ps
}

// Every partition table a node runs (firmware/partitions-*.csv) has the keys partition at
// keyspart.Offset, keyspart.Size, data subtype 0x42, and nothing else overlaps it. (The bench
// apps' tables, firmware/bench, aren't a node's.)
func TestTablesHaveKeys(t *testing.T) {
	n := 0
	err := filepath.WalkDir(firmware, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "bench" || d.Name() == "build" || d.Name() == "managed_components" || d.Name() == "dist") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasPrefix(d.Name(), "partitions") || !strings.HasSuffix(d.Name(), ".csv") {
			return nil
		}
		n++
		bin, ps := csvTable(t, p)
		if err := hasKeysPartition(bin); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		for _, x := range ps {
			if x.Name != "keys" && x.Offset < keyspart.Offset+keyspart.Size && keyspart.Offset < x.Offset+x.Size {
				t.Errorf("%s: %s overlaps the keys partition", p, x.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("%d partition tables found under %s", n, firmware)
	}
}

// fakeChip writes a chip image directory as export_image.py does, with the tables from the
// firmware's CSVs and a small app: keys says whether image.json has the keys offset (an
// export from before the keys partition doesn't), table the CSV for both tables.
func fakeChip(t *testing.T, keys bool, table string) Chip {
	t.Helper()
	dir := t.TempDir()
	boot := make([]byte, 64)
	boot[0] = 0xE9
	tbl, _ := csvTable(t, table)
	files := map[string][]byte{"bootloader.bin": boot, "partitions-8mb.bin": tbl, "partitions-4mb.bin": tbl,
		"ota_data_initial.bin": bytes.Repeat([]byte{0xFF}, 0x2000), "app.bin": bytes.Repeat([]byte{0xA5}, 0x3000)}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	off := map[string]int{"bootloader.bin": 0, "partitions-8mb.bin": 0x8000, "partitions-4mb.bin": 0x8000,
		"ota_data_initial.bin": 0xf000, "app.bin": 0x20000, "board": 0x12000}
	if keys {
		off["keys"] = keyspart.Offset
	}
	b, _ := json.Marshal(map[string]any{"image": "esp32s3-eth", "chip": "esp32s3", "chip_family": "ESP32-S3", "offsets": off,
		"flash_mode": "dio", "flash_freq": "80m", "version": "0.0.9", "built": "now", "elf_sha256": "00"})
	os.WriteFile(filepath.Join(dir, "image.json"), b, 0o644)
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A USB install's image has the keys partition at 0x1b000: slot A with the two public keys
// (generation 1, source USB), slot B erased, exactly as `python tools/keyspart.py` writes it;
// the rest of the image is as before.
func TestAssembleWritesKeys(t *testing.T) {
	keysDir, keys := testKeys(t)
	board := bytes.Repeat([]byte{0x42}, 100)
	for _, mb := range []int{4, 8, 16} {
		c := fakeChip(t, true, filepath.Join(firmware, "partitions-8mb.csv"))
		img, err := c.Assemble(mb, board, keys)
		if err != nil {
			t.Fatalf("%d MB: %v", mb, err)
		}
		if got := img[keyspart.Offset : keyspart.Offset+keyspart.Size]; !bytes.Equal(got, keys) {
			t.Fatalf("%d MB: the keys range isn't the keys partition", mb)
		}
		state, slot, r := keyspart.Read(img[keyspart.Offset:])
		if state != keyspart.StateOK || slot != 0 || r.Generation != 1 || r.Source != keyspart.SrcUSB {
			t.Fatalf("%d MB: %s slot %d %+v", mb, state, slot, r)
		}
		if !bytes.Equal(img[0x12000:0x12000+len(board)], board) || img[0x20000] != 0xA5 || img[0x1d000] != 0xFF {
			t.Fatalf("%d MB: board, app or the gap after the keys wrong", mb)
		}
		// Without keys: the same image, the range erased.
		bare, err := c.Assemble(mb, board, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(bare[keyspart.Offset:keyspart.Offset+keyspart.Size], bytes.Repeat([]byte{0xFF}, keyspart.Size)) {
			t.Fatal("no keys: the range isn't erased")
		}
		copy(bare[keyspart.Offset:], keys)
		if !bytes.Equal(bare, img) {
			t.Fatal("with keys, more than the keys range differs")
		}
	}

	// The same bytes as keyspart.py's partition image of the same keys.
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	out := filepath.Join(t.TempDir(), "keys.bin")
	if b, err := exec.Command(py, filepath.Join(firmware, "tools", "keyspart.py"), filepath.Join(keysDir, "release.pub"),
		filepath.Join(keysDir, "recovery.pub"), out).CombinedOutput(); err != nil {
		t.Fatalf("keyspart.py: %v: %s", err, b)
	}
	want, _ := os.ReadFile(out)
	c := fakeChip(t, true, filepath.Join(firmware, "partitions-4mb.csv"))
	img, err := c.Assemble(4, board, keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(img[keyspart.Offset:keyspart.Offset+keyspart.Size], want) {
		t.Fatal("the keys range differs from keyspart.py's")
	}
}

// A chip image exported before the keys partition (no keys offset, or a table without it)
// is refused, saying to import a current release; so are keys of the wrong size.
func TestAssembleRefusesOldImages(t *testing.T) {
	_, keys := testKeys(t)
	old := filepath.Join(t.TempDir(), "old.csv")
	os.WriteFile(old, []byte("nvs, data, nvs, 0x9000, 0x6000\notadata, data, ota, 0xf000, 0x2000\nphy_init, data, phy, 0x11000, 0x1000\n"+
		"board, data, 0x40, 0x12000, 0x1000\nconfig, data, 0x41, 0x13000, 0x8000\nota_0, app, ota_0, 0x20000, 0x300000\n"), 0o644)
	for name, c := range map[string]Chip{
		"no keys offset":         fakeChip(t, false, filepath.Join(firmware, "partitions-8mb.csv")),
		"a table without keys":   fakeChip(t, true, old),
		"no offset and no table": fakeChip(t, false, old),
	} {
		for _, k := range [][]byte{keys, nil} {
			_, err := c.Assemble(8, []byte{1}, k)
			if err == nil || !strings.Contains(err.Error(), "import a current release") {
				t.Errorf("%s (keys %v): %v", name, k != nil, err)
			}
		}
	}
	c := fakeChip(t, true, filepath.Join(firmware, "partitions-8mb.csv"))
	if _, err := c.Assemble(8, []byte{1}, keys[:100]); err == nil {
		t.Error("a short keys partition taken")
	}
	if _, err := ParseTable(bytes.Repeat([]byte{0x00}, 64)); err == nil {
		t.Error("zeros read as a table")
	}
}
