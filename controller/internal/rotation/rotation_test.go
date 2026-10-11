package rotation_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/rotation/rotationtest"
)

const firmware = "../../../firmware"

// The files a node's KEYS release is prepared in are byte for byte what
// firmware/tools/release.py keys-prepare writes (testdata/prepare, made by it; and the tool
// run again here when there is a python3).
func TestNodeFilesMatchReleasePy(t *testing.T) {
	rel, err := os.ReadFile("testdata/prepare/release.pub")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := os.ReadFile("testdata/prepare/recovery.pub")
	node, _ := release.ParseMAC("30:ed:a0:00:00:01")
	for _, c := range []struct {
		dir  string
		keep bool
	}{{"testdata/prepare/keep", true}, {"testdata/prepare/compromised", false}} {
		m, p, info, err := rotation.NodeFiles(node, "esp32s3-octal", 1760000000000, rel, rec, c.keep, "0123456789ABCDEF")
		if err != nil {
			t.Fatal(err)
		}
		for name, got := range map[string][]byte{rotation.ManifestFile: m, rotation.PayloadFile: p, rotation.InfoFile: info} {
			want, err := os.ReadFile(filepath.Join(c.dir, name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/%s differs from release.py keys-prepare's:\n got %q\nwant %q", c.dir, name, got, want)
			}
		}
	}

	py, err := exec.LookPath("python3")
	if err != nil {
		t.Log("no python3: compared with the committed files only")
		return
	}
	// Fresh keys and a seq, through the tool now.
	dir := t.TempDir()
	r2, r3 := release.PublicRaw(rotationtest.Key(t)), release.PublicRaw(rotationtest.Key(t))
	os.WriteFile(filepath.Join(dir, "r.pub"), r2, 0o600)
	os.WriteFile(filepath.Join(dir, "c.pub"), r3, 0o600)
	out := filepath.Join(dir, "out")
	cmd := exec.Command(py, filepath.Join(firmware, "tools", "release.py"), "keys-prepare", "--node", "02:00:00:00:10:07",
		"--image", "esp32p4-rev1", "--release", filepath.Join(dir, "r.pub"), "--recovery", filepath.Join(dir, "c.pub"),
		"--keep-retired", "--current-recovery", "fedcba9876543210", "--seq", "1791234567890", "--dir", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("release.py keys-prepare: %v: %s", err, b)
	}
	node, _ = release.ParseMAC("02:00:00:00:10:07")
	m, p, info, err := rotation.NodeFiles(node, "esp32p4-rev1", 1791234567890, r2, r3, true, "fedcba9876543210")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string][]byte{rotation.ManifestFile: m, rotation.PayloadFile: p, rotation.InfoFile: info} {
		want, _ := os.ReadFile(filepath.Join(out, name))
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from release.py keys-prepare's (run now)", name)
		}
	}
}

// prepare prepares a rotation of the fleet to a new release key (and the recovery key rec,
// nil to keep it).
func prepare(t *testing.T, f *rotationtest.Fleet, next *ecdsa.PrivateKey, rec []byte, compromised bool) (string, *rotation.Plan, error) {
	t.Helper()
	ctx := context.Background()
	ts, err := rotation.Targets(ctx, f.Data, f.Addrs, &fleet.Client{})
	if err != nil {
		t.Fatal(err)
	}
	cur, err := rotation.CurrentRecovery(f.Data, nil, rec, ts)
	if err != nil {
		t.Fatal(err)
	}
	return rotation.Prepare(rotation.PrepareOptions{DataDir: f.Data, Nodes: ts, Current: f.Release, NewRelease: next,
		CurrentRecovery: cur, NewRecovery: rec, Compromised: compromised, LastSeq: rotation.LastSeq(f.Data),
		Repush: rotation.Repush(f.Data)})
}

func push(t *testing.T, f *rotationtest.Fleet, dir string, listed []string) (*rotation.Plan, error) {
	t.Helper()
	cur, err := keys.FileSource{Path: keys.Path(f.Data)}.Key()
	if err != nil {
		t.Fatal(err)
	}
	next, err := keys.ReadNext(keys.NextPath(f.Data))
	if err != nil {
		t.Fatal(err)
	}
	return rotation.Push(context.Background(), rotation.PushOptions{DataDir: f.Data, Dir: dir, Listed: listed, Current: cur,
		Next: next, Client: &fleet.Client{Poll: 10 * time.Millisecond}, Logf: t.Logf, Healthy: 300 * time.Millisecond,
		Poll: 10 * time.Millisecond})
}

