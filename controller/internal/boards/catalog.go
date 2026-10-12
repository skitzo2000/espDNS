package boards

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Catalog is the board library in memory, so a request never reads the files again. The
// shipped boards (the catalog directory, inside the image) load once; the user's boards
// (data/boards) are read again only when that directory changes, or right after Save. The
// files stay the source of truth: one JSON per board, as boards/README.md describes.
type Catalog struct {
	customDir string

	mu      sync.Mutex
	shipped []Entry
	err     error  // the shipped catalog couldn't be read: every listing says so
	stamp   string // the custom directory as last scanned (names, sizes, mtimes)
	list    []Entry
	etag    string
}

// NewCatalog loads the shipped boards from catalogDir and scans customDir ("" for none).
func NewCatalog(catalogDir, customDir string) *Catalog {
	c := &Catalog{customDir: customDir}
	c.shipped, c.err = loadDir(catalogDir, "catalog", map[string]bool{})
	c.rescan()
	return c
}

// Entries is every board, shipped and custom, in a fixed order (maker, then title), with
// an ETag that changes when the list does. The custom directory is read again if it changed.
func (c *Catalog) Entries() ([]Entry, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return nil, "", c.err
	}
	if c.stamp != stampOf(c.customDir) {
		c.rescan()
	}
	return c.list, c.etag, nil
}

// Find is the board called name: the shipped one, if a custom file clashes with it.
func (c *Catalog) Find(name string) (Entry, bool) {
	list, _, err := c.Entries()
	if err != nil {
		return Entry{}, false
	}
	for _, e := range list {
		if e.Board.Name == name && e.Source == "catalog" {
			return e, true
		}
	}
	return Find(list, name)
}

// Save writes bd as a custom board (Save) and takes it into the list at once.
func (c *Catalog) Save(bd Board) error {
	if c.customDir == "" {
		return errors.New("no directory for your boards")
	}
	list, _, err := c.Entries()
	if err != nil {
		return err
	}
	if err := Save(c.customDir, bd, list); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rescan()
	return nil
}

// rescan reads the custom directory and builds the ordered list and its ETag; c.mu held.
func (c *Catalog) rescan() {
	seen := map[string]bool{}
	list := append([]Entry(nil), c.shipped...)
	for _, e := range c.shipped {
		seen[e.Board.Name] = true
	}
	c.stamp = stampOf(c.customDir)
	custom, err := loadDir(c.customDir, "custom", seen)
	if err != nil {
		// A custom file that can't be read is one entry saying so, not a dead list.
		custom = append(custom, Entry{Source: "custom", Board: Board{Name: filepath.Base(c.customDir)},
			Errors: []string{err.Error()}, Warnings: []string{}})
	}
	list = append(list, custom...)
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i].Board, list[j].Board
		if ma, mb := makerOf(a), makerOf(b); ma != mb {
			return ma < mb
		}
		if ta, tb := titleOf(a), titleOf(b); ta != tb {
			return ta < tb
		}
		return a.Name < b.Name
	})
	c.list = list
	c.etag = etagOf(list)
}

func titleOf(b Board) string {
	if b.Title != "" {
		return strings.ToLower(b.Title)
	}
	return b.Name
}

// makerOf is the title's first word, as the picker groups boards.
func makerOf(b Board) string {
	return strings.Fields(titleOf(b) + " ")[0]
}

func etagOf(list []Entry) string {
	raw, _ := json.Marshal(list)
	sum := sha256.Sum256(raw)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}

// stampOf tells the custom directory's state apart: its *.json files' names, sizes and
// modification times, and the directory's own; "" for no directory.
func stampOf(dir string) string {
	if dir == "" {
		return ""
	}
	st, err := os.Stat(dir)
	if err != nil {
		return "none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d;", st.ModTime().UnixNano())
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if info, err := e.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", e.Name(), info.Size(), info.ModTime().UnixNano())
		}
	}
	return b.String()
}
