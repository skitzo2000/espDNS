package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/keyspart"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

func newPub(t *testing.T) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return release.PublicRaw(k)
}

func recoveryBody(pub []byte, replace bool) string {
	b, _ := json.Marshal(map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub), "replace": replace})
	return string(b)
}

// Make your keys from the browser: the release key made in the controller (never over one,
// never with nodes listed), the recovery key's public half posted (checked, never replaced
// by accident, never once nodes are listed), "I saved recovery.pem" marked only once both
// are there, and the keys step done then.
func TestKeysAPI(t *testing.T) {
	e := newSetupEnv(t)
	// Before a login: the reads are open (no password yet), the writes refused.
	if code, out := e.do("GET /api/key", ""); code != http.StatusOK || out["present"] != false || out["recovery"].(map[string]any)["present"] != false {
		t.Fatalf("read: %d %v", code, out)
	}
	rec := newPub(t)
	for _, c := range []struct{ p, b string }{{"POST /api/keys/release", "{}"}, {"POST /api/keys/recovery", recoveryBody(rec, false)}} {
		if code, _ := e.do(c.p, c.b); code != http.StatusForbidden {
			t.Fatalf("%s without a login: %d", c.p, code)
		}
	}
	e.firstPassword()

	// The keys step can't be marked before there are keys.
	if code, _ := e.do("POST /api/setup/step", `{"step": "keys"}`); code != http.StatusBadRequest {
		t.Fatalf("marked without keys: %d", code)
	}
	// The release key, made here; once.
	code, out := e.do("POST /api/keys/release", "{}")
	if code != http.StatusOK || out["present"] != true || len(out["fingerprint"].(string)) != 16 {
		t.Fatalf("generate: %d %v", code, out)
	}
	relFP := out["fingerprint"].(string)
	k, err := keys.FileSource{Path: keys.Path(e.dir)}.Key()
	if err != nil || keys.Fingerprint(k) != relFP {
		t.Fatalf("release.pem: %v", err)
	}
	if fi, _ := os.Stat(keys.Path(e.dir)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("release.pem mode %v", fi.Mode())
	}
	if code, out := e.do("POST /api/keys/release", "{}"); code != http.StatusConflict || !strings.Contains(out["error"].(string), "already there") {
		t.Fatalf("generate again: %d %v", code, out)
	}
	// Its public half to download: the 65-byte point.
	w := serve(e.h, withSession(fromHere("GET /api/keys/release.pub", ""), e.cookie, e.tok))
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), release.PublicRaw(k)) {
		t.Fatalf("release.pub: %d %x", w.Code, w.Body.Bytes())
	}

	// The recovery key: malformed ones refused, a private key never taken.
	bad := bytes.Clone(rec)
	bad[64] ^= 1
	for name, body := range map[string]string{
		"not base64":      `{"public_key": "not base64!"}`,
		"short":           recoveryBody(rec[:64], false),
		"off the curve":   recoveryBody(bad, false),
		"empty":           `{"public_key": ""}`,
		"the release key": recoveryBody(release.PublicRaw(k), false),
		"a private key":   `{"public_key": "` + base64.StdEncoding.EncodeToString(rec) + `", "private_key": "MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg"}`,
		"a JWK":           `{"public_key": "` + base64.StdEncoding.EncodeToString(rec) + `", "d": "x"}`,
		"not JSON":        `nope`,
	} {
		if code, out := e.do("POST /api/keys/recovery", body); code != http.StatusBadRequest {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if _, err := os.Stat(keys.RecoveryPath(e.dir)); err == nil {
		t.Fatal("a refused recovery key written")
	}
	code, out = e.do("POST /api/keys/recovery", recoveryBody(rec, false))
	recInfo := out["recovery"].(map[string]any)
	if code != http.StatusOK || recInfo["present"] != true || recInfo["fingerprint"] != keyspart.Fingerprint(rec) {
		t.Fatalf("recovery: %d %v", code, out)
	}
	if got, _ := os.ReadFile(keys.RecoveryPath(e.dir)); !bytes.Equal(got, rec) {
		t.Fatal("recovery.pub isn't the 65-byte point")
	}
	if code, _ := e.do("POST /api/keys/recovery", recoveryBody(rec, false)); code != http.StatusOK {
		t.Fatalf("the same again: %d", code)
	}
	// Both keys: the step isn't done until recovery.pem is said to be saved.
	_, out = e.do("GET /api/setup", "")
	if steps(out)["keys"] || out["release_key"] != true || out["recovery_key"] != true || out["keys_saved"] != false {
		t.Fatalf("keys before the mark: %v", out)
	}
	if code, out = e.do("POST /api/setup/step", `{"step": "keys"}`); code != http.StatusOK || !steps(out)["keys"] {
		t.Fatalf("keys marked: %d %v", code, out)
	}

	// Another recovery key: refused unless replace; replaced (no nodes yet), and the mark
	// goes: the recovery.pem saved isn't this one's.
	rec2 := newPub(t)
	if code, _ := e.do("POST /api/keys/recovery", recoveryBody(rec2, false)); code != http.StatusConflict {
		t.Fatalf("another without replace: %d", code)
	}
	if code, _ := e.do("POST /api/keys/recovery", recoveryBody(rec2, true)); code != http.StatusOK {
		t.Fatalf("replace: %d", code)
	}
	if _, out = e.do("GET /api/setup", ""); steps(out)["keys"] {
		t.Fatal("still marked saved after a new recovery key")
	}

	// A node listed: neither key is replaced from here.
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"192.0.2.53"}}); err != nil {
		t.Fatal(err)
	}
	if _, out = e.do("GET /api/key", ""); out["nodes_listed"] != true {
		t.Fatalf("nodes_listed: %v", out)
	}
	if code, _ := e.do("POST /api/keys/recovery", recoveryBody(rec, true)); code != http.StatusConflict {
		t.Fatalf("replace with a node listed: %d", code)
	}
	os.Remove(keys.Path(e.dir))
	if code, out := e.do("POST /api/keys/release", "{}"); code != http.StatusConflict || !strings.Contains(out["error"].(string), "lists 1 node") {
		t.Fatalf("generate with a node listed: %d %v", code, out)
	}
	if _, err := os.Stat(keys.Path(e.dir)); err == nil {
		t.Fatal("a release key made with a node listed")
	}
	// Made anyway on purpose: the node listed is installed again over USB to take it.
	if code, out := e.do("POST /api/keys/release", `{"reinstall":true}`); code != http.StatusOK || out["present"] != true {
		t.Fatalf("generate with reinstall: %d %v", code, out)
	}
	before, _ := os.ReadFile(keys.Path(e.dir))
	if code, _ := e.do("POST /api/keys/release", `{"reinstall":true}`); code != http.StatusConflict {
		t.Fatalf("generate with reinstall over a key: %d", code)
	}
	if after, _ := os.ReadFile(keys.Path(e.dir)); !bytes.Equal(before, after) {
		t.Fatal("the release key replaced")
	}
}

