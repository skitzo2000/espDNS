package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/skitzo2000/espdns/controller/internal/boards"
	"github.com/skitzo2000/espdns/controller/internal/image"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/keyspart"
	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
)

// builder serves the board catalog and builds a board's image for flashing from the browser.
type builder struct {
	catalog   *boards.Catalog // the shipped boards (loaded once) and data/boards, in memory
	imagesDir string          // data/firmware/images

	// keys is the two public keys a new node is given (keys.Public: the release key's and
	// the recovery key's), written into its keys partition; nil, or an error, and no image is
	// made ("set up your keys first").
	keys func() (releasePub, recoveryPub []byte, err error)

	mu     sync.Mutex
	builds map[string]*build
}

// build is one image prepared for flashing: a board (from the catalog, possibly changed for
// this node, or a custom one) on its chip image, with the keys; or the keys alone (KeysOnly:
// Write keys only, which keeps everything else on the board).
type build struct {
	ID       string       `json:"id"`
	Board    boards.Board `json:"board"`
	Chip     image.Chip   `json:"chip"`
	Keys     buildKeys    `json:"keys"`
	KeysOnly bool         `json:"keys_only,omitempty"`
	bin      []byte
}

// buildKeys are the fingerprints of the keys a build writes (as /status names them).
type buildKeys struct {
	Release  string `json:"release"`
	Recovery string `json:"recovery"`
}

func newBuilder(catalogDir, customDir, imagesDir string) *builder {
	return &builder{catalog: boards.NewCatalog(catalogDir, customDir), imagesDir: imagesDir, builds: map[string]*build{}}
}

func (b *builder) routes(mux jobs.Mux) {
	mux.HandleFunc("GET /api/boards", b.listBoards)
	mux.HandleFunc("POST /api/boards/check", b.checkBoard)
	mux.HandleFunc("POST /api/boards", b.saveBoard)
	mux.HandleFunc("GET /api/images", b.listImages)
	mux.HandleFunc("POST /api/build", b.prepare)
	mux.HandleFunc("POST /api/build/keys", b.prepareKeys)
	mux.HandleFunc("GET /build/{id}/manifest.json", b.manifest)
	mux.HandleFunc("GET /build/{id}/image.bin", b.image)
}

// keysPart is the keys partition a USB install writes (keyspart.Install), and its keys'
// fingerprints; refused with "set up your keys first" while either key is missing.
func (b *builder) keysPart() ([]byte, buildKeys, error) {
	if b.keys == nil {
		return nil, buildKeys{}, keys.ErrNoKeys
	}
	rel, rec, err := b.keys()
	if err != nil {
		return nil, buildKeys{}, err
	}
	part, err := keyspart.Install(rel, rec)
	if err != nil {
		return nil, buildKeys{}, fmt.Errorf("%w: %v", keys.ErrNoKeys, err)
	}
	return part, buildKeys{keyspart.Fingerprint(rel), keyspart.Fingerprint(rec)}, nil
}

// prepareKeys makes the keys partition alone for Write keys only (POST /api/build/keys, no
// body), served at /build/<id>/image.bin: usb.js writes it at 0x1b000 with the vendored
// esptool-js (one write, no erase, read back), keeping the rest of the flash, so a node in
// setup mode (no keys, or corrupt ones) gets its keys and keeps its settings. It has no ESP
// Web Tools manifest. It answers with the build's id, as POST /api/build does.
func (b *builder) prepareKeys(w http.ResponseWriter, r *http.Request) {
	part, fps, err := b.keysPart()
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	sum := sha256.Sum256(part)
	writeJSON(w, b.keep(&build{ID: "keys-" + hex.EncodeToString(sum[:8]), Keys: fps, KeysOnly: true, bin: part}))
}

// keep keeps a build to serve, and gives it back.
func (b *builder) keep(bl *build) *build {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.builds) > 32 { // a handful of recent builds is plenty; they are cheap to make again
		b.builds = map[string]*build{}
	}
	b.builds[bl.ID] = bl
	return bl
}

type boardView struct {
	boards.Entry
	ImageReady bool `json:"image_ready"` // its chip image has been imported
}

// listBoards is the library from memory, ordered by maker then title, with an ETag (the
// list's, plus which chip images are imported), so a browser that has it gets 304.
func (b *builder) listBoards(w http.ResponseWriter, r *http.Request) {
	entries, etag, err := b.catalog.Entries()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	imgs, _ := image.List(b.imagesDir)
	ready := map[string]bool{}
	names := make([]string, 0, len(imgs))
	for _, c := range imgs {
		ready[c.Image] = true
		names = append(names, c.Image)
	}
	sort.Strings(names)
	etag = strings.TrimSuffix(etag, `"`) + "-" + strings.Join(names, ",") + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	out := make([]boardView, 0, len(entries))
	for _, e := range entries {
		out = append(out, boardView{e, ready[e.Board.Image]})
	}
	writeJSON(w, out)
}

