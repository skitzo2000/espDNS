// Package rotation rotates the fleet's keys (docs/security.md, Rotating keys): a KEYS
// release (release.Keys, firmware/main/keys.h) for each listed node, signed offline with
// the recovery key every node trusts, then pushed one node at a time by the controller.
//
// Three steps, each its own command (espdns key rotate prepare|sign|push) and, but the
// signing, a step of the account panel's Rotate keys:
//
//	prepare  for every node in settings.json (its ID as internal/pins pins it, its chip
//	         image from its /status), the unsigned KEYS release, into
//	         <data>/keys/rotation/<id>/ with plan.json; with a new release key, that key
//	         in <data>/keys/release.next.pem (mode 0600)
//	sign     offline (docker run --network none): the recovery private key, from a file or
//	         standard input, checked to be the one every node trusts, signs each node's
//	         release (release.bin). It is never written anywhere.
//	push     one node at a time under the fleet lock: its KEYS release posted, its /status
//	         read again (the new keys in use), then everything it stores (its config, hosted
//	         zones, overrides and blocklist) signed again with the new release key and pushed,
//	         its health checked, then the next node. The first failure stops it; the nodes done
//	         stay done (plan.json records them) and push again goes on from there. When every
//	         listed node reports the new keys, release.next.pem becomes release.pem (the old
//	         one kept as release.prev.pem) and recovery.pub is the new recovery key.
//
// Each node's directory holds what firmware/tools/release.py keys-prepare writes for it,
// byte for byte (manifest.bin, payload.bin, keys.json), and keys-sign's release.bin: a node
// can be signed with either tool.
package rotation

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/keyspart"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// PlanFile is the plan in a rotation's directory; each node's files are in a directory
// named for its node ID (hex, no colons).
const (
	PlanFile     = "plan.json"
	ManifestFile = "manifest.bin"
	PayloadFile  = "payload.bin"
	InfoFile     = "keys.json"
	SignedFile   = "release.bin"
)

// Root is where the rotations are, in dataDir: <data>/keys/rotation.
func Root(dataDir string) string { return filepath.Join(dataDir, keys.Dir, "rotation") }

// Node states in a plan.
const (
	StatePrepared = "prepared" // its KEYS release not sent yet
	StateKeys     = "keys"     // it took its KEYS release: its stored releases not all signed again yet
	StateDone     = "done"     // the new keys in use, what it stores signed again, healthy
)

// Keys are a release and a recovery key, by fingerprint (as /status "keys" names them).
type Keys struct {
	Release  string `json:"release"`
	Recovery string `json:"recovery"`
}

// Node is one node of a rotation, and how far the push is with it.
type Node struct {
	Addr   string `json:"addr"`
	NodeID string `json:"node_id"`
	Image  string `json:"image"`
	Seq    uint64 `json:"seq"`
	Dir    string `json:"dir"` // its files, in the rotation's directory
	State  string `json:"state"`
	// Repush is what it stores that is signed again with the new release key (prepare:
	// what will be; push: what was), Missing what can't be (the controller doesn't have the
	// bytes it runs), each in words.
	Repush  []string  `json:"repush,omitempty"`
	Missing []string  `json:"missing,omitempty"`
	KeysAt  time.Time `json:"keys_at,omitzero"`
	DoneAt  time.Time `json:"done_at,omitzero"`
	Error   string    `json:"error,omitempty"` // why the last push stopped at it
}

