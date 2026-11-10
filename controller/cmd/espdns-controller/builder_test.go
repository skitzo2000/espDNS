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

	"github.com/skitzo2000/espdns/controller/internal/boards"
	"github.com/skitzo2000/espdns/controller/internal/image"
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

// TestBuildWifiFlag: the Install over USB Wi-Fi rules (#73) come from the board and its
// image, not from the chip alone. espressif-p4-func-ev has its ESP32-C6 described, so on
// esp32p4-c6 it takes a Wi-Fi network over USB and shows the box (wired board with a Wi-Fi
// backup, #78); a P4 board whose file names no companion takes none and shows no box, even
// when its notes say a C6 sits on the board. wifiTakes/hasEthernet/MayRunOnWiFi are what
// builder.go's make() puts into the build's wifi, wifi_box and wired; the build itself stops
// at "isn't imported" here (no chip images in the test dir), so the flags are asserted on
// the same three functions with a chip image version that has the Wi-Fi backup (wifiBackup).
func TestBuildWifiFlag(t *testing.T) {
	entries, err := boards.Load("../../../boards", "")
	if err != nil {
		t.Fatal(err)
	}
	find := func(name string) boards.Board {
		for _, e := range entries {
			if e.Board.Name == name {
				if len(e.Errors) > 0 {
					t.Fatalf("%s: %v", name, e.Errors)
				}
				return e.Board
			}
		}
		t.Fatalf("no %s in the catalog", name)
		return boards.Board{}
	}
	backup := image.Chip{Version: "0.0.7"} // wifiBackup and newer: a wired board takes a network
	for _, c := range []struct {
		board   string
		wifi    bool // takes a Wi-Fi network over USB
		wifiBox bool // with the Wi-Fi checkbox
		wired   bool // on its cable as it ships
	}{
		{"espressif-p4-func-ev", true, true, true},
		{"p4-ip101", false, false, true},
		{"ws-p4-nano", false, false, true}, // its notes name a C6; its image drives no radio
		{"ws-s3-eth", true, true, true},
		{"xiao-s3-sense", true, false, false},
	} {
		b := find(c.board)
		wifi := wifiTakes(b, backup)
		if wifi != c.wifi || (wifi && hasEthernet(b)) != c.wifiBox || !b.MayRunOnWiFi() != c.wired {
			t.Errorf("%s: wifi %v (want %v), box %v (want %v), wired %v (want %v)", c.board,
				wifi, c.wifi, wifi && hasEthernet(b), c.wifiBox, !b.MayRunOnWiFi(), c.wired)
		}
	}

	// The catalog board builds as far as the missing chip image: its wifi.companion passes
	// the checks, so the answer is "isn't imported" and not a board error.
	bd := newBuilder("../../../boards", t.TempDir(), t.TempDir())
	bd.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
	mux := http.NewServeMux()
	bd.routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/build",
		strings.NewReader(`{"name":"espressif-p4-func-ev","network":{"address":"192.0.2.60/23","gateway":"192.0.2.1"}}`)))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "esp32p4-c6 isn't imported") {
		t.Errorf("espressif-p4-func-ev: %d %s", rec.Code, rec.Body.String())
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

// testChips writes one imported chip image per catalog image, of firmware version v (as
// export_image.py does: an ESP bootloader header, a table with the keys partition, a small
// app), so a build assembles.
func testChips(t *testing.T, v string) string {
	t.Helper()
	dir := t.TempDir()
	entry := func(name string, sub byte, off, size uint32) []byte {
		e := make([]byte, 32)
		e[0], e[1], e[2], e[3] = 0xAA, 0x50, 1, sub
		binary.LittleEndian.PutUint32(e[4:], off)
		binary.LittleEndian.PutUint32(e[8:], size)
		copy(e[12:28], name)
		return e
	}
	tbl := append(entry("keys", 0x42, keyspart.Offset, 0x2000), bytes.Repeat([]byte{0xFF}, 32)...)
	for _, img := range []string{"esp32s3-octal", "esp32s3-eth", "esp32s3-quad", "esp32p4-rev1", "esp32p4"} {
		d := filepath.Join(dir, img)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		boot := make([]byte, 64)
		boot[0], boot[2], boot[3] = 0xE9, 2, 0x2F // magic, DIO, 4 MB / 80 MHz
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
		info := map[string]any{"image": img, "chip": chip, "chip_family": strings.ToUpper(strings.Replace(chip, "esp32", "ESP32-", 1)),
			"offsets": map[string]int{"bootloader.bin": 0, "partitions-8mb.bin": 0x8000, "partitions-4mb.bin": 0x8000,
				"ota_data_initial.bin": 0xf000, "app.bin": 0x20000, "board": 0x12000, "keys": keyspart.Offset},
			"flash_mode": "dio", "flash_freq": "80m", "version": v, "built": "2026-11-10T00:00:00Z", "elf_sha256": "00"}
		b, _ := json.Marshal(info)
		if err := os.WriteFile(filepath.Join(d, "image.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A build says what the page does about Wi-Fi: wifi, the install can set the network over
// USB (a chip with Wi-Fi, no P4; on a wired board only from wifiBackup on); wifi_box, only
// when the Wi-Fi checkbox is ticked (a board with Ethernet, fitted or an add-on; a board
// with no Ethernet always gets its Wi-Fi set); wired, the board is on its cable as it ships
// (the box starts unticked: Wi-Fi as its backup).
func TestBuildWiFiWired(t *testing.T) {
	for _, c := range []struct {
		board, fw        string
		wifi, box, wired bool
	}{
		{"ws-s3-eth", "0.0.7", true, true, true},       // wired S3: Wi-Fi as its backup, a choice
		{"ws-s3-eth", "0.1.0", true, true, true},       // and on any later firmware
		{"ws-s3-eth", "0.0.6", false, false, true},     // firmware that takes no network on a wired board
		{"ws-s3-eth", "3fbae0a", false, false, true},   // a build from before versions
		{"m5stack-cores3", "0.0.6", true, true, false}, // ships on Wi-Fi, an optional add-on: a box, ticked
		{"xiao-s3-sense", "0.0.7", true, false, false}, // Wi-Fi only: no box, Wi-Fi always set
		{"xiao-s3-expansion", "0.0.6", true, false, false},
		{"p4-ip101", "0.0.7", false, false, true}, // a P4: no Wi-Fi
	} {
		b := newBuilder("../../../boards", t.TempDir(), testChips(t, c.fw))
		b.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
		mux := http.NewServeMux()
		b.routes(mux)
		body, _ := json.Marshal(map[string]any{"name": c.board, "network": map[string]any{"address": "192.0.2.60/24", "gateway": "192.0.2.1"}})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/build", bytes.NewReader(body)))
		var out struct {
			WiFi  bool `json:"wifi"`
			Box   bool `json:"wifi_box"`
			Wired bool `json:"wired"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s on %s: %d %s", c.board, c.fw, rec.Code, rec.Body)
		}
		if out.WiFi != c.wifi || out.Box != c.box || out.Wired != c.wired {
			t.Errorf("%s on %s: wifi %v box %v wired %v, want wifi %v box %v wired %v", c.board, c.fw,
				out.WiFi, out.Box, out.Wired, c.wifi, c.box, c.wired)
		}
	}
}
