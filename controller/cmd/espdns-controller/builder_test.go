package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/boards"
	"github.com/skitzo2000/espdns/controller/internal/image"
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

// A build's manifest: for a board that may run on Wi-Fi, no Improv wait of 0, so ESP Web
// Tools asks for the Wi-Fi network over Improv after installing (the image erases NVS); for
// a wired board, a wait of 0, so it only installs.
func TestBuildManifestImprov(t *testing.T) {
	b := newBuilder("../../../boards", t.TempDir(), t.TempDir())
	b.keys = func() ([]byte, []byte, error) { return newPub(t), newPub(t), nil }
	mux := http.NewServeMux()
	b.routes(mux)
	entries, err := boards.Load("../../../boards", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		board string
		wifi  bool
	}{{"xiao-s3-sense", true}, {"ws-s3-eth", false}, {"p4-ip101", false}} {
		e, ok := boards.Find(entries, c.board)
		if !ok {
			t.Fatalf("%s not in the catalog", c.board)
		}
		b.builds["b-"+c.board] = &build{ID: "b-" + c.board, Board: e.Board, Chip: image.Chip{Version: "0.0.4", ChipFamily: "ESP32-S3"}}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/build/b-"+c.board+"/manifest.json", nil))
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); rec.Code != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %s", c.board, rec.Code, rec.Body)
		}
		wait, has := m["new_install_improv_wait_time"]
		if c.wifi && has {
			t.Errorf("%s (Wi-Fi): improv wait %v, want ESP Web Tools' default", c.board, wait)
		}
		if !c.wifi && wait != float64(0) {
			t.Errorf("%s (wired): improv wait %v, want 0", c.board, wait)
		}
		if m["name"] != "espDNS "+c.board || m["new_install_prompt_erase"] != true {
			t.Errorf("%s: manifest %s", c.board, rec.Body)
		}
	}
}
