package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/keyspart"
)

// The builder refuses an image without the node's address (a node never asks DHCP for one
// on its own) and checks the address as the node does; with a good one it goes on to the
// chip image, which isn't imported here.
func TestBuildNeedsAddress(t *testing.T) {
	b := newBuilder("../../../boards", t.TempDir(), t.TempDir())
	b.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
	mux := http.NewServeMux()
	b.routes(mux)
	for _, c := range []struct{ body, want string }{
		{`{"name":"ws-s3-eth"}`, "network: the node's address"},
		{`{"name":"ws-s3-eth","network":{"address":"192.0.2.60"}}`, "prefix length"},
		{`{"name":"ws-s3-eth","network":{"address":"192.0.2.60/23"}}`, "gateway: required"},
		{`{"name":"ws-s3-eth","network":{"address":"192.0.2.60/23","gateway":"198.51.100.1"}}`, "in the node's network"},
		{`{"name":"ws-s3-eth","network":{"address":"dhcp","gateway":"192.0.2.1"}}`, "only with a static address"},
		{`{"name":"ws-s3-eth","network":{"address":"192.0.2.60/23","gateway":"192.0.2.1"}}`, "isn't imported"},
		{`{"name":"ws-s3-eth","network":{"address":"dhcp"}}`, "isn't imported"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/build", strings.NewReader(c.body)))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s, want %q", c.body, rec.Code, rec.Body.String(), c.want)
		}
	}
}

// The library is served from memory with an ETag: the same list again is a 304; a board
// saved is in the next list (a new ETag), and the build finds it by name.
func TestBoardsListETag(t *testing.T) {
	b := newBuilder("../../../boards", t.TempDir(), t.TempDir())
	mux := http.NewServeMux()
	b.routes(mux)
	get := func(etag string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/boards", nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		mux.ServeHTTP(rec, req)
		return rec
	}
	rec := get("")
	etag := rec.Header().Get("ETag")
	var list []boardView
	if err := json.Unmarshal(rec.Body.Bytes(), &list); rec.Code != http.StatusOK || err != nil || etag == "" || len(list) < 20 {
		t.Fatalf("%d %q %v: %d boards", rec.Code, etag, err, len(list))
	}
	if list[0].Board.Title < list[len(list)-1].Board.Title {
		// ordered by maker then title: Adafruit before Waveshare
	} else {
		t.Fatalf("order: %s … %s", list[0].Board.Title, list[len(list)-1].Board.Title)
	}
	if rec = get(etag); rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("with the ETag: %d %s", rec.Code, rec.Body)
	}
	if rec = get(`"other"`); rec.Code != http.StatusOK {
		t.Fatalf("with another ETag: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/boards", strings.NewReader(
		`{"name":"mine","title":"Acme Board","image":"esp32s3-octal","flash_mb":8,"psram_mb":8}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	rec = get(etag)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") == etag || !strings.Contains(rec.Body.String(), `"name":"mine"`) {
		t.Fatalf("after a save: %d, etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	// Saving over a shipped board is refused; the build finds the saved one by name.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/boards", strings.NewReader(`{"name":"ws-s3-eth","image":"esp32s3-octal"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "catalog board") {
		t.Fatalf("save over a catalog board: %d %s", rec.Code, rec.Body)
	}
	b.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/build", strings.NewReader(`{"name":"mine","network":{"address":"192.0.2.60/24","gateway":"192.0.2.1"}}`)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "isn't imported") {
		t.Fatalf("build of the saved board: %d %s", rec.Code, rec.Body)
	}
}

// testChips writes one imported chip image per catalog image (as export_image.py and
// dist's fake chips do: an ESP bootloader header, a table with the keys partition, a small
// app), so a build assembles and answers with its wifi and wired flags.
func testChips(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, img := range []string{"esp32s3-octal", "esp32s3-eth", "esp32s3-quad", "esp32p4-rev1", "esp32p4"} {
		d := filepath.Join(dir, img)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		boot := make([]byte, 64)
		boot[0], boot[2], boot[3] = 0xE9, 2, 0x2F // magic, DIO, 4 MB / 80 MHz
		entry := func(name string, sub byte, off, size uint32) []byte {
			e := make([]byte, 32)
			e[0], e[1], e[2], e[3] = 0xAA, 0x50, 1, sub
			binary.LittleEndian.PutUint32(e[4:], off)
			binary.LittleEndian.PutUint32(e[8:], size)
			copy(e[12:28], name)
			return e
		}
		tbl := append(entry("keys", 0x42, 0x1b000, 0x2000), bytes.Repeat([]byte{0xFF}, 32)...)
		files := map[string][]byte{"bootloader.bin": boot, "partitions-8mb.bin": tbl, "partitions-4mb.bin": tbl,
			"ota_data_initial.bin": bytes.Repeat([]byte{0xFF}, 0x2000), "app.bin": bytes.Repeat([]byte{0xA5}, 0x3000)}
		for n, b := range files {
			if err := os.WriteFile(filepath.Join(d, n), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		chip := "esp32s3"
		if strings.HasPrefix(img, "esp32p4") {
			chip = "esp32p4"
		}
		info := map[string]any{"image": img, "chip": chip, "chip_family": "ESP32-" + strings.ToUpper(strings.TrimPrefix(chip, "esp32")),
			"offsets": map[string]int{"bootloader.bin": 0, "partitions-8mb.bin": 0x8000, "partitions-4mb.bin": 0x8000,
				"ota_data_initial.bin": 0xf000, "app.bin": 0x20000, "board": 0x12000, "keys": keyspart.Offset},
			"flash_mode": "dio", "flash_freq": "80m", "version": "0.0.5", "built": "2026-11-03T00:00:00Z", "elf_sha256": "00"}
		b, _ := json.Marshal(info)
		if err := os.WriteFile(filepath.Join(d, "image.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A build says what networking the page has to offer: wifi is the chip's (boards.HasWiFi:
// every S3 board, the P4 boards not), wired whether the board has Ethernet fitted as its
// network (an Ethernet add-on that may not be fitted is not: the board ships on Wi-Fi, as
// the XIAO Sense does). The page ticks the Wi-Fi checkbox as the only network when only
// wifi is set, and offers it off by default as a backup when both are.
func TestBuildWifiWired(t *testing.T) {
	b := newBuilder("../../../boards", t.TempDir(), testChips(t))
	b.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
	mux := http.NewServeMux()
	b.routes(mux)
	for _, c := range []struct {
		board       string
		wifi, wired bool
	}{
		{"ws-s3-eth", true, true},      // an S3 with its W5500 fitted: Wi-Fi as a backup
		{"xiao-s3-sense", true, false}, // the XIAO: Wi-Fi as it ships, the add-on optional
		{"p4-ip101", false, true},      // a P4: no Wi-Fi at all, wired only
	} {
		body, _ := json.Marshal(map[string]any{"name": c.board, "network": map[string]any{"address": "192.0.2.60/24", "gateway": "192.0.2.1"}})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/build", bytes.NewReader(body)))
		var out struct {
			WiFi  bool `json:"wifi"`
			Wired bool `json:"wired"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %s", c.board, rec.Code, rec.Body)
		}
		if out.WiFi != c.wifi || out.Wired != c.wired {
			t.Errorf("%s: wifi %v wired %v, want wifi %v wired %v", c.board, out.WiFi, out.Wired, c.wifi, c.wired)
		}
	}
}
