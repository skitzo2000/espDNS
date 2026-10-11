package release

import (
	"bytes"
	"testing"
)

// The KEYS payload and manifest are byte for byte what tools/release.py makes (and the node
// checks in test_core), and its recovery-key signature verifies.
func TestKeysMatchesPython(t *testing.T) {
	v := vectors(t)
	if v["V_KEYS"] == nil {
		t.Fatal("no KEYS vectors in release_vectors.h")
	}
	node, _ := ParseMAC("30:ed:a0:00:00:01")
	p, err := KeysPayload(v["V_KEY2"], v["V_KEY3"], true)
	if err != nil || !bytes.Equal(p, v["V_KEYS_PAYLOAD"]) {
		t.Fatalf("payload differs from the Python tool's: %v\n got %x\nwant %x", err, p, v["V_KEYS_PAYLOAD"])
	}
	m, err := KeysManifest(node, "test-board", 4000, p)
	if err != nil || !bytes.Equal(m, v["V_KEYS"][:ManifestLen]) {
		t.Fatalf("manifest differs from the Python tool's: %v", err)
	}
	if Keys != 7 || Keys.String() != "keys" {
		t.Errorf("kind %d %q", Keys, Keys)
	}
	whole := append(bytes.Clone(v["V_KEYS"]), v["V_KEYS_PAYLOAD"]...)
	rel, rec, keep, err := VerifyKeys(v["V_KEY1"], node, whole)
	if err != nil || !bytes.Equal(rel, v["V_KEY2"]) || !bytes.Equal(rec, v["V_KEY3"]) || !keep {
		t.Fatalf("VerifyKeys: %v", err)
	}
	// with the release key, for any node, for another node, another key, a changed payload
	if _, _, _, err := VerifyKeys(v["V_KEY0"], node, append(bytes.Clone(v["V_KEYS_REL0"]), v["V_KEYS_PAYLOAD"]...)); err == nil {
		t.Error("a KEYS release signed with the release key verifies")
	}
	if _, _, _, err := VerifyKeys(v["V_KEY1"], node, append(bytes.Clone(v["V_KEYS_ANY"]), v["V_KEYS_PAYLOAD"]...)); err == nil {
		t.Error("a KEYS release for any node verifies")
	}
	other := node
	other[5] ^= 1
	if _, _, _, err := VerifyKeys(v["V_KEY1"], other, whole); err == nil {
		t.Error("a KEYS release verifies on another node")
	}
	if _, _, _, err := VerifyKeys(v["V_KEY3"], node, whole); err == nil {
		t.Error("verified with another recovery key")
	}
	bad := bytes.Clone(whole)
	bad[HeaderLen+6] = 0
	if _, _, _, err := VerifyKeys(v["V_KEY1"], node, bad); err == nil {
		t.Error("a changed payload verifies")
	}
	if _, err := KeysManifest(AnyNode, "test-board", 4000, p); err == nil {
		t.Error("a KEYS manifest for any node")
	}
}

func TestKeysPayloadRefuses(t *testing.T) {
	v := vectors(t)
	good := v["V_KEYS_PAYLOAD"]
	flip := func(i int) []byte { b := bytes.Clone(good); b[i] ^= 1; return b }
	same := bytes.Clone(good)
	copy(same[73:], good[8:73])
	for name, p := range map[string][]byte{
		"short": good[:len(good)-1], "long": append(bytes.Clone(good), 0), "magic": flip(3), "version": flip(4),
		"flags": flip(7), "release point": flip(40), "recovery point": flip(100), "same keys": same,
	} {
		if _, _, _, err := ParseKeysPayload(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := KeysPayload(v["V_KEY2"], v["V_KEY2"], false); err == nil {
		t.Error("the same key twice")
	}
	if _, err := KeysPayload(v["V_KEY2"][:64], v["V_KEY3"], false); err == nil {
		t.Error("a short key")
	}
	p, _ := KeysPayload(v["V_KEY2"], v["V_KEY3"], false)
	if _, _, keep, err := ParseKeysPayload(p); err != nil || keep || p[6] != 0 {
		t.Errorf("without the flag: %v %v", keep, err)
	}
}
