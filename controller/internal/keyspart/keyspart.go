// Package keyspart packs and reads the keys partition: the public keys a node trusts, in
// flash apart from the firmware (firmware/main/keys.h, docs/architecture.md, Signed releases and pins).
// It is firmware/tools/keyspart.py in Go, byte for byte and answer for answer: both are
// checked against firmware/tests/keys_vectors.json, as main/keys.c is.
//
// The partition is two 4 KB slots at Offset; each holds a 256-byte record, the rest of its
// sector 0xFF:
//
//	off size field
//	  0    4 magic "EDKY"
//	  4    2 version = 1
//	  6    2 flags: bit 0, a retired release key is present
//	  8    4 generation (the higher wins; 0xFFFFFFFF invalid)
//	 12    1 source: 1 USB install, 2 seeded from an embedded copy, 3 KEYS release
//	 13    3 0
//	 16    8 seq of the KEYS release that wrote it (0 otherwise)
//	 24   65 release public key (0x04 || X || Y)
//	 89   65 recovery public key
//	154   65 retired release public key (0xFF if none)
//	219   33 0
//	252    4 CRC-32 (IEEE, as zlib.crc32) of bytes 0..251
//
// All little-endian. A USB install writes slot A (generation 1, source USB) and leaves slot
// B erased (Partition).
package keyspart

import (
	"bytes"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	Offset   = 0x1b000 // where the partition is, in every partition table (firmware/partitions-*.csv)
	Size     = 0x2000  // two slots
	SlotSize = 0x1000
	RecLen   = 256
	Version  = 1
	PubLen   = 65

	GenInvalid  = 0xFFFFFFFF
	FlagRetired = 0x0001

	SrcUSB     = 1
	SrcSeed    = 2
	SrcRelease = 3
)

var magic = []byte("EDKY")

// Record is one slot's fields.
type Record struct {
	Flags      uint16
	Generation uint32
	Source     uint8
	Seq        uint64
	Release    []byte // 65 bytes
	Recovery   []byte // 65 bytes
	Retired    []byte // 65 bytes, or nil (no retired key)
}

// PointOK says whether pub is an uncompressed point on P-256, as mbedtls_ecp_check_pubkey
// and keyspart.point_ok take one: 65 bytes, 0x04, X and Y below p, on the curve.
func PointOK(pub []byte) bool {
	if len(pub) != PubLen || pub[0] != 4 {
		return false
	}
	_, err := ecdh.P256().NewPublicKey(pub)
	return err == nil
}

// Fingerprint is how /status names a key: the first 8 bytes of the SHA-256 of its 65-byte
// point, in hex (release.Fingerprint, the firmware's).
func Fingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// Pack is the 256-byte record for a slot: keyspart.pack.
func Pack(r Record) ([]byte, error) {
	if !PointOK(r.Release) || !PointOK(r.Recovery) {
		return nil, errors.New("release and recovery must be P-256 public keys")
	}
	if bytes.Equal(r.Release, r.Recovery) {
		return nil, errors.New("release and recovery keys are the same")
	}
	if r.Retired != nil && !PointOK(r.Retired) {
		return nil, errors.New("retired must be a P-256 public key")
	}
	if r.Generation == GenInvalid {
		return nil, fmt.Errorf("generation %d: 0 to %d", r.Generation, GenInvalid-1)
	}
	var flags uint16
	if r.Retired != nil {
		flags = FlagRetired
	}
	rec := make([]byte, RecLen)
	copy(rec, magic)
	binary.LittleEndian.PutUint16(rec[4:], Version)
	binary.LittleEndian.PutUint16(rec[6:], flags)
	binary.LittleEndian.PutUint32(rec[8:], r.Generation)
	rec[12] = r.Source
	binary.LittleEndian.PutUint64(rec[16:], r.Seq)
	copy(rec[24:], r.Release)
	copy(rec[89:], r.Recovery)
	if r.Retired != nil {
		copy(rec[154:], r.Retired)
	} else {
		copy(rec[154:219], bytes.Repeat([]byte{0xFF}, PubLen))
	}
	binary.LittleEndian.PutUint32(rec[252:], crc32.ChecksumIEEE(rec[:252]))
	return rec, nil
}

// Slot is a record as its whole 4 KB sector.
func Slot(rec []byte) []byte {
	s := bytes.Repeat([]byte{0xFF}, SlotSize)
	copy(s, rec)
	return s
}