func status(t *testing.T, addr string) release.NodeStatus {
	t.Helper()
	st, err := (&fleet.Client{}).Status(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// after are a node's events since the first "push keys".
func after(n *fakenode.Node) []string {
	ev := n.Events()
	i := slices.Index(ev, "push keys")
	if i < 0 {
		return nil
	}
	return ev[i:]
}

func TestRotateWholeFleet(t *testing.T) {
	f := rotationtest.New(t, 3)
	next := rotationtest.Key(t)
	dir, p, err := prepare(t, f, next, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	newFP, oldFP := keys.Fingerprint(next), keys.Fingerprint(f.Release)
	if p.New.Release != newFP || p.Old.Release != oldFP || p.New.Recovery != p.Old.Recovery || !p.KeepRetired() || len(p.Nodes) != 3 {
		t.Fatalf("plan %+v", p)
	}
	for _, n := range p.Nodes {
		if len(n.Repush) != 4 || len(n.Missing) != 0 {
			t.Errorf("%s: will re-push %q, missing %q", n.Addr, n.Repush, n.Missing)
		}
	}
	if k, err := keys.ReadNext(keys.NextPath(f.Data)); err != nil || k == nil || !k.Equal(next) {
		t.Fatalf("release.next.pem: %v", err)
	}
	if fi, _ := os.Stat(keys.NextPath(f.Data)); fi.Mode().Perm() != 0o600 {
		t.Errorf("release.next.pem mode %v", fi.Mode())
	}
	// Each node's files: what release.py would write for it, unsigned.
	node0, _ := release.ParseMAC(p.Nodes[0].NodeID)
	m, _, _, _ := rotation.NodeFiles(node0, rotationtest.Image, p.Nodes[0].Seq, release.PublicRaw(next), release.PublicRaw(f.Recovery), true, p.Old.Recovery)
	if b, _ := os.ReadFile(filepath.Join(dir, p.Nodes[0].Dir, rotation.ManifestFile)); !bytes.Equal(b, m) {
		t.Error("manifest.bin isn't NodeFiles'")
	}

	// Not signed: nothing goes to a node.
	if _, err := push(t, f, dir, f.Addrs); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("push unsigned: %v", err)
	}
	for _, n := range f.Nodes {
		if after(n) != nil {
			t.Fatal("a node took a release before the rotation was signed")
		}
	}
	// A release key, or another key, signs nothing.
	for _, k := range []*ecdsa.PrivateKey{f.Release, next, rotationtest.Key(t)} {
		if _, err := rotation.Sign(dir, k); err == nil {
			t.Error("signed with a key that isn't the recovery key")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, p.Nodes[0].Dir, rotation.SignedFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("release.bin written by a refused signing")
	}
	lines, err := rotation.Sign(dir, f.Recovery)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || !strings.Contains(lines[1], f.Addrs[0]) || !strings.Contains(lines[1], newFP) {
		t.Errorf("sign said %q", lines)
	}

	p, err = push(t, f, dir, f.Addrs)
	if err != nil {
		t.Fatal(err)
	}
	if p.Finished.IsZero() || !p.Done() {
		t.Fatalf("plan %+v", p)
	}
	for i, n := range f.Nodes {
		st := status(t, f.Addrs[i])
		if st.Keys[0] != newFP || st.Keys[1] != p.Old.Recovery || st.KeysRetired != oldFP {
			t.Errorf("%s: keys %v retired %q", f.Addrs[i], st.Keys, st.KeysRetired)
		}
		want := []string{"push keys", "push config", "push zones", "push overrides", "push blocklist"}
		if ev := after(n); !slices.Equal(ev, want) {
			t.Errorf("%s: %q after the KEYS release", f.Addrs[i], ev)
		}
		// The config record follows the config pushed again.
		if r, _ := configs.LoadPushed(f.Data, n.ID()); r == nil || r.Seq != st.Config.Seq {
			t.Errorf("%s: config record %+v, node at seq %d", f.Addrs[i], r, st.Config.Seq)
		}
	}
	// The controller's keys swapped: the new release key, the old one kept.
	cur, err := keys.FileSource{Path: keys.Path(f.Data)}.Key()
	if err != nil || !cur.Equal(next) {
		t.Fatalf("release.pem: %v", err)
	}
	if prev, err := keys.ReadNext(keys.PrevPath(f.Data)); err != nil || prev == nil || !prev.Equal(f.Release) {
		t.Errorf("release.prev.pem: %v", err)
	}
	if _, err := os.Stat(keys.NextPath(f.Data)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("release.next.pem still there")
	}
	if rec, _ := keys.ReadRecovery(f.Data); !bytes.Equal(rec, release.PublicRaw(f.Recovery)) {
		t.Error("recovery.pub changed")
	}
	if _, err := push(t, f, dir, f.Addrs); err == nil || !strings.Contains(err.Error(), "finished") {
		t.Errorf("push again: %v", err)
	}
	if rotation.Pending(f.Data) != nil {
		t.Error("still pending")
	}
}

// A push that stops at a node (unhealthy after its keys changed) goes on from it the next
// time; meanwhile every node gets the key it trusts.
func TestRotateResumesAndSignsEachNodeWithItsKey(t *testing.T) {
	f := rotationtest.New(t, 3)
	next := rotationtest.Key(t)
	newRec := rotationtest.Key(t)
	dir, p, err := prepare(t, f, next, release.PublicRaw(newRec), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotation.Sign(dir, f.Recovery); err != nil {
		t.Fatal(err)
	}
	f.Nodes[1].Break(true)
	_, err = push(t, f, dir, f.Addrs)
	if err == nil || !strings.Contains(err.Error(), f.Addrs[1]) || !strings.Contains(err.Error(), "push again") {
		t.Fatalf("push: %v", err)
	}
	p, _ = rotation.Load(dir)
	if p.Nodes[0].State != rotation.StateDone || p.Nodes[1].State != rotation.StateKeys || p.Nodes[1].Error == "" ||
		p.Nodes[2].State != rotation.StatePrepared || !p.Finished.IsZero() {
		t.Fatalf("plan after the failure: %+v %+v %+v", p.Nodes[0], p.Nodes[1], p.Nodes[2])
	}
	if after(f.Nodes[2]) != nil {
		t.Fatal("the node after the failed one was touched")
	}
	cur, _ := keys.FileSource{Path: keys.Path(f.Data)}.Key()
	if !cur.Equal(f.Release) {
		t.Fatal("release.pem swapped part way")
	}

	// Partial rotation: a push to each node is signed with the key it trusts.
	pu := f.Pusher(cur)
	pu.Alt = keys.Alternates(keys.FileSource{Path: keys.Path(f.Data)}, t.Logf)
	for _, a := range []string{f.Addrs[0], f.Addrs[2]} {
		if _, err := pu.PushRelease(context.Background(), a, release.Control, release.FlushPayload()); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	plain := f.Pusher(cur)
	if _, err := plain.PushRelease(context.Background(), f.Addrs[0], release.Control, release.FlushPayload()); err == nil {
		t.Error("the old key alone signed for a node with its new keys")
	}

	f.Nodes[1].Break(false)
	// One node at a time, by address: the others wait, the keys aren't swapped yet.
	cur2, _ := keys.FileSource{Path: keys.Path(f.Data)}.Key()
	next2, _ := keys.ReadNext(keys.NextPath(f.Data))
	p, err = rotation.Push(context.Background(), rotation.PushOptions{DataDir: f.Data, Dir: dir, Listed: f.Addrs, Current: cur2,
		Next: next2, Client: &fleet.Client{}, Logf: t.Logf, Healthy: 300 * time.Millisecond, Poll: 10 * time.Millisecond,
		Only: []string{f.Addrs[1]}})
	if err != nil || p.Nodes[1].State != rotation.StateDone || p.Nodes[2].State != rotation.StatePrepared || !p.Finished.IsZero() {
		t.Fatalf("push of one node: %v %+v", err, p.Nodes)
	}
	if after(f.Nodes[2]) != nil {
		t.Fatal("a node not asked for was pushed")
	}
	p, err = push(t, f, dir, f.Addrs)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Done() || p.Finished.IsZero() {
		t.Fatalf("plan %+v", p)
	}
	if n := slices.Index(f.Nodes[0].Events(), "push keys"); n < 0 || slices.Contains(f.Nodes[0].Events()[n+1:], "push keys") {
		t.Error("node 1 took its KEYS release twice")
	}
	// The recovery key changed too: recovery.pub follows.
	if rec, _ := keys.ReadRecovery(f.Data); !bytes.Equal(rec, release.PublicRaw(newRec)) {
		t.Error("recovery.pub not updated")
	}
	for i := range f.Nodes {
		if st := status(t, f.Addrs[i]); st.Keys[1] != keys.Fingerprint(newRec) {
			t.Errorf("%s: recovery %s", f.Addrs[i], st.Keys[1])
		}
	}
}

// After a compromise: the old key isn't kept on the nodes, and a node storing what the
// controller can't sign again isn't rotated (its config would not load at the next boot).
func TestRotateCompromised(t *testing.T) {
	f := rotationtest.New(t, 2)
	if _, _, err := prepare(t, f, nil, release.PublicRaw(rotationtest.Key(t)), true); err == nil || !strings.Contains(err.Error(), "new release key") {
		t.Fatalf("compromised without a new release key: %v", err)
	}
	// Node 2's config pushed some other way: no record of it here.
	f.Store(t, 1, false)
	next := rotationtest.Key(t)
	if _, _, err := prepare(t, f, next, nil, true); err == nil || !strings.Contains(err.Error(), "config") || !strings.Contains(err.Error(), f.Addrs[1]) {
		t.Fatalf("prepare with an unrecorded config: %v", err)
	}
	if _, err := os.Stat(keys.NextPath(f.Data)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a refused prepare left release.next.pem")
	}
	f.Store(t, 1, true)
	dir, p, err := prepare(t, f, next, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.KeepRetired() {
		t.Fatal("keeps the retired key after a compromise")
	}
	b, _ := os.ReadFile(filepath.Join(dir, p.Nodes[0].Dir, rotation.PayloadFile))
	if _, _, keep, err := release.ParseKeysPayload(b); err != nil || keep {
		t.Fatalf("payload keeps the old key: %v", err)
	}
	if _, err := rotation.Sign(dir, f.Recovery); err != nil {
		t.Fatal(err)
	}
	if _, err := push(t, f, dir, f.Addrs); err != nil {
		t.Fatal(err)
	}
	for i, n := range f.Nodes {
		if n.Retired() != nil || status(t, f.Addrs[i]).KeysRetired != "" {
			t.Errorf("%s keeps a retired key", f.Addrs[i])
		}
	}
}

// The controller's keys are swapped only once every listed node reports the new ones.
func TestRotateNotFinishedWithAListedNodeLeft(t *testing.T) {
	f := rotationtest.New(t, 3)
	next := rotationtest.Key(t)
	ts, _ := rotation.Targets(context.Background(), f.Data, f.Addrs[:2], &fleet.Client{})
	rec, _ := keys.ReadRecovery(f.Data)
	dir, _, err := rotation.Prepare(rotation.PrepareOptions{DataDir: f.Data, Nodes: ts, Current: f.Release, NewRelease: next,
		CurrentRecovery: rec, LastSeq: rotation.LastSeq(f.Data)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotation.Sign(dir, f.Recovery); err != nil {
		t.Fatal(err)
	}
	_, err = push(t, f, dir, f.Addrs)
	if !errors.Is(err, rotation.ErrNotFinished) || !strings.Contains(err.Error(), f.Addrs[2]) {
		t.Fatalf("push: %v", err)
	}
	if cur, _ := (keys.FileSource{Path: keys.Path(f.Data)}).Key(); !cur.Equal(f.Release) {
		t.Fatal("release.pem swapped with a listed node on the old keys")
	}
	// Once that node is no longer listed, the push finishes.
	if _, err := push(t, f, dir, f.Addrs[:2]); err != nil {
		t.Fatal(err)
	}
}

func TestDiscardAndPrepareGuards(t *testing.T) {
	f := rotationtest.New(t, 1)
	next := rotationtest.Key(t)
	dir, _, err := prepare(t, f, next, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepare(t, f, rotationtest.Key(t), nil, false); err == nil || !strings.Contains(err.Error(), "under way") {
		t.Fatalf("a second rotation: %v", err)
	}
	if err := rotation.Discard(f.Data, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Error("dir kept")
	}
	if _, err := os.Stat(keys.NextPath(f.Data)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("release.next.pem kept")
	}
	if _, _, err := prepare(t, f, nil, nil, false); err == nil || !strings.Contains(err.Error(), "nothing changes") {
		t.Errorf("no change: %v", err)
	}
	// A signed release taken from elsewhere: checked against the plan.
	dir, p, err := prepare(t, f, next, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	for _, n := range []string{rotation.PlanFile} {
		b, _ := os.ReadFile(filepath.Join(dir, n))
		os.WriteFile(filepath.Join(other, n), b, 0o600)
	}
	os.MkdirAll(filepath.Join(other, p.Nodes[0].Dir), 0o700)
	for _, n := range []string{rotation.ManifestFile, rotation.PayloadFile, rotation.InfoFile} {
		b, _ := os.ReadFile(filepath.Join(dir, p.Nodes[0].Dir, n))
		os.WriteFile(filepath.Join(other, p.Nodes[0].Dir, n), b, 0o600)
	}
	if _, err := rotation.Sign(other, f.Recovery); err != nil {
		t.Fatal(err)
	}
	signed, _ := os.ReadFile(filepath.Join(other, p.Nodes[0].Dir, rotation.SignedFile))
	bad := bytes.Clone(signed)
	bad[100] ^= 1
	if _, err := p.AddSigned(dir, bad); err == nil {
		t.Error("took a changed release")
	}
	if n, err := p.AddSigned(dir, signed); err != nil || n.NodeID != p.Nodes[0].NodeID {
		t.Fatalf("AddSigned: %v", err)
	}
	if _, err := p.Signed(dir, p.Nodes[0]); err != nil {
		t.Fatal(err)
	}
}