// With a node listed, the recovery key posted must be the one it trusts (slot 1 of its
// /status keys): an existing fleet's recovery.pub, imported.
func TestRecoveryMustMatchListedNodes(t *testing.T) {
	rec, other := newPub(t), newPub(t)
	node := nodes.Node{Addr: "192.0.2.53", Source: nodes.SourceSettings,
		Status: map[string]any{"keys": []any{"0123456789abcdef", keyspart.Fingerprint(rec)}}}
	e := newSetupEnv(t, func(s *server) { s.nodes = func() []nodes.Node { return []nodes.Node{node} } })
	e.firstPassword()
	if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"192.0.2.53"}}); err != nil {
		t.Fatal(err)
	}
	if code, out := e.do("POST /api/keys/recovery", recoveryBody(other, false)); code != http.StatusConflict ||
		!strings.Contains(out["error"].(string), keyspart.Fingerprint(rec)) {
		t.Fatalf("another key than the node's: %d %v", code, out)
	}
	if code, out := e.do("POST /api/keys/recovery", recoveryBody(rec, false)); code != http.StatusOK {
		t.Fatalf("the node's: %d %v", code, out)
	}
}

// A listed node that trusts another release key than the controller's (a new key made, the
// node to be installed again over USB) doesn't vouch for the recovery key. Unless it may
// still trust the controller's: as its retired key, or with a rotation under way.
func TestRecoveryNodeOfAnotherReleaseKey(t *testing.T) {
	for _, c := range []struct {
		name     string
		retired  bool // the node keeps the controller's release key as its retired key
		rotating bool // a rotation is under way
		want     int
	}{{"another key", false, false, http.StatusOK}, {"retired", true, false, http.StatusConflict},
		{"rotating", false, true, http.StatusConflict}} {
		t.Run(c.name, func(t *testing.T) {
			rec := newPub(t)
			st := map[string]any{"keys": []any{"0123456789abcdef", keyspart.Fingerprint(rec)}}
			node := nodes.Node{Addr: "192.0.2.53", Source: nodes.SourceSettings, Status: st}
			e := newSetupEnv(t, func(s *server) { s.nodes = func() []nodes.Node { return []nodes.Node{node} } })
			e.firstPassword()
			if err := settings.Save(settings.Path(e.dir), settings.Settings{Nodes: []string{"192.0.2.53"}}); err != nil {
				t.Fatal(err)
			}
			code, out := e.do("POST /api/keys/release", `{"reinstall":true}`)
			if code != http.StatusOK {
				t.Fatalf("generate with reinstall: %d %v", code, out)
			}
			if c.retired {
				st["keys_retired"] = out["fingerprint"]
			}
			if c.rotating {
				newRel, newRec := newPub(t), newPub(t)
				p := &rotation.Plan{Version: 1, ID: "20261016-120000", Old: rotation.Keys{Release: out["fingerprint"].(string), Recovery: release.Fingerprint(rec)},
					New:            rotation.Keys{Release: release.Fingerprint(newRel), Recovery: release.Fingerprint(newRec)},
					OldRecoveryPub: hex.EncodeToString(rec), NewReleasePub: hex.EncodeToString(newRel), NewRecoveryPub: hex.EncodeToString(newRec),
					Nodes: []*rotation.Node{{Addr: "192.0.2.53", NodeID: "aa:bb:cc:dd:ee:ff", Dir: "aabbccddeeff", State: rotation.StateKeys}}}
				if err := p.Save(filepath.Join(rotation.Root(e.dir), p.ID)); err != nil {
					t.Fatal(err)
				}
				if rotation.Pending(e.dir) == nil {
					t.Fatal("the rotation isn't under way")
				}
			}
			if code, out := e.do("POST /api/keys/recovery", recoveryBody(newPub(t), false)); code != c.want {
				t.Fatalf("a new recovery key: %d %v, want %d", code, out, c.want)
			}
		})
	}
}

