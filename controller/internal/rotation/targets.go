package rotation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
)

// Targets are the nodes listed in settings.json, each with the node ID pinned to its
// address (internal/pins) and its /status read now. A node not pinned, or not answering,
// is an error: a rotation leaves no listed node behind.
func Targets(ctx context.Context, dataDir string, listed []string, c *fleet.Client) ([]Target, error) {
	ledger := pins.Open(dataDir)
	var out []Target
	for _, a := range listed {
		id, err := ledger.Pinned(a)
		if errors.Is(err, pins.ErrNotPinned) {
			return nil, fmt.Errorf("%s is listed but not pinned to a node: adopt it (or espdns pin) first", a)
		}
		if err != nil {
			return nil, err
		}
		st, err := c.Status(ctx, a)
		if err != nil {
			return nil, fmt.Errorf("%s: %w (every listed node must answer to be prepared)", a, err)
		}
		out = append(out, Target{Addr: a, NodeID: id, Status: st})
	}
	return out, nil
}

// RefuseOutOfService refuses to prepare a rotation while a listed node is out of service
// (settings.json "out_of_service"): a rotation leaves no listed node behind, and one out of
// service is sent nothing, so it would be left trusting the old keys.
func RefuseOutOfService(out []string) error {
	if len(out) == 0 {
		return nil
	}
	return fmt.Errorf("%s out of service: return it to service, or remove it from the group, before rotating keys "+
		"(a rotation leaves no listed node behind)", strings.Join(out, ", "))
}

// Includes is the rotation under way (Pending) when it includes the node at addr, else nil:
// such a node is neither taken out of service nor removed until the rotation is finished
// or discarded.
func Includes(dataDir, addr string) *Plan {
	p := Pending(dataDir)
	if p == nil || !slices.ContainsFunc(p.Nodes, func(n *Node) bool { return n.Addr == addr }) {
		return nil
	}
	return p
}

// LastSeq is the last KEYS seq the record in dataDir has signed for a node (0 for none).
func LastSeq(dataDir string) func(string) uint64 {
	ledger := pins.Open(dataDir)
	return func(id string) uint64 {
		s, _ := ledger.Seq(id, release.Keys.String())
		return s
	}
}

// CurrentRecovery is the recovery public key the nodes trust now: given (from a file), else
// <data>/keys/recovery.pub, else the new recovery key when it is that key (the same one kept).
// It must be the one every target reports in slot 1.
func CurrentRecovery(dataDir string, given, newRecovery []byte, ts []Target) ([]byte, error) {
	trusted := ""
	for _, t := range ts {
		if len(t.Status.Keys) < 2 {
			return nil, fmt.Errorf("%s reports no keys", t.Addr)
		}
		if trusted == "" {
			trusted = t.Status.Keys[1]
		}
		if t.Status.Keys[1] != trusted {
			return nil, fmt.Errorf("the nodes trust different recovery keys (%s trusts %s, another %s): rotate them apart", t.Addr, t.Status.Keys[1], trusted)
		}
	}
	cands := [][]byte{given}
	if given == nil {
		if rec, err := keys.ReadRecovery(dataDir); err == nil {
			cands = append(cands, rec)
		}
		cands = append(cands, newRecovery)
	}
	for _, c := range cands {
		if c != nil && release.Fingerprint(c) == trusted {
			return c, nil
		}
	}
	if given != nil {
		return nil, fmt.Errorf("the recovery public key given is %s, but the nodes trust %s", release.Fingerprint(given), trusted)
	}
	return nil, fmt.Errorf("the controller doesn't have the recovery public key the nodes trust (%s): give it (-recovery-pub "+
		"recovery.pub, its public half only)", trusted)
}

// SignCommand is the offline signing of rotation id as a person runs it from the
// controller's directory (where its data/ is, compose.yaml's default): the rotation's
// directory alone mounted into the controller's image, no network, the recovery key on
// standard input (read, never written).
func SignCommand(id string) string {
	return fmt.Sprintf(`docker run --rm -i --network none --user "$(id -u):$(id -g)" -v "$PWD/data/keys/rotation/%s:/rotation" `+
		`--entrypoint /espdns espdns-controller key rotate sign -dir /rotation -key - < recovery.pem`, id)
}

// Repush is Collect in words, for Prepare: what a node's push will sign again, and what it
// can't.
func Repush(dataDir string) func(release.NodeStatus) ([]string, []string) {
	return func(st release.NodeStatus) ([]string, []string) {
		items, missing := Collect(dataDir, st)
		return Words(items), missing
	}
}
