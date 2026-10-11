package image

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/boards"
)

// The firmware tree, for the chip images exported by `make export-images` and the factory
// images tools/factory.py made with esptool from them (`make factory BOARD=...`).
const firmware = "../../../firmware"

// Assembling a catalog board in Go gives the same bytes esptool's merge_bin gave, with the
// keys partition tools/factory.py writes from firmware/keys (make factory's default: your
// keys, git-ignored; skipped without them).
func TestAssembleMatchesFactory(t *testing.T) {
	keys := firmwareKeys(t)
	for _, tc := range []struct{ board string }{{"xiao-s3-sense"}, {"p4-ip101"}} {
		want, err := os.ReadFile(filepath.Join(firmware, "dist", tc.board, "dns2-"+tc.board+".bin"))
		if err != nil {
			t.Skipf("no factory image for %s (run make factory in firmware/): %v", tc.board, err)
		}
		entries, err := boards.Load("../../../boards", "")
		if err != nil {
			t.Fatal(err)
		}
		e, ok := boards.Find(entries, tc.board)
		if !ok {
			t.Fatalf("%s not in the catalog", tc.board)
		}
		chip, err := Open(filepath.Join(firmware, "dist", "images", e.Board.Image))
		if err != nil {
			t.Skipf("no exported chip image %s: %v", e.Board.Image, err)
		}
		// The board partition factory.py wrote (the catalog file with the address it was given
		// appended: boards.Pack's JSON of a changed board orders its fields otherwise), so what
		// is compared is the assembly: layout, bootloader header, tables, keys partition.
		part := want[0x12000:0x13000]
		got, err := chip.Assemble(e.Board.FlashMB, part, keys)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			n := len(got)
			if len(want) < n {
				n = len(want)
			}
			for i := 0; i < n; i++ {
				if got[i] != want[i] {
					t.Fatalf("%s: %d bytes vs esptool's %d; first difference at 0x%x", tc.board, len(got), len(want), i)
				}
			}
			t.Fatalf("%s: %d bytes vs esptool's %d", tc.board, len(got), len(want))
		}
	}
}

func TestSetFlashSizeRejectsNonImages(t *testing.T) {
	if _, err := setFlashSize([]byte("not an image at all, just some bytes"), 3); err == nil {
		t.Fatal("accepted something that isn't an ESP image")
	}
}

// Manifest: a board that may run on Wi-Fi gets ESP Web Tools' Improv step after installing
// (no wait time given: its default); a wired one a wait of 0, so it only installs.
func TestManifestImprov(t *testing.T) {
	c := Chip{Version: "0.0.4", ChipFamily: "ESP32-S3"}
	if w, ok := c.Manifest("b", true, "image.bin")["new_install_improv_wait_time"]; ok {
		t.Errorf("Wi-Fi: improv wait %v, want none (the default)", w)
	}
	if w := c.Manifest("b", false, "image.bin")["new_install_improv_wait_time"]; w != 0 {
		t.Errorf("wired: improv wait %v, want 0", w)
	}
}