// Install over USB without the keys: refused, "set up your keys first", for an image and for
// Write keys only; with them, Write keys only is the keys partition alone, with no ESP Web
// Tools manifest, and
// its bytes are the keys partition of the two keys.
func TestBuildNeedsKeys(t *testing.T) {
	e := newSetupEnv(t, func(s *server) { s.builder = newBuilder("../../../boards", t.TempDir(), t.TempDir()) })
	e.firstPassword()
	for _, c := range []struct{ p, b string }{
		{"POST /api/build", `{"name":"ws-s3-eth","network":{"address":"192.0.2.60/24","gateway":"192.0.2.1"}}`},
		{"POST /api/build/keys", `{}`},
	} {
		code, out := e.do(c.p, c.b)
		if code != http.StatusUnprocessableEntity || !strings.Contains(out["raw"].(string), "set up your keys first") {
			t.Fatalf("%s without keys: %d %v", c.p, code, out)
		}
	}
	e.do("POST /api/keys/release", "{}")
	if code, out := e.do("POST /api/build/keys", `{}`); code != http.StatusUnprocessableEntity || !strings.Contains(out["raw"].(string), "recovery") {
		t.Fatalf("without the recovery key: %d %v", code, out)
	}
	rec := newPub(t)
	e.do("POST /api/keys/recovery", recoveryBody(rec, false))
	code, out := e.do("POST /api/build/keys", `{}`)
	if code != http.StatusOK || out["keys_only"] != true || out["keys"].(map[string]any)["recovery"] != keyspart.Fingerprint(rec) {
		t.Fatalf("keys only: %d %v", code, out)
	}
	id := out["id"].(string)
	// No ESP Web Tools manifest for it (its install would offer to erase): the page writes
	// image.bin itself.
	if w := serve(e.h, withSession(fromHere("GET /build/"+id+"/manifest.json", ""), e.cookie, e.tok)); w.Code != http.StatusNotFound {
		t.Fatalf("keys-only manifest: %d %s", w.Code, w.Body)
	}
	w := serve(e.h, withSession(fromHere("GET /build/"+id+"/image.bin", ""), e.cookie, e.tok))
	k, _ := keys.FileSource{Path: keys.Path(e.dir)}.Key()
	want, _ := keyspart.Install(release.PublicRaw(k), rec)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), want) {
		t.Fatalf("keys.bin: %d, %d bytes", w.Code, w.Body.Len())
	}
	// The image: past the keys now, to the chip image (not imported here).
	if code, out := e.do("POST /api/build", `{"name":"ws-s3-eth","network":{"address":"192.0.2.60/24","gateway":"192.0.2.1"}}`); code != http.StatusUnprocessableEntity ||
		!strings.Contains(out["raw"].(string), "isn't imported") {
		t.Fatalf("with keys: %d %v", code, out)
	}
}