// Partition is the 8 KB partition image: each slot's record, or erased (nil).
func Partition(a, b []byte) []byte {
	out := bytes.Repeat([]byte{0xFF}, Size)
	copy(out, a)
	copy(out[SlotSize:], b)
	return out
}

// Install is what a USB install writes: slot A with release and recovery (generation 1,
// source USB), slot B erased.
func Install(release, recovery []byte) ([]byte, error) {
	rec, err := Pack(Record{Generation: 1, Source: SrcUSB, Release: release, Recovery: recovery})
	if err != nil {
		return nil, err
	}
	return Partition(rec, nil), nil
}

// Parse reads a record (a slot's first 256 bytes, or more): the fields, or why it isn't a
// valid one, in keyspart.parse's words (and in its order).
func Parse(rec []byte) (Record, error) {
	if len(rec) < RecLen {
		return Record{}, errors.New("short slot")
	}
	if !bytes.Equal(rec[:4], magic) {
		return Record{}, errors.New("not a keys record")
	}
	version := binary.LittleEndian.Uint16(rec[4:])
	flags := binary.LittleEndian.Uint16(rec[6:])
	r := Record{Flags: flags, Generation: binary.LittleEndian.Uint32(rec[8:]), Source: rec[12],
		Seq: binary.LittleEndian.Uint64(rec[16:])}
	if version != Version {
		return Record{}, errors.New("unsupported version")
	}
	if crc32.ChecksumIEEE(rec[:252]) != binary.LittleEndian.Uint32(rec[252:]) {
		return Record{}, errors.New("bad CRC")
	}
	release, recovery, retired := rec[24:89], rec[89:154], rec[154:219]
	if flags&^FlagRetired != 0 {
		return Record{}, errors.New("unknown flags")
	}
	if !allOf(rec[13:16], 0) || !allOf(rec[219:252], 0) {
		return Record{}, errors.New("reserved bytes set")
	}
	if r.Generation == GenInvalid {
		return Record{}, errors.New("invalid generation")
	}
	if r.Source < SrcUSB || r.Source > SrcRelease {
		return Record{}, errors.New("unknown source")
	}
	if !PointOK(release) {
		return Record{}, errors.New("release key is not a P-256 point")
	}
	if !PointOK(recovery) {
		return Record{}, errors.New("recovery key is not a P-256 point")
	}
	if bytes.Equal(release, recovery) {
		return Record{}, errors.New("release and recovery keys are the same")
	}
	if flags&FlagRetired != 0 {
		if !PointOK(retired) {
			return Record{}, errors.New("retired key is not a P-256 point")
		}
		r.Retired = bytes.Clone(retired)
	} else if !allOf(retired, 0xFF) {
		return Record{}, errors.New("retired key set without its flag")
	}
	r.Release, r.Recovery = bytes.Clone(release), bytes.Clone(recovery)
	return r, nil
}

// States Pick answers.
const (
	StateOK      = "ok"
	StateBlank   = "blank"   // both slots erased
	StateForeign = "foreign" // no valid slot, neither with the magic: bytes something else left
	StateCorrupt = "corrupt" // no valid slot, one with the magic: a damaged record
)

// Pick is keyspart.pick (keys.c's keys_pick): of the two slots' contents, the valid one with
// the higher generation (A on a tie), its index and fields; else the state and slot -1.
func Pick(a, b []byte) (state string, slot int, r Record) {
	blankA, blankB := allOf(a, 0xFF), allOf(b, 0xFF)
	if blankA && blankB {
		return StateBlank, -1, Record{}
	}
	var ra, rb Record
	okA, okB := false, false
	if !blankA {
		var err error
		ra, err = Parse(a)
		okA = err == nil
	}
	if !blankB {
		var err error
		rb, err = Parse(b)
		okB = err == nil
	}
	if okA && (!okB || ra.Generation >= rb.Generation) {
		return StateOK, 0, ra
	}
	if okB {
		return StateOK, 1, rb
	}
	if bytes.HasPrefix(a, magic) || bytes.HasPrefix(b, magic) {
		return StateCorrupt, -1, Record{}
	}
	return StateForeign, -1, Record{}
}

// Read is the partition image's keys: Pick of its two slots.
func Read(part []byte) (state string, slot int, r Record) {
	if len(part) < Size {
		return StateCorrupt, -1, Record{}
	}
	return Pick(part[:SlotSize], part[SlotSize:Size])
}

func allOf(b []byte, v byte) bool {
	for _, x := range b {
		if x != v {
			return false
		}
	}
	return true
}
