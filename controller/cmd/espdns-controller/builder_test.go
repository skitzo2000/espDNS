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
