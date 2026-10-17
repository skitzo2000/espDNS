package adoption

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"

	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// Web is an adoption as the controller's Adopt page asks for it (the job kind "adopt"):
// a node it found, a config from the data directory, the address.
type Web struct {
	Node   string `json:"node"`    // the node's address now
	NodeID string `json:"node_id"` // its ID as the page showed it (checked at step 2)
	Config string `json:"config"`  // a file in configs/
	// Address: "" (the config's, else the one it runs on), a.b.c.d/nn, or "dhcp" (never on
	// a network in settings.json's no_dhcp); Gateway goes with a static one.
	Address  string `json:"address,omitempty"`
	Gateway  string `json:"gateway,omitempty"`
	Reserved bool   `json:"reserved,omitempty"`
	// NodeKey: the node key expected (a board waiting after its install over USB); "": any.
	NodeKey string `json:"node_key,omitempty"`
	// Static: the address as + Add node asks for it, never none (the config's): a fixed one
	// (a.b.c.d/nn) with its gateway, always given (never the config's, which may be another
	// network's), or dhcp only with Reserved (the router reserves its address for its MAC).
	// A request without it may leave them out, as the CLI may.
	Static bool `json:"static,omitempty"`
	// AddToSettings: once adopted, its address goes into settings.json.
	AddToSettings bool   `json:"add_to_settings,omitempty"`
	DryRun        bool   `json:"dry_run,omitempty"`
	After         string `json:"after,omitempty"` // the adoption: the dry run it follows
}

// ParseWeb reads an Adopt page request, unknown fields refused.
func ParseWeb(raw []byte) (Web, error) {
	var w Web
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return w, fmt.Errorf("params: %v", err)
	}
	return w, nil
}

var macRE = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// Request is the request the CLI's flags make for the same adoption, as make fleet-adopt
// runs it: -data, -mdns 0, -host and -node, -config in the data directory, the settings'
// DNS peers, no flag that loosens the rule.
func (w Web) Request(dataDir, catalog string) (Request, error) {
	r := Request{DataDir: dataDir, Catalog: catalog, Wait: DefaultWait, DryRun: w.DryRun}
	if w.Node == "" {
		return r, errors.New(`"node": choose the node to adopt`)
	}
	if !macRE.MatchString(w.NodeID) {
		return r, errors.New(`"node_id": the node's ID as found (its MAC, aa:bb:cc:dd:ee:ff)`)
	}
	if err := configs.CheckName(w.Config); err != nil {
		return r, fmt.Errorf(`"config": %w`, err)
	}
	switch {
	case w.Static && w.Address == "dhcp":
		if !w.Reserved {
			return r, errors.New(`"address": dhcp only once the node's address is reserved for its MAC in your router ` +
				`("reserved"); else a fixed address with its prefix length (192.0.2.60/24) and its gateway`)
		}
	case w.Static:
		p, err := netip.ParsePrefix(w.Address)
		if err != nil || !p.Addr().Is4() || p.Bits() < 1 {
			return r, fmt.Errorf(`"address": %q: a node added here gets a fixed address with its prefix length (192.0.2.60/24), `+
				`or dhcp reserved for it in your router`, w.Address)
		}
		if w.Gateway == "" {
			return r, fmt.Errorf(`"gateway": give the gateway for %s: the node's next hop, an address in its own network (%s)`,
				w.Address, p.Masked())
		}
		if g, err := netip.ParseAddr(w.Gateway); err == nil && g.Is4() && !p.Masked().Contains(g) {
			return r, fmt.Errorf(`"gateway": %s is not in %s, the node's own network: the gateway is its next hop, `+
				`an address in its own network, whatever routes lie beyond it`, g, p.Masked())
		}
		if err := (&nodecfg.Network{Address: w.Address, Gateway: w.Gateway}).Check(); err != nil {
			return r, fmt.Errorf("%w: the gateway is the node's next hop, another address in its own network (%s), "+
				"whatever routes lie beyond it", err, p.Masked())
		}
	case w.Address == "", w.Address == "dhcp":
	default:
		if p, err := netip.ParsePrefix(w.Address); err != nil || !p.Addr().Is4() {
			return r, fmt.Errorf(`"address": %q is not an IPv4 address with its prefix length (192.0.2.60/24)`, w.Address)
		}
	}
	if w.Gateway != "" {
		if w.Address == "dhcp" {
			return r, errors.New(`"gateway": only with a static address`)
		}
		if a, err := netip.ParseAddr(w.Gateway); err != nil || !a.Is4() {
			return r, fmt.Errorf(`"gateway": %q is not an IPv4 address`, w.Gateway)
		}
	}
	if _, err := settings.Load(settings.Path(dataDir)); err != nil {
		return r, err
	}
	r.Host, r.Node, r.Address, r.Gateway, r.Reserved, r.NodeKey = w.Node, w.NodeID, w.Address, w.Gateway, w.Reserved, w.NodeKey
	r.Config = filepath.Join(configs.Path(dataDir), w.Config)
	r.AddToSettings = w.AddToSettings
	return r, nil
}

// Key is the request's identity for a dry run: what the adoption does, without the choices
// made only at the adoption itself (the dry run it follows, the settings).
func (w Web) Key() string {
	w.DryRun, w.After, w.AddToSettings = false, "", false
	b, _ := json.Marshal(w)
	return string(b)
}