// Plan is a rotation (plan.json).
type Plan struct {
	Version int       `json:"version"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	// Compromised: the release key it replaces is not kept as the nodes' retired key (a
	// planned rotation keeps it, so what they stored still loads until pushed again).
	Compromised bool `json:"compromised"`
	Old         Keys `json:"old"`
	New         Keys `json:"new"`
	// The public keys, hex: the recovery key the KEYS releases are signed with (each node
	// trusts it now), the new release and recovery keys.
	OldRecoveryPub string `json:"old_recovery_pub"`
	NewReleasePub  string `json:"new_release_pub"`
	NewRecoveryPub string `json:"new_recovery_pub"`
	// NewReleaseKey: the new release private key is <data>/keys/release.next.pem; false when
	// the release key stays (a rotation of the recovery key alone).
	NewReleaseKey bool      `json:"new_release_key"`
	Nodes         []*Node   `json:"nodes"`
	Finished      time.Time `json:"finished,omitzero"` // the controller's keys swapped: done
}

// KeepRetired is the KEYS releases' flag: the release key replaced is kept as the nodes'
// retired key (a planned rotation of the release key).
func (p *Plan) KeepRetired() bool { return !p.Compromised && p.Old.Release != p.New.Release }

// Started says whether a KEYS release went to any node.
func (p *Plan) Started() bool {
	return slices.ContainsFunc(p.Nodes, func(n *Node) bool { return n.State != StatePrepared })
}

// Done says whether every node is done.
func (p *Plan) Done() bool {
	return !slices.ContainsFunc(p.Nodes, func(n *Node) bool { return n.State != StateDone })
}

func unhex(name, s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || !keyspart.PointOK(b) {
		return nil, fmt.Errorf("plan.json: %s is not a P-256 public key", name)
	}
	return b, nil
}

// check is plan.json's own consistency.
func (p *Plan) check() error {
	if p.Version != 1 {
		return fmt.Errorf("plan.json: version %d, not 1", p.Version)
	}
	for _, f := range []struct{ name, hex, fp string }{{"old_recovery_pub", p.OldRecoveryPub, p.Old.Recovery},
		{"new_release_pub", p.NewReleasePub, p.New.Release}, {"new_recovery_pub", p.NewRecoveryPub, p.New.Recovery}} {
		b, err := unhex(f.name, f.hex)
		if err != nil {
			return err
		}
		if release.Fingerprint(b) != f.fp {
			return fmt.Errorf("plan.json: %s is not the key %s", f.name, f.fp)
		}
	}
	if len(p.Nodes) == 0 {
		return errors.New("plan.json: no nodes")
	}
	for _, n := range p.Nodes {
		if _, err := release.ParseMAC(n.NodeID); err != nil || n.Dir != dirOf(n.NodeID) {
			return fmt.Errorf("plan.json: node %q", n.NodeID)
		}
	}
	return nil
}

func dirOf(id string) string { return strings.ReplaceAll(strings.ToLower(id), ":", "") }

// Load reads a rotation's plan.
func Load(dir string) (*Plan, error) {
	b, err := os.ReadFile(filepath.Join(dir, PlanFile))
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, PlanFile), err)
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Save writes the plan, whole or not at all (mode 0600).
func (p *Plan) Save(dir string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return secfile.Write(filepath.Join(dir, PlanFile), append(b, '\n'))
}

var idRE = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9]+)?$`)

// Find is the rotation dir names: a directory (with its plan.json), or a rotation's ID
// under Root(dataDir).
func Find(dataDir, dir string) (string, error) {
	if idRE.MatchString(dir) {
		if _, err := os.Stat(filepath.Join(dir, PlanFile)); err != nil {
			dir = filepath.Join(Root(dataDir), dir)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, PlanFile)); err != nil {
		return "", fmt.Errorf("%s: no rotation there (no %s): espdns key rotate prepare makes one", dir, PlanFile)
	}
	return dir, nil
}

// List is every rotation in dataDir, newest first (one whose plan doesn't load is left out).
func List(dataDir string) []*Plan {
	ents, _ := os.ReadDir(Root(dataDir))
	var out []*Plan
	for _, e := range ents {
		if !e.IsDir() || !idRE.MatchString(e.Name()) {
			continue
		}
		if p, err := Load(filepath.Join(Root(dataDir), e.Name())); err == nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *Plan) int { return strings.Compare(b.ID, a.ID) })
	return out
}

