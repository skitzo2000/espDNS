package release

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/skitzo2000/espdns/controller/internal/keyspart"
)

// A KEYS release (Kind Keys) rewrites one node's keys (firmware/main/keys.h): signed with the
// node's current recovery key (KeyRecovery), addressed to its node ID (never AnyNode), its
// payload KeysPayload. The node writes the keys slot it isn't using with the next generation
// and uses the new keys at once, no reboot. firmware/tools/release.py makes the same bytes
// (firmware/tests/release_vectors.h checks all three).
//
//	off  size  field
//	  0     4  magic "EDKR"
//	  4     2  version = 1
//	  6     2  flags: bit 0 = keep the release key it replaces as the retired key
//	  8    65  new release public key
//	 73    65  new recovery public key
const (
	KeysPayloadLen = 138
	keysMagic      = "EDKR"
	keysVersion    = 1
	// KeysKeepRetired keeps the release key being replaced as the node's retired key: what it
	// stored with it (config, zones, lists) still loads at boot until it is pushed again with
	// the new key. Never after a compromise: then the old key must sign nothing more.
	KeysKeepRetired = 0x0001
)

// KeysPayload is the payload of a KEYS release: the new release and recovery public keys
// (65 bytes each, uncompressed P-256 points, different), and whether the node keeps the
// release key they replace as its retired key.
func KeysPayload(release, recovery []byte, keepRetired bool) ([]byte, error) {
	if !keyspart.PointOK(release) || !keyspart.PointOK(recovery) {
		return nil, errors.New("the new release and recovery keys must be P-256 public keys")
	}
	if bytes.Equal(release, recovery) {
		return nil, errors.New("the new release and recovery keys are the same")
	}
	p := make([]byte, 8, KeysPayloadLen)
	copy(p, keysMagic)
	binary.LittleEndian.PutUint16(p[4:], keysVersion)
	if keepRetired {
		binary.LittleEndian.PutUint16(p[6:], KeysKeepRetired)
	}
	p = append(p, release...)
	return append(p, recovery...), nil
}

// ParseKeysPayload checks a KEYS release's payload as the node does (keys.c keys_rel_parse).
func ParseKeysPayload(p []byte) (release, recovery []byte, keepRetired bool, err error) {
	if len(p) != KeysPayloadLen {
		return nil, nil, false, errors.New("a keys release's payload is 138 bytes")
	}
	if string(p[:4]) != keysMagic {
		return nil, nil, false, errors.New("not a keys release payload")
	}
	if binary.LittleEndian.Uint16(p[4:]) != keysVersion {
		return nil, nil, false, errors.New("unsupported keys release version")
	}
	flags := binary.LittleEndian.Uint16(p[6:])
	if flags&^KeysKeepRetired != 0 {
		return nil, nil, false, errors.New("unknown keys release flags")
	}
	release, recovery = bytes.Clone(p[8:73]), bytes.Clone(p[73:138])
	switch {
	case !keyspart.PointOK(release):
		return nil, nil, false, errors.New("new release key is not a P-256 point")
	case !keyspart.PointOK(recovery):
		return nil, nil, false, errors.New("new recovery key is not a P-256 point")
	case bytes.Equal(release, recovery):
		return nil, nil, false, errors.New("new release and recovery keys are the same")
	}
	return release, recovery, flags&KeysKeepRetired != 0, nil
}

// KeysManifest is the unsigned manifest of a KEYS release for one node: for the recovery
// key, addressed to target (never AnyNode), for the chip image it runs.
func KeysManifest(target [6]byte, image string, seq uint64, payload []byte) ([]byte, error) {
	if target == AnyNode {
		return nil, errors.New("a keys release is for one node, not any node")
	}
	if _, _, _, err := ParseKeysPayload(payload); err != nil {
		return nil, err
	}
	return Manifest(Keys, KeyRecovery, target, image, seq, payload)
}

// VerifyKeys checks a whole KEYS release (header and payload) as a node with recovery key
// recovery and ID node does before it writes anything (no seq or image check: the node's
// own), and returns its new keys.
func VerifyKeys(recovery []byte, node [6]byte, rel []byte) (newRelease, newRecovery []byte, keepRetired bool, err error) {
	if len(rel) != HeaderLen+KeysPayloadLen || string(rel[:8]) != magic {
		return nil, nil, false, errors.New("not a KEYS release")
	}
	if !Verify(recovery, rel) {
		return nil, nil, false, errors.New("not signed with this recovery key")
	}
	m, payload := rel[:ManifestLen], rel[HeaderLen:]
	if Kind(m[8]) != Keys || m[9] != KeyRecovery {
		return nil, nil, false, errors.New("not a KEYS release for the recovery key")
	}
	if !bytes.Equal(m[10:16], node[:]) {
		return nil, nil, false, errors.New("a KEYS release for another node")
	}
	if binary.LittleEndian.Uint64(m[40:]) != uint64(len(payload)) {
		return nil, nil, false, errors.New("payload length does not match the manifest")
	}
	want, err := Manifest(Keys, KeyRecovery, node, string(bytes.TrimRight(m[16:32], "\x00")),
		binary.LittleEndian.Uint64(m[32:]), payload)
	if err != nil || !bytes.Equal(want[48:80], m[48:80]) {
		return nil, nil, false, errors.New("payload does not match the signed hash")
	}
	return ParseKeysPayload(payload)
}
