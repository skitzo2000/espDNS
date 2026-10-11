package rotation

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// ErrStopped: a stop was asked for between two nodes.
var ErrStopped = fleet.ErrStopped

// PushOptions are what Push takes.
type PushOptions struct {
	DataDir string
	Dir     string // the rotation's directory
	// Listed are settings.json's nodes now: a node of the plan no longer listed is left out,
	// and every listed node must report the new keys before the controller's keys are swapped.
	Listed []string
	// Only, if set, are the nodes (by address) this push takes; the others are left for a
	// later push, and the controller's keys are swapped only by the push that takes the last.
	Only []string
	// Current is release.pem, Next release.next.pem (nil if there is none).
	Current, Next *ecdsa.PrivateKey
	// Client reads the nodes (Status, Health); its HTTP posts too.
	Client *fleet.Client
	Logf   func(format string, args ...any)
	// Progress hears the plan after every change to it (the controller's job progress).
	Progress func(p *Plan)
	// Healthy is how long a node has to be in service again after it (0: 2 minutes); Poll
	// how often that is asked (0: 1 s).
	Healthy, Poll time.Duration
	Stop          <-chan struct{}
	Now           func() time.Time
}

func (o *PushOptions) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
	}
}

func (o *PushOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *PushOptions) httpClient() *http.Client {
	if o.Client != nil && o.Client.HTTP != nil {
		return o.Client.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// Push pushes a signed rotation, one node at a time (the caller holds the fleet lock): each
// node's KEYS release, then everything it stores signed again with the new release key
// (Collect), then its health; then the next node. The first failure stops it, said with the
// node (its Error in the plan); the nodes before it stay done, and Push again goes on from the
// node that failed. When every listed node reports the new keys, the controller's keys are
// swapped (Finish). It returns the plan as it ends.
func Push(ctx context.Context, o PushOptions) (*Plan, error) {
	p, err := Load(o.Dir)
	if err != nil {
		return nil, err
	}
	if !p.Finished.IsZero() {
		return p, fmt.Errorf("rotation %s finished %s: nothing to push", p.ID, p.Finished.Format(time.RFC3339))
	}
	newKey, err := p.newKey(o.Current, o.Next)
	if err != nil {
		return p, err
	}
	save := func() error {
		if err := p.Save(o.Dir); err != nil {
			return err
		}
		if o.Progress != nil {
			o.Progress(p)
		}
		return nil
	}
	listed := func(n *Node) bool { return slices.Contains(o.Listed, n.Addr) }
	taken := func(n *Node) bool { return len(o.Only) == 0 || slices.Contains(o.Only, n.Addr) }
	for _, a := range o.Only {
		if !slices.ContainsFunc(p.Nodes, func(n *Node) bool { return n.Addr == a }) {
			return p, fmt.Errorf("%s is not a node of rotation %s", a, p.ID)
		}
	}
	// Every node still to take its KEYS release has it signed, before any node is touched.
	for _, n := range p.Nodes {
		if n.State != StatePrepared || !listed(n) || !taken(n) {
			continue
		}
		if _, err := p.Signed(o.Dir, n); errors.Is(err, fs.ErrNotExist) {
			return p, fmt.Errorf("%s: not signed yet: sign the rotation offline first (espdns key rotate sign -dir %s -key -)", n.Addr, o.Dir)
		} else if err != nil {
			return p, fmt.Errorf("%s: %w", n.Addr, err)
		}
	}
	o.logf("rotation %s: release %s -> %s, recovery %s -> %s%s", p.ID, p.Old.Release, p.New.Release, p.Old.Recovery, p.New.Recovery,
		map[bool]string{true: " (compromised: the old release key is dropped at once)", false: ""}[p.Compromised])
	pusher := &release.Pusher{Key: newKey, KeyID: release.KeyRelease, Pins: pins.Open(o.DataDir), Client: o.httpClient(), Now: o.Now}
	for _, n := range p.Nodes {
		select {
		case <-o.Stop:
			return p, ErrStopped
		default:
		}
		if !listed(n) {
			o.logf("%s: no longer in settings.json: left out", n.Addr)
			continue
		}
		if n.State == StateDone {
			o.logf("%s: done already", n.Addr)
			continue
		}
		if !taken(n) {
			o.logf("%s: left for a later push", n.Addr)
			continue
		}
		err := p.pushNode(ctx, &o, n, pusher, save)
		if err != nil {
			n.Error = err.Error()
			if serr := save(); serr != nil {
				o.logf("plan.json: %v", serr)
			}
			return p, fmt.Errorf("%s: %w (the nodes before it are done; push again to go on from it)", n.Addr, err)
		}
		n.Error = ""
		n.State, n.DoneAt = StateDone, o.now().UTC()
		if err := save(); err != nil {
			return p, err
		}
		o.logf("%s: done: new keys in use, what it stores signed again, healthy", n.Addr)
	}
	var left []string
	for _, n := range p.Nodes {
		if listed(n) && n.State != StateDone {
			left = append(left, n.Addr)
		}
	}
	if len(left) > 0 {
		o.logf("left for a later push: %s; the controller's keys are swapped once they are done", strings.Join(left, ", "))
		return p, nil
	}
	return p, p.finish(ctx, &o, save)
}

// newKey is the rotation's new release key, of current (release.pem) and next
// (release.next.pem).
func (p *Plan) newKey(current, next *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	for _, k := range []*ecdsa.PrivateKey{next, current} {
		if k != nil && keys.Fingerprint(k) == p.New.Release {
			return k, nil
		}
	}
	if p.NewReleaseKey {
		return nil, fmt.Errorf("the rotation's new release key %s is not in release.next.pem (nor release.pem): it was moved or replaced", p.New.Release)
	}
	return nil, fmt.Errorf("release.pem is not the release key %s this rotation keeps", p.New.Release)
}

func keysAre(st release.NodeStatus, k Keys) bool {
	return len(st.Keys) >= 2 && st.Keys[0] == k.Release && st.Keys[1] == k.Recovery
}

// pushNode takes one node from where it is to done.
func (p *Plan) pushNode(ctx context.Context, o *PushOptions, n *Node, pusher *release.Pusher, save func() error) error {
	c := o.Client
	ledger := pins.Open(o.DataDir)
	id, err := ledger.Pinned(n.Addr)
	if err != nil {
		return err
	}
	if !strings.EqualFold(id, n.NodeID) {
		return fmt.Errorf("node %s is pinned there now, not %s: prepare a new rotation", id, n.NodeID)
	}
	st, err := c.Status(ctx, n.Addr)
	if err != nil {
		return err
	}
	if !strings.EqualFold(st.NodeID, n.NodeID) {
		return fmt.Errorf("it answers as node %q, not %s: nothing sent", st.NodeID, n.NodeID)
	}
	var allowed []string
	if h, err := c.Health(ctx, n.Addr); err == nil {
		allowed = h.Reasons
	}
	allowed = append(allowed, "reboot pending", "config on trial")

	if n.State == StatePrepared {
		switch {
		case keysAre(st, p.New):
			o.logf("%s: it has the new keys already (an earlier push's reply was lost)", n.Addr)
		case keysAre(st, p.Old):
			if err := p.sendKeys(ctx, o, n, st, ledger); err != nil {
				return err
			}
		default:
			return fmt.Errorf("it trusts %v: neither the rotation's old keys (%s, %s) nor its new ones", st.Keys, p.Old.Release, p.Old.Recovery)
		}
		n.State, n.KeysAt = StateKeys, o.now().UTC()
		if err := save(); err != nil {
			return err
		}
	}

	// Everything it stores, signed again with the new release key.
	if st, err = c.Status(ctx, n.Addr); err != nil {
		return err
	}
	if !keysAre(st, p.New) {
		return fmt.Errorf("it reports keys %v, not the new ones (%s, %s)", st.Keys, p.New.Release, p.New.Recovery)
	}
	items, missing := Collect(o.DataDir, st)
	if p.Compromised && len(missing) > 0 {
		return fmt.Errorf("it stores what the controller can't sign again (%s), which won't load at its next boot (the old key was "+
			"dropped): push it from the controller now (it signs with the new key), then push the rotation again",
			strings.Join(missing, "; "))
	}
	n.Repush, n.Missing = nil, missing
	for _, it := range items {
		out, err := pusher.PushRelease(ctx, n.Addr, it.Kind, it.Payload)
		if err != nil {
			return fmt.Errorf("its %s, signed again with the new release key: %w", it.What, err)
		}
		if it.Record != nil {
			if err := it.Record(n.Addr, n.NodeID, out.Seq); err != nil {
				o.logf("%s: %s not recorded as what it runs: %v", n.Addr, it.What, err)
			}
		}
		o.logf("%s: %s signed again with the new release key: %s", n.Addr, it.What, out.Reply)
		w := fmt.Sprintf("%s (seq %d)", it.What, out.Seq)
		if out.Node.RebootPending {
			w += ", in use from its next restart"
		}
		n.Repush = append(n.Repush, w)
	}
	for _, m := range missing {
		o.logf("%s: not signed again: %s. It still loads with the retired key until it is pushed again", n.Addr, m)
	}
	if err := save(); err != nil {
		return err
	}
	return p.waitHealthy(ctx, o, n, allowed)
}

// sendKeys posts the node's signed KEYS release and checks it took it.
func (p *Plan) sendKeys(ctx context.Context, o *PushOptions, n *Node, st release.NodeStatus, ledger *pins.Ledger) error {
	if p.Compromised {
		if _, missing := Collect(o.DataDir, st); len(missing) > 0 {
			return fmt.Errorf("it stores what the controller can't sign again (%s), which would not load at its next boot once the "+
				"old key is dropped: push it from the controller first, then push the rotation again", strings.Join(missing, "; "))
		}
	}
	b, err := p.Signed(o.Dir, n)
	if err != nil {
		return err
	}
	// The seq: recorded before it is sent, as every release's (internal/pins). The same one
	// again is this release sent again (a push that stopped before the node took it).
	last, err := ledger.Seq(n.NodeID, release.Keys.String())
	if err != nil {
		return err
	}
	switch {
	case st.Seq[release.Keys.String()] >= n.Seq:
		return fmt.Errorf("it took KEYS seq %d already, not below this one's %d: prepare a new rotation", st.Seq[release.Keys.String()], n.Seq)
	case last > n.Seq:
		return fmt.Errorf("a later KEYS release (seq %d) was signed for it since this rotation was prepared: prepare a new rotation", last)
	case last < n.Seq:
		if err := ledger.SetSeq(n.NodeID, release.Keys.String(), n.Seq); err != nil {
			return err
		}
	}
	o.logf("%s: sending its KEYS release (seq %d): release %s, recovery %s", n.Addr, n.Seq, p.New.Release, p.New.Recovery)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+n.Addr+"/release", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := o.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("its KEYS release (seq %d) was refused: %s %s", n.Seq, resp.Status, strings.TrimSpace(string(body)))
	}
	var reply struct {
		Message string   `json:"message"`
		Keys    []string `json:"keys"`
	}
	json.Unmarshal(body, &reply)
	o.logf("%s: %s", n.Addr, strings.TrimSpace(reply.Message))
	st2, err := o.Client.Status(ctx, n.Addr)
	if err != nil {
		return fmt.Errorf("took its KEYS release, then: %w", err)
	}
	if !keysAre(st2, p.New) {
		return fmt.Errorf("took its KEYS release, but reports keys %v, not the new ones (%s, %s)", st2.Keys, p.New.Release, p.New.Recovery)
	}
	if st.KeysGen != nil && st2.KeysGen != nil && *st2.KeysGen <= *st.KeysGen {
		return fmt.Errorf("took its KEYS release, but its keys generation is %d, not above %d", *st2.KeysGen, *st.KeysGen)
	}
	switch {
	case p.KeepRetired() && st2.KeysRetired != p.Old.Release:
		o.logf("%s: warning: its retired key is %q, not the old release key %s", n.Addr, st2.KeysRetired, p.Old.Release)
	case p.Compromised && st2.KeysRetired != "":
		return fmt.Errorf("took its KEYS release, but keeps %s as its retired key after a compromise", st2.KeysRetired)
	}
	return nil
}

// waitHealthy waits for the node to be in service, degraded at most for the reasons in
// allowed (those it had before, a pending restart).
func (p *Plan) waitHealthy(ctx context.Context, o *PushOptions, n *Node, allowed []string) error {
	wait, poll := o.Healthy, o.Poll
	if wait <= 0 {
		wait = 2 * time.Minute
	}
	if poll <= 0 {
		poll = time.Second
	}
	end := time.Now().Add(wait)
	for {
		h, err := o.Client.Health(ctx, n.Addr)
		if err == nil {
			if err = h.InService(allowed); err == nil {
				return nil
			}
		}
		if time.Now().After(end) {
			return fmt.Errorf("not healthy %v after its keys changed: %v", wait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// ErrNotFinished: a listed node doesn't report the new keys yet; the controller's keys stay.
var ErrNotFinished = errors.New("not every listed node reports the new keys")

// finish swaps the controller's keys once every listed node reports the new ones:
// release.next.pem becomes release.pem (the old one kept as release.prev.pem), recovery.pub
// becomes the new recovery key, and the plan is finished.
func (p *Plan) finish(ctx context.Context, o *PushOptions, save func() error) error {
	var not []string
	for _, a := range o.Listed {
		st, err := o.Client.Status(ctx, a)
		switch {
		case err != nil:
			not = append(not, fmt.Sprintf("%s (not read: %v)", a, err))
		case !keysAre(st, p.New):
			not = append(not, fmt.Sprintf("%s (keys %v)", a, st.Keys))
		}
	}
	if len(not) > 0 {
		return fmt.Errorf("%w (%s): the controller's keys are not swapped yet. A node that doesn't answer: push again once it "+
			"does. A node listed after this rotation was prepared: take it out of settings.json to finish, then write its "+
			"keys over USB (Install over USB, Write keys only) and add it back", ErrNotFinished, strings.Join(not, "; "))
	}
	if p.NewReleaseKey {
		cur, err := keys.FileSource{Path: keys.Path(o.DataDir)}.Key()
		if err != nil {
			return err
		}
		if keys.Fingerprint(cur) != p.New.Release {
			b, err := keys.Encode(cur)
			if err != nil {
				return err
			}
			if err := secfile.Write(keys.PrevPath(o.DataDir), b); err != nil {
				return err
			}
			next, err := keys.ReadNext(keys.NextPath(o.DataDir))
			if err != nil || next == nil || keys.Fingerprint(next) != p.New.Release {
				return fmt.Errorf("%s is not the new release key %s: not swapped", keys.NextPath(o.DataDir), p.New.Release)
			}
			if err := os.Rename(keys.NextPath(o.DataDir), keys.Path(o.DataDir)); err != nil {
				return err
			}
			o.logf("the controller's release key is %s now (%s); the old one, %s, is kept in %s", p.New.Release, keys.Path(o.DataDir),
				p.Old.Release, keys.PrevPath(o.DataDir))
		}
	}
	newRec, _ := hex.DecodeString(p.NewRecoveryPub)
	if rec, err := keys.ReadRecovery(o.DataDir); err != nil || !bytes.Equal(rec, newRec) {
		if err := secfile.Write(keys.RecoveryPath(o.DataDir), newRec); err != nil {
			return err
		}
		o.logf("the recovery public key is %s now (%s)", p.New.Recovery, keys.RecoveryPath(o.DataDir))
	}
	p.Finished = o.now().UTC()
	if err := save(); err != nil {
		return err
	}
	o.logf("rotation %s finished: every listed node trusts release %s, recovery %s", p.ID, p.New.Release, p.New.Recovery)
	return nil
}