// Info is a node's keys.json, as firmware/tools/release.py keys-prepare writes it (its
// fields in that order).
type Info struct {
	Node        string `json:"node"`
	Image       string `json:"image"`
	Seq         uint64 `json:"seq"`
	KeepRetired bool   `json:"keep_retired"`
	Release     string `json:"release"`
	Recovery    string `json:"recovery"`
	Signer      string `json:"signer"`
}

// NodeFiles are the unsigned KEYS release for one node, as release.py keys-prepare --node
// node --image image --release <newRelease> --recovery <newRecovery> [--keep-retired]
// --current-recovery <signer> --seq seq writes them: manifest.bin, payload.bin, keys.json.
func NodeFiles(node [6]byte, image string, seq uint64, newRelease, newRecovery []byte, keepRetired bool,
	signer string) (manifest, payload, info []byte, err error) {
	if payload, err = release.KeysPayload(newRelease, newRecovery, keepRetired); err != nil {
		return nil, nil, nil, err
	}
	if manifest, err = release.KeysManifest(node, image, seq, payload); err != nil {
		return nil, nil, nil, err
	}
	mac := make([]string, 6)
	for i, b := range node {
		mac[i] = fmt.Sprintf("%02x", b)
	}
	in := Info{Node: strings.Join(mac, ":"), Image: image, Seq: seq, KeepRetired: keepRetired,
		Release: release.Fingerprint(newRelease), Recovery: release.Fingerprint(newRecovery), Signer: strings.ToLower(signer)}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(in); err != nil {
		return nil, nil, nil, err
	}
	return manifest, payload, buf.Bytes(), nil
}

// Target is a node to prepare for: its address in settings.json, its node ID as pinned
// there, and its /status read now.
type Target struct {
	Addr   string
	NodeID string
	Status release.NodeStatus
}

// PrepareOptions are what Prepare takes.
type PrepareOptions struct {
	DataDir string
	Nodes   []Target
	// Current is the release key the controller signs with now (release.pem): every node
	// must trust it.
	Current *ecdsa.PrivateKey
	// NewRelease is the new release key (written to release.next.pem); nil: it stays.
	NewRelease *ecdsa.PrivateKey
	// CurrentRecovery is the recovery public key every node trusts now (the KEYS releases are
	// signed with its private half).
	CurrentRecovery []byte
	// NewRecovery is the new recovery public key; nil: it stays.
	NewRecovery []byte
	Compromised bool
	// LastSeq is the last KEYS seq signed for a node (internal/pins), 0 for none.
	LastSeq func(nodeID string) uint64
	// Repush says, for a node's /status, what it stores that push signs again, and what it
	// can't (Collect); nil: not said.
	Repush func(st release.NodeStatus) (will, missing []string)
	Now    time.Time
}

