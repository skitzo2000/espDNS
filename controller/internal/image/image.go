// Package image assembles a board's whole-flash image from a chip image (exported by
// `make export-images` in firmware/, or a release's) and the board's definition, the way tools/factory.py
// does with esptool, so the controller can flash any board without a toolchain.
package image

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/skitzo2000/espdns/controller/internal/keyspart"
)

// Chip is one exported chip image: data/firmware/images/<image>/image.json and its files.
type Chip struct {
	Image      string         `json:"image"`
	Chip       string         `json:"chip"`
	ChipFamily string         `json:"chip_family"` // ESP Web Tools' name, e.g. ESP32-S3
	Offsets    map[string]int `json:"offsets"`
	FlashMode  string         `json:"flash_mode"`
	FlashFreq  string         `json:"flash_freq"`
	Version    string         `json:"version"`
	Built      string         `json:"built"`
	ElfSHA256  string         `json:"elf_sha256"`
	dir        string
}

// List returns the chip images under dir.
func List(dir string) ([]Chip, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*", "image.json"))
	if err != nil {
		return nil, err
	}
	out := []Chip{}
	for _, f := range files {
		c, err := Open(filepath.Dir(f))
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Image < out[j].Image })
	return out, nil
}

// Open reads one chip image directory.
func Open(dir string) (Chip, error) {
	var c Chip
	b, err := os.ReadFile(filepath.Join(dir, "image.json"))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", dir, err)
	}
	c.dir = dir
	return c, nil
}

// Manifest is the ESP Web Tools manifest that installs one whole-flash image (Assemble's)
// at offset 0, erasing the flash first; path is the image's URL, relative to the manifest's.
// The controller's builder serves it for the image it assembled (a release has no factory
// images: internal/dist).
//
// wifi is whether the board may run on Wi-Fi (boards.Board.MayRunOnWiFi). The image erases
// NVS, so such a board has no network until it is given one: ESP Web Tools then waits for
// the firmware's Improv Wi-Fi serial (firmware/main/improv.c) after installing, for its
// default 10 s, and offers its Wi-Fi form; on a board that already runs espDNS it offers
// Connect to Wi-Fi or Change Wi-Fi. A wired board doesn't speak Improv, so the wait is 0
// and ESP Web Tools only installs.
func (c Chip) Manifest(board string, wifi bool, path string) map[string]any {
	m := map[string]any{
		"name":                     "espDNS " + board,
		"version":                  c.Version,
		"new_install_prompt_erase": true,
		"builds": []any{map[string]any{
			"chipFamily": c.ChipFamily,
			"parts":      []any{map[string]any{"path": path, "offset": 0}},
		}},
	}
	if !wifi {
		m["new_install_improv_wait_time"] = 0
	}
	return m
}

// Dir is the directory the chip image was read from.
func (c Chip) Dir() string { return c.dir }

// flashSizeCode is the bootloader header's flash size field (esptool's FLASH_SIZES).
var flashSizeCode = map[int]byte{1: 0, 2: 1, 4: 2, 8: 3, 16: 4, 32: 5, 64: 6, 128: 7}

// Assemble returns the whole-flash image, written at offset 0: bootloader (with the board's
// flash size), the partition table for that size, OTA data, the board partition, the keys
// partition and the app. Gaps are 0xFF, as esptool merge_bin leaves them; NVS is erased.
//
// keysPart is the keys partition image (keyspart.Install: the public keys the node is to
// trust, slot A written, slot B erased), keyspart.Size bytes; nil leaves the range erased,
// and the node comes up in setup mode (or from a seed built into the image). Either way the
// chip image's partition table must have the keys partition at keyspart.Offset: one
// exported before there was one (an image.json without a keys offset) is refused.
func (c Chip) Assemble(flashMB int, boardPart, keysPart []byte) ([]byte, error) {
	code, ok := flashSizeCode[flashMB]
	if !ok {
		return nil, fmt.Errorf("flash size %d MB: not one esptool knows", flashMB)
	}
	if keysPart != nil && len(keysPart) != keyspart.Size {
		return nil, fmt.Errorf("keys partition: %d bytes, not %d", len(keysPart), keyspart.Size)
	}
	table := "partitions-8mb.bin"
	if flashMB < 8 {
		table = "partitions-4mb.bin"
	}
	if off, ok := c.Offsets["keys"]; !ok || off != keyspart.Offset {
		return nil, fmt.Errorf("chip image %s %s has no keys partition (exported before there was one): "+
			"import a current release's chip images (espdns release import), then prepare the image again", c.Image, c.Version)
	}
	type part struct {
		off  int
		data []byte
	}
	var parts []part
	for _, name := range []string{"bootloader.bin", table, "ota_data_initial.bin", "app.bin"} {
		off, ok := c.Offsets[name]
		if !ok {
			return nil, fmt.Errorf("%s: no offset for %s", c.Image, name)
		}
		data, err := os.ReadFile(filepath.Join(c.dir, name))
		if err != nil {
			return nil, err
		}
		switch name {
		case "bootloader.bin":
			if data, err = setFlashSize(data, code); err != nil {
				return nil, fmt.Errorf("%s bootloader: %w", c.Image, err)
			}
		case table:
			if err := hasKeysPartition(data); err != nil {
				return nil, fmt.Errorf("chip image %s %s, %s: %w: import a current release's chip images "+
					"(espdns release import), then prepare the image again", c.Image, c.Version, name, err)
			}
		}
		parts = append(parts, part{off, data})
	}
	parts = append(parts, part{c.Offsets["board"], boardPart})
	if keysPart != nil {
		parts = append(parts, part{keyspart.Offset, keysPart})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].off < parts[j].off })

	end := 0
	for i, p := range parts {
		if i > 0 && p.off < parts[i-1].off+len(parts[i-1].data) {
			return nil, fmt.Errorf("%s: parts overlap at 0x%x", c.Image, p.off)
		}
		end = p.off + len(p.data)
	}
	if end > flashMB<<20 {
		return nil, fmt.Errorf("image is %d bytes, more than %d MB of flash", end, flashMB)
	}
	out := bytes.Repeat([]byte{0xFF}, end)
	for _, p := range parts {
		copy(out[p.off:], p.data)
	}
	return out, nil
}

