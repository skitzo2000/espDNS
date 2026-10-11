// Package disttest makes a signed release of stand-in chip images (internal/dist's files:
// the chip tarballs, SHA256SUMS and its signature), for the controller's tests and the
// browser tests' stand-in forge (e2e/fakeforge). The apps are not firmware: an app
// descriptor, the address and keys markers a release's carry, and text.
package disttest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/dist"
)

// App is a stand-in app image of version: what a release's app carries for the controller
// to read (release.ParseAppDesc, BuiltinAddress, KeysOf), and text.
func App(version, text string) []byte {
	b := make([]byte, 32+256)
	b[0] = 0xE9
	binary.LittleEndian.PutUint32(b[32:], 0xABCD5432)
	copy(b[32+16:], version)
	copy(b[32+48:], "espdns")
	copy(b[32+80:], "12:00:00")
	copy(b[32+96:], "Oct  9 2026")
	b = append(b, "espdns:builtin-address=\x00espdns:keys=partition-v1\x00"...)
	return append(b, text...)
}

// table is a partition table binary with the keys partition, as every export has.
func table(what string) []byte {
	entry := func(name string, sub byte, off, size uint32) []byte {
		e := make([]byte, 32)
		e[0], e[1], e[2], e[3] = 0xAA, 0x50, 1, sub
		binary.LittleEndian.PutUint32(e[4:], off)
		binary.LittleEndian.PutUint32(e[8:], size)
		copy(e[12:28], name)
		return e
	}
	t := append(entry("keys", 0x42, 0x1b000, 0x2000), entry(what, 0x40, 0x12000, 0x1000)...)
	return append(t, bytes.Repeat([]byte{0xFF}, 32)...)
}

// chips are the chip images a release has, and the chip each is for.
var chips = map[string]string{"esp32p4-rev1": "esp32p4", "esp32s3-eth": "esp32s3", "esp32s3-octal": "esp32s3"}

// Release writes into out (made if missing) a release of version with a chip tarball for
// each of images (the catalog's when none), SHA256SUMS over them, and k's signature over it.
func Release(out, version string, k *ecdsa.PrivateKey, images ...string) error {
	if len(images) == 0 {
		images = []string{"esp32p4-rev1", "esp32s3-eth", "esp32s3-octal"}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	var files []string
	for _, img := range images {
		chip := chips[img]
		if chip == "" {
			chip = strings.SplitN(img, "-", 2)[0]
		}
		info, err := json.Marshal(map[string]any{"image": img, "chip": chip, "chip_family": strings.ToUpper(strings.Replace(chip, "esp32", "ESP32-", 1)),
			"version": version, "built": "2026-10-09T12:00:00Z", "elf_sha256": "00", "flash_mode": "dio", "flash_freq": "80m",
			"offsets": map[string]int{"bootloader.bin": 0, "partitions-8mb.bin": 0x8000, "partitions-4mb.bin": 0x8000,
				"ota_data_initial.bin": 0xf000, "app.bin": 0x20000, "board": 0x12000, "keys": 0x1b000}})
		if err != nil {
			return err
		}
		boot := make([]byte, 64)
		boot[0], boot[2], boot[3] = 0xE9, 2, 0x2F
		parts := []struct {
			name string
			b    []byte
		}{{"app.bin", App(version, "the app of "+img)}, {"bootloader.bin", boot}, {"image.json", info},
			{"ota_data_initial.bin", bytes.Repeat([]byte{0xFF}, 8192)}, {"partitions-4mb.bin", table("4 " + img)},
			{"partitions-8mb.bin", table("8 " + img)}}
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		when := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		tw.WriteHeader(&tar.Header{Name: img + "/", Mode: 0o755, Typeflag: tar.TypeDir, ModTime: when, Format: tar.FormatUSTAR})
		for _, p := range parts {
			tw.WriteHeader(&tar.Header{Name: img + "/" + p.name, Mode: 0o644, Typeflag: tar.TypeReg, Size: int64(len(p.b)), ModTime: when, Format: tar.FormatUSTAR})
			tw.Write(p.b)
		}
		if err := tw.Close(); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		name := dist.ChipFile(version, img)
		if err := os.WriteFile(filepath.Join(out, name), buf.Bytes(), 0o644); err != nil {
			return err
		}
		files = append(files, name)
	}
	sums, err := dist.Sums(out, files)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, dist.SumsFile), sums, 0o644); err != nil {
		return err
	}
	return dist.Sign(out, k)
}