// Prepare writes a new rotation in Root(o.DataDir) and returns its directory and plan: each
// node's unsigned KEYS release and plan.json, and the new release key in release.next.pem
// (unless one there is that key already). Nothing is sent to a node.
func Prepare(o PrepareOptions) (string, *Plan, error) {
	if len(o.Nodes) == 0 {
		return "", nil, errors.New("no node listed in settings.json: nothing to rotate")
	}
	if o.Current == nil {
		return "", nil, errors.New("no release key: the nodes' keys are rotated from the controller that signs for them")
	}
	if p := Pending(o.DataDir); p != nil {
		return "", nil, fmt.Errorf("rotation %s is under way (%s): push it to the end, or discard it (espdns key rotate discard)",
			p.ID, filepath.Join(Root(o.DataDir), p.ID))
	}
	if !keyspart.PointOK(o.CurrentRecovery) {
		return "", nil, errors.New("the recovery public key the nodes trust is needed (data/keys/recovery.pub, or -recovery-pub)")
	}
	oldRel := release.PublicRaw(o.Current)
	newRel := oldRel
	if o.NewRelease != nil {
		newRel = release.PublicRaw(o.NewRelease)
	}
	newRec := o.CurrentRecovery
	if o.NewRecovery != nil {
		if !keyspart.PointOK(o.NewRecovery) {
			return "", nil, errors.New("the new recovery key is not a P-256 public key (65 bytes, 0x04 || X || Y, or a PEM public key)")
		}
		newRec = o.NewRecovery
	}
	old := Keys{Release: release.Fingerprint(oldRel), Recovery: release.Fingerprint(o.CurrentRecovery)}
	nw := Keys{Release: release.Fingerprint(newRel), Recovery: release.Fingerprint(newRec)}
	switch {
	case o.Compromised && nw.Release == old.Release:
		return "", nil, errors.New("-compromised needs a new release key (-new-release generate): the old one must sign nothing more")
	case nw == old:
		return "", nil, errors.New("nothing changes: give a new release key (-new-release), a new recovery key (-new-recovery), or both")
	case nw.Release == nw.Recovery:
		return "", nil, errors.New("the new release and recovery keys are the same key")
	case nw.Release == old.Recovery:
		return "", nil, errors.New("the new release key is the recovery key the nodes trust now: the recovery key stays offline")
	case nw.Recovery == old.Release:
		return "", nil, errors.New("the new recovery key is the release key the controller holds: the recovery key stays offline")
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	p := &Plan{Version: 1, Created: now.UTC(), Compromised: o.Compromised, Old: old, New: nw,
		OldRecoveryPub: hex.EncodeToString(o.CurrentRecovery), NewReleasePub: hex.EncodeToString(newRel),
		NewRecoveryPub: hex.EncodeToString(newRec), NewReleaseKey: nw.Release != old.Release}
	type files struct{ m, pl, info []byte }
	var nf []files
	for _, t := range o.Nodes {
		st := t.Status
		id, err := release.ParseMAC(t.NodeID)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", t.Addr, err)
		}
		switch {
		case !strings.EqualFold(st.NodeID, t.NodeID):
			return "", nil, fmt.Errorf("%s answers as node %q, but node %s is pinned there: not prepared", t.Addr, st.NodeID, t.NodeID)
		case st.Image == "":
			return "", nil, fmt.Errorf("%s runs firmware from before chip images: update it first", t.Addr)
		case st.KeysSource != "partition" && st.KeysSource != "seeded":
			why := "its keys are not from its keys partition (keys_source " + st.KeysSource + ")"
			if st.KeysSource == "" {
				why = "it runs firmware from before the keys partition"
			}
			return "", nil, fmt.Errorf("%s: %s, so it takes no KEYS release: update its firmware (or write its keys over USB) first", t.Addr, why)
		case !hasSeq(st, release.Keys):
			return "", nil, fmt.Errorf("%s runs firmware from before KEYS releases (no seq.keys in its /status): update it first", t.Addr)
		case len(st.Keys) < 2:
			return "", nil, fmt.Errorf("%s reports no keys", t.Addr)
		}
		n := &Node{Addr: t.Addr, NodeID: strings.ToLower(t.NodeID), Image: st.Image, Dir: dirOf(t.NodeID), State: StatePrepared}
		switch {
		case st.Keys[0] == nw.Release && st.Keys[1] == nw.Recovery:
			n.State = StateKeys // it has them already (an earlier rotation got this far): push signs its releases again
		case st.Keys[0] != old.Release:
			return "", nil, fmt.Errorf("%s trusts the release key %s, not the controller's %s: not prepared", t.Addr, st.Keys[0], old.Release)
		case st.Keys[1] != old.Recovery:
			return "", nil, fmt.Errorf("%s trusts the recovery key %s, not %s: not prepared (give the recovery public key it trusts)",
				t.Addr, st.Keys[1], old.Recovery)
		}
		last := st.Seq[release.Keys.String()]
		if o.LastSeq != nil {
			last = max(last, o.LastSeq(n.NodeID))
		}
		if n.Seq, err = release.NextSeq(last, now); err != nil {
			return "", nil, fmt.Errorf("%s: %w", t.Addr, err)
		}
		if o.Repush != nil {
			n.Repush, n.Missing = o.Repush(st)
			if o.Compromised && len(n.Missing) > 0 {
				return "", nil, fmt.Errorf("%s stores what the controller can't sign again (%s): after a compromise it would not load "+
					"at its next boot. Push it from the controller first (its node page, or an apply), then prepare again",
					t.Addr, strings.Join(n.Missing, "; "))
			}
		}
		m, pl, info, err := NodeFiles(id, st.Image, n.Seq, newRel, newRec, p.KeepRetired(), old.Recovery)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", t.Addr, err)
		}
		p.Nodes = append(p.Nodes, n)
		nf = append(nf, files{m, pl, info})
	}

	// The new release key: release.next.pem, unless it holds this key already (prepared
	// again); another one there is a rotation under way.
	next, err := keys.ReadNext(keys.NextPath(o.DataDir))
	switch {
	case err != nil:
		return "", nil, fmt.Errorf("%s: %v (no rotation is under way: move it away)", keys.NextPath(o.DataDir), err)
	case next != nil && (o.NewRelease == nil || !next.Equal(o.NewRelease)):
		return "", nil, fmt.Errorf("%s holds a new release key (%s) no rotation uses: move it away, or give it as -new-release",
			keys.NextPath(o.DataDir), keys.Fingerprint(next))
	}
	root := Root(o.DataDir)
	if err := secfile.MkdirAll(root); err != nil {
		return "", nil, err
	}
	base := now.UTC().Format("20060102-150405")
	var dir string
	for i := 1; ; i++ {
		p.ID = base
		if i > 1 {
			p.ID = fmt.Sprintf("%s-%d", base, i)
		}
		dir = filepath.Join(root, p.ID)
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	write := func() error {
		for i, n := range p.Nodes {
			d := filepath.Join(dir, n.Dir)
			for name, b := range map[string][]byte{ManifestFile: nf[i].m, PayloadFile: nf[i].pl, InfoFile: nf[i].info} {
				if err := secfile.Write(filepath.Join(d, name), b); err != nil {
					return err
				}
			}
		}
		if err := p.Save(dir); err != nil {
			return err
		}
		if o.NewRelease == nil || next != nil {
			return nil
		}
		b, err := keys.Encode(o.NewRelease)
		if err != nil {
			return err
		}
		return secfile.Create(keys.NextPath(o.DataDir), b)
	}
	if err := write(); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	return dir, p, nil
}

