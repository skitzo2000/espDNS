package service

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

func pub(t *testing.T) []byte {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return release.PublicRaw(k)
}

// rotating writes a rotation under way in dataDir, for the node at addr.
func rotating(t *testing.T, dataDir, addr string) {
	old, rel, rec := pub(t), pub(t), pub(t)
	p := &rotation.Plan{Version: 1, ID: "20261010-120000", Created: time.Now(),
		Old: rotation.Keys{Recovery: release.Fingerprint(old)}, New: rotation.Keys{Release: release.Fingerprint(rel), Recovery: release.Fingerprint(rec)},
		OldRecoveryPub: hex.EncodeToString(old), NewReleasePub: hex.EncodeToString(rel), NewRecoveryPub: hex.EncodeToString(rec),
		Nodes: []*rotation.Node{{Addr: addr, NodeID: "02:00:00:00:00:11", Dir: "020000000011", State: rotation.StatePrepared}}}
	dir := filepath.Join(rotation.Root(dataDir), p.ID)
	if err := secfile.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(dir); err != nil {
		t.Fatal(err)
	}
	if rotation.Pending(dataDir) == nil {
		t.Fatal("the rotation doesn't load")
	}
}

// Out: a listed node in service, not in a rotation under way. In: a listed node out of
// service. Remove: a listed node, not in a rotation, named by no pending firmware update.
// A rotation isn't prepared while a node is out of service.
func TestChecks(t *testing.T) {
	dir := t.TempDir()
	a, b, c := "192.0.2.2", "192.0.2.3", "192.0.2.4"
	s := settings.Settings{Nodes: []string{a, b, c}, OutOfService: []string{b}}
	for _, x := range []struct {
		name string
		err  error
		want string
	}{
		{"out", CheckOut(dir, s, a), ""},
		{"out, not listed", CheckOut(dir, s, "192.0.2.9"), "not in settings.json"},
		{"out, out already", CheckOut(dir, s, b), "out of service already"},
		{"in", CheckIn(s, b), ""},
		{"in, in service", CheckIn(s, a), "in service already"},
		{"remove", CheckRemove(dir, s, b, nil), ""},
		{"remove, an update pending", CheckRemove(dir, s, a, []changes.Change{{Kind: changes.Firmware, Nodes: []string{a}, Summary: "Update a"}}),
			`"Update a" names 192.0.2.2: discard it first`},
		{"rotate", rotation.RefuseOutOfService(s.OutOfService), "192.0.2.3 out of service: return it to service, or remove it from the group"},
		{"rotate, none out", rotation.RefuseOutOfService(nil), ""},
	} {
		if (x.want == "") != (x.err == nil) || x.err != nil && !strings.Contains(x.err.Error(), x.want) {
			t.Errorf("%s: %v, want %q", x.name, x.err, x.want)
		}
	}
	rotating(t, dir, c)
	if err := CheckOut(dir, s, c); err == nil || !strings.Contains(err.Error(), "key rotation under way") {
		t.Errorf("out, in a rotation: %v", err)
	}
	if err := CheckRemove(dir, s, c, nil); err == nil || !strings.Contains(err.Error(), "key rotation under way") {
		t.Errorf("remove, in a rotation: %v", err)
	}
	if err := CheckOut(dir, s, a); err != nil {
		t.Errorf("out, not in the rotation: %v", err)
	}
}

// Returning needs it to answer and to trust the controller's release key.
func TestReady(t *testing.T) {
	fp := release.Fingerprint(pub(t))
	if err := Ready("192.0.2.2", release.NodeStatus{Keys: []string{fp, "x"}}, nil, fp); err != nil {
		t.Error(err)
	}
	if err := Ready("192.0.2.2", release.NodeStatus{}, errors.New("timeout"), fp); !errors.Is(err, ErrNotAnswering) ||
		!strings.Contains(err.Error(), "stays out of service") {
		t.Errorf("not answering: %v", err)
	}
	if err := Ready("192.0.2.2", release.NodeStatus{Keys: []string{"0123456789abcdef"}}, nil, fp); err == nil ||
		!strings.Contains(err.Error(), "trusts the release key 0123456789abcdef") {
		t.Errorf("another key: %v", err)
	}
	if err := Ready("192.0.2.2", release.NodeStatus{}, nil, fp); err == nil || !strings.Contains(err.Error(), "release key none") {
		t.Errorf("no keys: %v", err)
	}
}