func (b *builder) listImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := image.List(b.imagesDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"images": imgs, "known": boards.Images})
}

func readBoard(w http.ResponseWriter, r *http.Request) (boards.Board, error) {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&raw); err != nil {
		return boards.Board{}, err
	}
	return boards.Parse(raw)
}

func (b *builder) checkBoard(w http.ResponseWriter, r *http.Request) {
	bd, err := readBoard(w, r)
	if err != nil {
		writeJSON(w, map[string]any{"errors": []string{err.Error()}, "warnings": []string{}})
		return
	}
	errs, warns := boards.Check(bd)
	writeJSON(w, map[string]any{"errors": nonNil(errs), "warnings": nonNil(warns)})
}

func (b *builder) saveBoard(w http.ResponseWriter, r *http.Request) {
	bd, err := readBoard(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := b.catalog.Save(bd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, bd)
}

// prepare builds the image for a board: {"name": "<catalog board>"} as it is, or
// {"board": {...}} for a changed or custom one, with the node's address in "network"
// ({"address": "192.0.2.52/24", "gateway": "192.0.2.1"}, or {"address": "dhcp"} on
// a network with a DHCP server), which goes into its board partition. It answers with the
// build's id; the browser then flashes /build/<id>/manifest.json with ESP Web Tools.
func (b *builder) prepare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string           `json:"name"`
		Board   json.RawMessage  `json:"board"`
		Network *nodecfg.Network `json:"network"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var e boards.Entry
	switch {
	case len(req.Board) > 0:
		bd, err := boards.Parse(req.Board)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		e = boards.Entry{Source: "custom"}.WithChanges(bd)
	case req.Name != "":
		var ok bool
		if e, ok = b.catalog.Find(req.Name); !ok {
			http.Error(w, "no board "+req.Name, http.StatusNotFound)
			return
		}
	default:
		http.Error(w, "name or board required", http.StatusBadRequest)
		return
	}
	if req.Network != nil {
		bd := e.Board
		bd.Network = req.Network
		e = e.WithChanges(bd)
	}
	bl, err := b.make(e)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, bl)
}

func (b *builder) make(e boards.Entry) (*build, error) {
	if len(e.Errors) > 0 {
		return nil, errors.New(strings.Join(e.Errors, "; "))
	}
	if e.Board.Network == nil {
		return nil, errors.New("network: the node's address, with its prefix length and gateway (or dhcp on a network " +
			"with a DHCP server): a node never asks DHCP for one on its own")
	}
	// The node trusts the keys written here from its first boot: none, and it would come up
	// in setup mode, taking nothing over the network.
	keysPart, fps, err := b.keysPart()
	if err != nil {
		return nil, err
	}
	chip, err := image.Open(b.imagesDir + "/" + e.Board.Image)
	if err != nil {
		return nil, fmt.Errorf("chip image %s isn't imported: build it and load it with espdns release import (docs/developer.md)", e.Board.Image)
	}
	part, err := e.Pack()
	if err != nil {
		return nil, err
	}
	flash := e.Board.FlashMB
	if flash == 0 {
		flash = 4
	}
	bin, err := chip.Assemble(flash, part, keysPart)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(bin)
	return b.keep(&build{ID: hex.EncodeToString(sum[:8]), Board: e.Board, Chip: chip, Keys: fps, bin: bin}), nil
}

func (b *builder) get(w http.ResponseWriter, r *http.Request) *build {
	b.mu.Lock()
	bl := b.builds[r.PathValue("id")]
	b.mu.Unlock()
	if bl == nil {
		http.Error(w, "no such build: prepare it again", http.StatusNotFound)
	}
	return bl
}

func (b *builder) manifest(w http.ResponseWriter, r *http.Request) {
	bl := b.get(w, r)
	if bl == nil {
		return
	}
	if bl.KeysOnly {
		// Never through ESP Web Tools: its install would offer to erase the whole flash.
		http.Error(w, "Write keys only has no ESP Web Tools manifest: the page writes /build/<id>/image.bin itself", http.StatusNotFound)
		return
	}
	writeJSON(w, bl.Chip.Manifest(bl.Board.Name, bl.Board.MayRunOnWiFi(), "image.bin"))
}

func (b *builder) image(w http.ResponseWriter, r *http.Request) {
	bl := b.get(w, r)
	if bl == nil {
		return
	}
	name := bl.Board.Name
	if bl.KeysOnly {
		name = "keys"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=espdns-%s.bin", name))
	w.Write(bl.bin)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