func hasSeq(st release.NodeStatus, k release.Kind) bool {
	_, ok := st.Seq[k.String()]
	return ok
}

// nodeRelease reads a node's prepared files and checks them against the plan, as
// release.py keys-sign does: manifest.bin, payload.bin and keys.json describe one KEYS
// release, for this node, with the plan's new keys.
func (p *Plan) nodeRelease(dir string, n *Node) (manifest, payload []byte, err error) {
	d := filepath.Join(dir, n.Dir)
	if manifest, err = os.ReadFile(filepath.Join(d, ManifestFile)); err != nil {
		return nil, nil, err
	}
	if payload, err = os.ReadFile(filepath.Join(d, PayloadFile)); err != nil {
		return nil, nil, err
	}
	infoB, err := os.ReadFile(filepath.Join(d, InfoFile))
	if err != nil {
		return nil, nil, err
	}
	var info Info
	if err := json.Unmarshal(infoB, &info); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Join(d, InfoFile), err)
	}
	id, _ := release.ParseMAC(n.NodeID)
	rel, rec, keep, err := release.ParseKeysPayload(payload)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", filepath.Join(d, PayloadFile), err)
	}
	want, err := release.KeysManifest(id, n.Image, n.Seq, payload)
	switch {
	case err != nil:
		return nil, nil, err
	case !bytes.Equal(want, manifest):
		return nil, nil, fmt.Errorf("%s: not the KEYS release of node %s (%s), seq %d, for %s", filepath.Join(d, ManifestFile),
			n.NodeID, n.Image, n.Seq, filepath.Join(d, PayloadFile))
	case hex.EncodeToString(rel) != p.NewReleasePub || hex.EncodeToString(rec) != p.NewRecoveryPub || keep != p.KeepRetired():
		return nil, nil, fmt.Errorf("%s: not the plan's new keys", filepath.Join(d, PayloadFile))
	case info.Node != n.NodeID || info.Image != n.Image || info.Seq != n.Seq || info.Release != p.New.Release ||
		info.Recovery != p.New.Recovery || info.KeepRetired != keep:
		return nil, nil, fmt.Errorf("%s does not describe %s and %s", filepath.Join(d, InfoFile), ManifestFile, PayloadFile)
	}
	return manifest, payload, nil
}