// Partition is one entry of an ESP-IDF partition table.
type Partition struct {
	Name    string
	Type    byte
	SubType byte
	Offset  int
	Size    int
}

// ParseTable reads a partition table binary (gen_esp32part.py's): 32-byte entries, each
// magic 0xAA 0x50, type, subtype, offset and size (u32 little-endian), a 16-byte name and
// flags; then an MD5 entry (0xEB 0xEB) or the end (0xFF).
func ParseTable(b []byte) ([]Partition, error) {
	var out []Partition
	for i := 0; i+32 <= len(b); i += 32 {
		e := b[i : i+32]
		switch {
		case e[0] == 0xAA && e[1] == 0x50:
			name := e[12:28]
			if n := bytes.IndexByte(name, 0); n >= 0 {
				name = name[:n]
			}
			out = append(out, Partition{Name: string(name), Type: e[2], SubType: e[3],
				Offset: int(binary.LittleEndian.Uint32(e[4:])), Size: int(binary.LittleEndian.Uint32(e[8:]))})
		case e[0] == 0xEB && e[1] == 0xEB, e[0] == 0xFF && e[1] == 0xFF:
			if len(out) == 0 {
				return nil, errors.New("an empty partition table")
			}
			return out, nil
		default:
			return nil, fmt.Errorf("not a partition table (entry %d)", i/32)
		}
	}
	return nil, errors.New("not a partition table (no end)")
}

// hasKeysPartition checks a partition table binary has the keys partition where the
// firmware and keyspart put it: data, subtype 0x42, at keyspart.Offset, keyspart.Size.
func hasKeysPartition(table []byte) error {
	ps, err := ParseTable(table)
	if err != nil {
		return err
	}
	for _, p := range ps {
		if p.Name == "keys" {
			if p.Type != 1 || p.SubType != 0x42 || p.Offset != keyspart.Offset || p.Size != keyspart.Size {
				return fmt.Errorf("its keys partition is type %d/0x%x at 0x%x, 0x%x bytes, not data/0x42 at 0x%x, 0x%x",
					p.Type, p.SubType, p.Offset, p.Size, keyspart.Offset, keyspart.Size)
			}
			return nil
		}
	}
	return errors.New("its partition table has no keys partition (an image exported before there was one)")
}

// setFlashSize sets the flash size in an ESP image header (byte 3, high nibble) and, when the
// image carries a SHA-256 digest of itself, recomputes it, as esptool does when it flashes.
//
// Layout: 24-byte header (magic 0xE9, segment count at 1, hash_appended at 23), segments of
// (load address u32, length u32, data), a checksum byte that ends a 16-byte-aligned block,
// then the 32-byte SHA-256 of everything before it.
func setFlashSize(img []byte, code byte) ([]byte, error) {
	if len(img) < 24 || img[0] != 0xE9 {
		return nil, errors.New("not an ESP image")
	}
	out := append([]byte(nil), img...)
	out[3] = code<<4 | out[3]&0x0F
	if out[23] != 1 {
		return out, nil
	}
	pos := 24
	for i := 0; i < int(out[1]); i++ {
		if pos+8 > len(out) {
			return nil, errors.New("truncated segment header")
		}
		pos += 8 + int(binary.LittleEndian.Uint32(out[pos+4:]))
	}
	pos = (pos/16)*16 + 16 // the checksum byte ends the next 16-byte block
	if pos+32 > len(out) {
		return nil, errors.New("no room for the appended SHA-256")
	}
	digest := sha256.Sum256(out[:pos])
	copy(out[pos:], digest[:])
	return out, nil
}
