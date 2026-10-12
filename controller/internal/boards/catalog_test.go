package boards

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The catalog in memory: ordered by maker then title, looked up by name, and the custom
// directory read again when it changes (a file dropped in, or Save), with the ETag following.
func TestCatalog_InMemory(t *testing.T) {
	custom := t.TempDir()
	c := NewCatalog("../../../boards", custom)
	list, etag, err := c.Entries()
	if err != nil || len(list) < 20 || etag == "" {
		t.Fatalf("%d boards, etag %q, %v", len(list), etag, err)
	}
	for i := 1; i < len(list); i++ {
		a, b := list[i-1].Board, list[i].Board
		if makerOf(a) > makerOf(b) || (makerOf(a) == makerOf(b) && titleOf(a) > titleOf(b)) {
			t.Errorf("out of order: %s (%s) before %s (%s)", a.Title, a.Name, b.Title, b.Name)
		}
	}
	if e, ok := c.Find("ws-s3-eth"); !ok || e.Source != "catalog" || e.Board.Title != "Waveshare ESP32-S3-ETH" {
		t.Fatalf("find: %+v %v", e, ok)
	}
	if _, ok := c.Find("nope"); ok {
		t.Fatal("found a board that isn't there")
	}

	// A file dropped into the custom directory shows up, in its maker's place.
	time.Sleep(10 * time.Millisecond) // a new mtime, whatever the file system's resolution
	os.WriteFile(filepath.Join(custom, "mine.json"), []byte(`{"name":"mine","title":"Acme Board","image":"esp32s3-octal","flash_mb":8,"psram_mb":8}`), 0o600)
	list2, etag2, _ := c.Entries()
	if len(list2) != len(list)+1 || etag2 == etag || list2[0].Board.Name != "mine" || list2[0].Source != "custom" {
		t.Fatalf("after a file: %d boards (was %d), etag %q (was %q), first %s", len(list2), len(list), etag2, etag, list2[0].Board.Name)
	}
	// Unchanged, the same list and ETag come back without a read.
	if _, etag3, _ := c.Entries(); etag3 != etag2 {
		t.Fatalf("etag moved with nothing changed: %q then %q", etag2, etag3)
	}

	// Save: a shipped name is refused; a new one is in the list at once.
	if err := c.Save(Board{Name: "ws-s3-eth", Image: "esp32s3-octal"}); err == nil {
		t.Fatal("saved over a catalog board")
	}
	if err := c.Save(Board{Name: "mine2", Title: "Acme Board 2", Image: "esp32s3-octal", PSRAMMB: p(8)}); err != nil {
		t.Fatal(err)
	}
	if e, ok := c.Find("mine2"); !ok || e.Source != "custom" {
		t.Fatalf("saved board: %+v %v", e, ok)
	}
	// A custom file with a shipped board's name is listed with an error, not in its place.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(custom, "clash.json"), []byte(`{"name":"ws-s3-eth","image":"esp32s3-octal"}`), 0o600)
	list4, _, _ := c.Entries()
	clashes := 0
	for _, e := range list4 {
		if e.Board.Name == "ws-s3-eth" && e.Source == "custom" {
			clashes++
			if !has(e.Errors, "already called") {
				t.Errorf("clash without its error: %v", e.Errors)
			}
		}
	}
	if clashes != 1 {
		t.Fatalf("%d clashing entries", clashes)
	}
	if e, _ := c.Find("ws-s3-eth"); e.Source != "catalog" {
		t.Fatal("a custom file shadowed a shipped board")
	}
}

// No custom directory: the shipped boards alone, and nothing to save into.
func TestCatalog_NoCustomDir(t *testing.T) {
	c := NewCatalog("../../../boards", "")
	if list, _, err := c.Entries(); err != nil || len(list) < 20 {
		t.Fatal(len(list), err)
	}
	if err := c.Save(Board{Name: "x", Image: "esp32s3-octal"}); err == nil {
		t.Fatal("saved with no custom directory")
	}
}