// Signed is a node's signed KEYS release (release.bin), checked: the release prepared for it,
// signed with the recovery key it trusts now. os.ErrNotExist (errors.Is) if not signed yet.
func (p *Plan) Signed(dir string, n *Node) ([]byte, error) {
	m, pl, err := p.nodeRelease(dir, n)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, n.Dir, SignedFile))
	if err != nil {
		return nil, err
	}
	if err := p.checkSigned(n, m, pl, b); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, n.Dir, SignedFile), err)
	}
	return b, nil
}

func (p *Plan) checkSigned(n *Node, m, pl, b []byte) error {
	rec, err := unhex("old_recovery_pub", p.OldRecoveryPub)
	if err != nil {
		return err
	}
	id, _ := release.ParseMAC(n.NodeID)
	if _, _, _, err := release.VerifyKeys(rec, id, b); err != nil {
		return fmt.Errorf("not the KEYS release for %s signed with the recovery key %s: %v", n.NodeID, p.Old.Recovery, err)
	}
	if !bytes.Equal(b[:release.ManifestLen], m) || !bytes.Equal(b[release.HeaderLen:], pl) {
		return fmt.Errorf("signed, but not the release prepared for %s", n.NodeID)
	}
	return nil
}

// AddSigned takes a signed KEYS release made elsewhere (release.py keys-sign, or espdns key
// rotate sign on another machine): checked to be one of the plan's nodes' release, signed
// with the recovery key they trust, then kept as that node's release.bin. It returns the node.
func (p *Plan) AddSigned(dir string, b []byte) (*Node, error) {
	if len(b) != release.HeaderLen+release.KeysPayloadLen {
		return nil, errors.New("not a signed KEYS release (release.bin)")
	}
	target := b[10:16]
	for _, n := range p.Nodes {
		id, _ := release.ParseMAC(n.NodeID)
		if !bytes.Equal(id[:], target) {
			continue
		}
		m, pl, err := p.nodeRelease(dir, n)
		if err != nil {
			return nil, err
		}
		if err := p.checkSigned(n, m, pl, b); err != nil {
			return nil, err
		}
		return n, secfile.Write(filepath.Join(dir, n.Dir, SignedFile), b)
	}
	return nil, errors.New("a KEYS release for a node that isn't in this rotation")
}

