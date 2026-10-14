package release

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
)

var sealNode = [6]byte{0x30, 0xed, 0xa0, 0x00, 0x00, 0x01}

func nodeKey(t *testing.T) (*ecdh.PrivateKey, string) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k, base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}

// A sealed password opens with the node's key and ID alone, and not once altered.
func TestSeal(t *testing.T) {
	k, pub := nodeKey(t)
	pk, err := ParseNodeKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(pk, sealNode, []byte("correct horse"))
	if err != nil || len(b) != 65+13+16 {
		t.Fatal(len(b), err)
	}
	if got, err := Open(k, sealNode, b); err != nil || string(got) != "correct horse" {
		t.Fatal(string(got), err)
	}
	other, _ := nodeKey(t)
	if _, err := Open(other, sealNode, b); err == nil {
		t.Error("opened with another node's key")
	}
	if _, err := Open(k, [6]byte{0x30, 0xed, 0xa0, 0, 0, 2}, b); err == nil {
		t.Error("opened for another node ID")
	}
	for _, i := range []int{1, 70, len(b) - 1} {
		c := bytes.Clone(b)
		c[i] ^= 1
		if _, err := Open(k, sealNode, c); err == nil {
			t.Errorf("opened with byte %d altered", i)
		}
	}
	if b2, _ := Seal(pk, sealNode, []byte("correct horse")); bytes.Equal(b, b2) {
		t.Error("two seals alike")
	}
	for _, bad := range []string{"", "AAAA", base64.StdEncoding.EncodeToString(make([]byte, 65))} {
		if _, err := ParseNodeKey(bad); err == nil {
			t.Errorf("node key %q taken", bad)
		}
	}
}

// SealConfig takes the password out, puts it in sealed, and leaves the rest as it was.
func TestSealConfig(t *testing.T) {
	k, pub := nodeKey(t)
	in := []byte(`{"format":1,"name":"a","wifi":{"ssid":"home","password":"secret123","power_save":true}}`)
	out, err := SealConfig(in, pub, sealNode)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("secret123")) || bytes.Contains(out, []byte(`"password"`)) {
		t.Fatal(string(out))
	}
	c, err := nodecfg.Parse(out)
	if err != nil || c.Name != "a" || *c.Wifi.SSID != "home" || !*c.Wifi.PowerSave || c.Wifi.Sealed == nil {
		t.Fatal(string(out), err)
	}
	b, _ := base64.StdEncoding.DecodeString(*c.Wifi.Sealed)
	if pw, err := Open(k, sealNode, b); err != nil || string(pw) != "secret123" {
		t.Fatal(string(pw), err)
	}
	// An open network: no password at all.
	out, err = SealConfig([]byte(`{"wifi":{"ssid":"cafe","password":""}}`), "", sealNode)
	if err != nil || string(out) != `{"format":1,"wifi":{"ssid":"cafe"}}` {
		t.Fatal(string(out), err)
	}
	// Nothing to seal: the payload as it was.
	if out, err := SealConfig([]byte(`{"name":"b"}`), "", sealNode); err != nil || string(out) != `{"name":"b"}` {
		t.Fatal(string(out), err)
	}
	if _, err := SealConfig(in, "", sealNode); err == nil {
		t.Error("sealed without a node key")
	}
}

// A push seals the password to the key pinned at adoption, and refuses without one or when
// the node reports another.
func TestPushSealsWifi(t *testing.T) {
	k := testKey(t)
	nk, pub := nodeKey(t)
	status := NodeStatus{NodeID: "30:ed:a0:00:00:01", Image: "esp32p4-rev1", Keys: []string{Fingerprint(PublicRaw(k)), "x"},
		Seq: map[string]uint64{"config": 1}, NodeKey: pub}
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			json.NewEncoder(w).Encode(status)
		case "/release":
			got, _ = io.ReadAll(r.Body)
			io.WriteString(w, "ok\n")
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	l := pinned(t, host, status.NodeID)
	p := &Pusher{Key: k, Pins: l}
	cfg := []byte(`{"format":1,"wifi":{"ssid":"home","password":"secret123"}}`)
	ctx := context.Background()

	if _, err := p.PushRelease(ctx, host, Config, cfg); err == nil || !strings.Contains(err.Error(), "no node key pinned") {
		t.Fatal(err)
	}
	if _, err := p.PushRelease(ctx, host, Config, []byte(`{"name":"a"}`)); err != nil {
		t.Fatal("a config without a password:", err)
	}
	if err := l.SetNodeKey(status.NodeID, pub); err != nil {
		t.Fatal(err)
	}
	got = nil
	if _, err := p.PushRelease(ctx, host, Config, cfg); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("secret123")) {
		t.Fatal("the password went in plain text")
	}
	c, err := nodecfg.Parse(got[HeaderLen:])
	if err != nil || c.Wifi.Sealed == nil {
		t.Fatal(err)
	}
	b, _ := base64.StdEncoding.DecodeString(*c.Wifi.Sealed)
	if pw, err := Open(nk, sealNode, b); err != nil || string(pw) != "secret123" {
		t.Fatal(string(pw), err)
	}
	// Another key answering (a reinstalled node, or something else on its address).
	_, status.NodeKey = nodeKey(t)
	if _, err := p.PushRelease(ctx, host, Config, cfg); err == nil || !strings.Contains(err.Error(), "not "+
		Fingerprint(nk.PublicKey().Bytes())) {
		t.Fatal(err)
	}
}

// The interop vector, checked by the firmware's host tests (tests/nodekey_vectors.json):
// the firmware opens what this code seals. NODEKEY_VECTORS=write go test -run
// TestSealVector writes it.
func TestSealVector(t *testing.T) {
	const path = "../../../firmware/tests/nodekey_vectors.json"
	h := func(s string) []byte { b := sha256.Sum256([]byte(s)); return b[:] }
	node, err := ecdh.P256().NewPrivateKey(h("espdns test node key"))
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.P256().NewPrivateKey(h("espdns test ephemeral key"))
	if err != nil {
		t.Fatal(err)
	}
	const pw = "correct horse battery staple"
	b, err := sealWith(eph, node.PublicKey(), sealNode, []byte(pw))
	if err != nil {
		t.Fatal(err)
	}
	v := map[string]string{"node_id": "30:ed:a0:00:00:01", "node_priv": hex.EncodeToString(node.Bytes()),
		"node_key": base64.StdEncoding.EncodeToString(node.PublicKey().Bytes()), "password": pw,
		"sealed": base64.StdEncoding.EncodeToString(b)}
	want, _ := json.MarshalIndent(v, "", "  ")
	want = append(want, '\n')
	if os.Getenv("NODEKEY_VECTORS") == "write" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if have, err := os.ReadFile(path); err != nil || !bytes.Equal(have, want) {
		t.Fatalf("%s is not what this code seals (NODEKEY_VECTORS=write to write it): %v", path, err)
	}
}