// Sign signs each node's prepared KEYS release with key, the recovery private key every
// node trusts now (checked against the plan first), into its release.bin. It reads nothing
// from the network and writes the key nowhere. It returns a line per node for a person.
func Sign(dir string, key *ecdsa.PrivateKey) ([]string, error) {
	p, err := Load(dir)
	if err != nil {
		return nil, err
	}
	pub := release.PublicRaw(key)
	fp := release.Fingerprint(pub)
	switch {
	case fp == p.New.Release || fp == p.Old.Release:
		return nil, fmt.Errorf("this is a release key (%s), not the recovery key: KEYS releases are signed with the recovery key %s", fp, p.Old.Recovery)
	case hex.EncodeToString(pub) != p.OldRecoveryPub:
		return nil, fmt.Errorf("this is key %s, not the recovery key every node in the rotation trusts (%s): nothing signed", fp, p.Old.Recovery)
	}
	type signed struct {
		n *Node
		b []byte
	}
	var out []signed
	for _, n := range p.Nodes {
		if n.State != StatePrepared {
			continue
		}
		m, pl, err := p.nodeRelease(dir, n)
		if err != nil {
			return nil, err
		}
		sig, err := release.Sign(key, m)
		if err != nil {
			return nil, err
		}
		b := append(append(bytes.Clone(m), sig...), pl...)
		if err := p.checkSigned(n, m, pl, b); err != nil {
			return nil, err
		}
		out = append(out, signed{n, b})
	}
	if len(out) == 0 {
		return nil, errors.New("every node in this rotation has taken its KEYS release already: nothing to sign")
	}
	retired := "kept as each node's retired key until its releases are pushed again"
	if !p.KeepRetired() {
		retired = "not kept (compromised: dropped at once)"
		if p.Old.Release == p.New.Release {
			retired = "the release key stays"
		}
	}
	lines := []string{fmt.Sprintf("signing with the recovery key %s; new keys: release %s, recovery %s; the old release key %s: %s",
		fp, p.New.Release, p.New.Recovery, p.Old.Release, retired)}
	for _, s := range out {
		if err := secfile.Write(filepath.Join(dir, s.n.Dir, SignedFile), s.b); err != nil {
			return lines, err
		}
		lines = append(lines, fmt.Sprintf("%s (node %s, %s), seq %d: release %s -> %s, recovery %s -> %s: signed (%s)", s.n.Addr,
			s.n.NodeID, s.n.Image, s.n.Seq, p.Old.Release, p.New.Release, p.Old.Recovery, p.New.Recovery,
			filepath.Join(s.n.Dir, SignedFile)))
	}
	return lines, nil
}

// Discard removes a rotation no node has taken a KEYS release of: its directory, and
// release.next.pem when no other rotation uses its key.
func Discard(dataDir, dir string) error {
	p, err := Load(dir)
	if err != nil {
		return err
	}
	if p.Started() && p.Finished.IsZero() {
		return errors.New("a node has taken its KEYS release already: this rotation can't be discarded; push it to the end")
	}
	if !p.Finished.IsZero() {
		return errors.New("this rotation is finished: kept for the record")
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if !p.NewReleaseKey {
		return nil
	}
	for _, o := range List(dataDir) {
		if o.NewReleaseKey && o.New.Release == p.New.Release && o.Finished.IsZero() {
			return nil
		}
	}
	next, err := keys.ReadNext(keys.NextPath(dataDir))
	if err == nil && next != nil && keys.Fingerprint(next) == p.New.Release {
		return os.Remove(keys.NextPath(dataDir))
	}
	return nil
}

// ParsePublic reads a public key file: the raw 65-byte point (recovery.pub, as
// tools/keygen.py and the first run write it) or a PEM public key. A private key is refused.
func ParsePublic(name string, b []byte) ([]byte, error) {
	if keyspart.PointOK(b) {
		return b, nil
	}
	t := bytes.TrimSpace(b)
	if bytes.Contains(t, []byte("PRIVATE KEY")) {
		return nil, fmt.Errorf("%s is a private key: give its public half (the private one stays offline)", name)
	}
	blk, _ := pem.Decode(t)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("%s: not a P-256 public key (65 bytes, 0x04 || X || Y, or a PEM public key)", name)
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%s: not a P-256 public key", name)
	}
	raw, err := ek.Bytes()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return raw, nil
}

// Pending is the rotation under way in dataDir (prepared, not finished), newest first; nil if
// none.
func Pending(dataDir string) *Plan {
	for _, p := range List(dataDir) {
		if p.Finished.IsZero() {
			return p
		}
	}
	return nil
}
