package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/adoption"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/pins"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The node lookup behind Nodes' "+ Add node": the nodes found on the network that aren't in
// settings.json yet, and one looked up at an address typed, each shown as what it is (its
// board, image, firmware and name, from its /status) and what comes next. Adding it is the
// Adopt flow (adopt.go: POST /api/adopt/preview, then the adopt jobs), or the settings-add
// job for one adopted already; nothing here changes a node or a file.
//
//	GET  /api/nodes/found   the nodes found over mDNS or looked up, not in settings.json
//	POST /api/nodes/lookup  {"address": "192.0.2.10"}: the node at that address, read there only
//	GET  /api/nodes/waiting the boards installed over USB, waiting to be added (waiting.go)
//	POST /api/nodes/waiting {"name", "board", "id", "address", "gateway", "node_key"}: one more
//	POST /api/nodes/waiting/{id}/add     one on DHCP found, its address reserved: added
//	POST /api/nodes/waiting/{id}/retry   a failed one waited for again
//	POST /api/nodes/waiting/{id}/delete  one removed (its config file stays)
//
// A lookup reads /status at the address typed, and nowhere else, within a few seconds
// (nodes.Registry.Lookup): no name is resolved (an IPv4 address only), no other host is
// asked. One in settings.json is read too: a node installed over USB again is back once it
// answers there (its uptime_s shorter than the time since the install began), not when it
// is merely listed. A node that answers is listed from then on, as one found over mDNS is
// (pruned when it is gone), so the adoption finds it as it finds those: a node on a network
// without DHCP, or one mDNS doesn't reach, is added by its address.

// nodeLookupTimeout bounds a lookup's request: the registry's own wait is the shorter.
const nodeLookupTimeout = 5 * time.Second

// nodeLookup reads the node at an address typed, there only (nodes.Registry.Lookup).
type nodeLookup func(ctx context.Context, addr string) (nodes.Node, error)

type addNodeServer struct {
	adopt  adoptKind
	lookup nodeLookup
	wait   waiter // Add it, for a board waiting on DHCP
}

// Next says what adds a found node.
const (
	nextAdopt        = "adopt"         // POST /api/adopt/preview with {"node": host, "node_id": id, ...}, then the adopt jobs
	nextSettingsAdd  = "settings-add"  // adopted already: the settings-add job with {"node": host}
	nextSettingsMove = "settings-move" // a node of yours at a new address: the settings-move job with {"node": host, "from": old}
)

// foundNode is a node as Add node shows it: as the Adopt page describes it, and what adds
// it ("" when nothing can yet: Refusals say why).
type foundNode struct {
	adoptNode
	Next string `json:"next"`
	// Move is the address settings.json lists this node at when this is the node itself,
	// reinstalled at another address (its ID matches that entry): Add node (and the USB panel)
	// says so, and settings-move moves that entry here. "" otherwise.
	Move string `json:"move,omitempty"`
	// Install says how to move it on when its firmware is the reason nothing adds it:
	// "usb", firmware from before node configs (before 0.0.1), which only a USB install
	// moves on (an update over the network goes to the nodes in settings.json only); "keys",
	// a node with no keys (setup mode, which takes nothing over the network), whose keys are
	// written over USB (Write keys only, which keeps its firmware and settings); "reinstall",
	// a node that trusts another controller's release key, installed again over USB with
	// this one's keys.
	Install string `json:"install,omitempty"`
}

// installUSB and installKeys are foundNode.Install for a node to install, or to write the
// keys of, over USB.
const (
	installUSB       = "usb"
	installKeys      = "keys"
	installReinstall = "reinstall"
)

func (a addNodeServer) routes(mux *routes) {
	mux.HandleFunc("GET /api/nodes/found", needLogin("adding a node needs", a.found))
	mux.HandleFunc("POST /api/nodes/lookup", needLogin("adding a node needs", a.lookupNode))
	mux.HandleFunc("GET /api/nodes/waiting", needLogin("adding a node needs", a.waiting))
	mux.HandleFunc("POST /api/nodes/waiting", needLogin("adding a node needs", a.addWaiting))
	mux.HandleFunc("POST /api/nodes/waiting/{id}/add", needLogin("adding a node needs", a.addFound))
	mux.HandleFunc("POST /api/nodes/waiting/{id}/retry", needLogin("adding a node needs", a.retryWaiting))
	mux.HandleFunc("POST /api/nodes/waiting/{id}/delete", needLogin("adding a node needs", a.deleteWaiting))
}

// view is n described for Add node.
func (a addNodeServer) view(n nodes.Node, all []nodes.Node, s settings.Settings, files []configs.File) foundNode {
	an := a.adopt.describe(n, all, s, files)
	f := foundNode{adoptNode: an, Move: movedFrom(n, all, s)}
	if st, ok := status(n); ok && f.Move != "" {
		if why := a.adopt.nodeKeyRefusal(st); why != "" {
			f.Refusals = append(f.Refusals, why)
		}
	}
	switch {
	case an.Read && !an.Listed && (an.KeysSource == "none" || an.KeysSource == "corrupt"):
		f.Install = installKeys
	case an.Read && !an.Configs && !an.Listed:
		f.Install = installUSB
	case slices.Contains(an.Refusals, otherKeyRefusal):
		f.Install = installReinstall
	}
	switch {
	case f.Move != "" && len(f.Refusals) == 0:
		f.Next = nextSettingsMove
	case len(f.Refusals) > 0 || an.Listed:
	case !an.Adopted:
		f.Next = nextAdopt
	default:
		f.Next = nextSettingsAdd
	}
	return f
}

// movedFrom is the address settings.json lists this node at when n is not listed and another
// node the controller knows, listed and silent now (as a reinstall leaves it), has the same
// ID (case-insensitively, as byID and describe compare): the address a reinstall moved it
// from. "" otherwise: while the old address answers, it is not a move.
func movedFrom(n nodes.Node, all []nodes.Node, s settings.Settings) string {
	if n.ID == "" || listed(s, n.Addr) {
		return ""
	}
	for _, o := range all {
		if o.Addr != n.Addr && !o.Online && strings.EqualFold(o.ID, n.ID) && slices.Contains(s.Nodes, o.Addr) {
			return o.Addr
		}
	}
	return ""
}

func (a addNodeServer) found(w http.ResponseWriter, r *http.Request) {
	s, err := settings.Load(settings.Path(a.adopt.dataDir))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	files, _ := configs.List(a.adopt.dataDir)
	all := a.adopt.nodes()
	out := []foundNode{}
	for _, n := range all {
		if n.Source == nodes.SourceSettings || listed(s, n.Addr) {
			continue
		}
		out = append(out, a.view(n, all, s, files))
	}
	writeJSON(w, map[string]any{"nodes": out})
}

// lookupReply is POST /api/nodes/lookup's answer.
type lookupReply struct {
	Address  string     `json:"address"`
	Listed   bool       `json:"listed"`             // in settings.json: one of the nodes already
	Found    bool       `json:"found"`              // an espDNS node answered there, just now
	Uptime   *float64   `json:"uptime_s,omitempty"` // its /status uptime_s as it answered: seconds since it started
	Answered bool       `json:"answered"`           // something answered HTTP there (not found: not a node)
	Error    string     `json:"error,omitempty"`
	Node     *foundNode `json:"node,omitempty"`
}

func (a addNodeServer) lookupNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Address string `json:"address"`
	}
	if err := body(w, r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	addr, err := lookupAddress(req.Address)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	s, err := settings.Load(settings.Path(a.adopt.dataDir))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	out := lookupReply{Address: addr, Listed: listed(s, addr)}
	files, _ := configs.List(a.adopt.dataDir)
	if a.lookup == nil {
		httpErr(w, http.StatusInternalServerError, errors.New("no node lookup here"))
		return
	}
	// A listed node is read at the address settings.json has for it (with its port, if any).
	at := addr
	if i := slices.IndexFunc(s.Nodes, func(h string) bool { return adoption.HostOnly(h) == addr }); i >= 0 {
		at = s.Nodes[i]
	}
	ctx, cancel := context.WithTimeout(r.Context(), nodeLookupTimeout)
	defer cancel()
	n, err := a.lookup(ctx, at)
	if err != nil {
		out.Answered, out.Error = errors.Is(err, nodes.ErrNotNode), fmt.Sprintf("%s: %v", addr, err)
		writeJSON(w, out)
		return
	}
	out.Found = true
	if up, ok := n.Status["uptime_s"].(float64); ok {
		out.Uptime = &up
	}
	f := a.view(n, a.adopt.nodes(), s, files)
	out.Node = &f
	writeJSON(w, out)
}

// listed says whether settings.json lists addr (with or without a port).
func listed(s settings.Settings, addr string) bool {
	return slices.ContainsFunc(s.Nodes, func(h string) bool { return h == addr || adoption.HostOnly(h) == adoption.HostOnly(addr) })
}

// Addresses no node has, beyond what netip names: "this network" (0.0.0.0/8) and the
// reserved block with the broadcast address (240.0.0.0/4, 255.255.255.255 in it).
var (
	thisNetwork = netip.MustParsePrefix("0.0.0.0/8")
	reserved    = netip.MustParsePrefix("240.0.0.0/4")
)

// lookupAddress is the address typed, checked: one IPv4 unicast address, as a node has
// (no name: nothing is resolved; no port: a node serves HTTP on 80). Private LAN ranges are
// where nodes live; loopback, link-local (169.254.0.0/16, the cloud metadata address in it),
// multicast, 0.0.0.0/8 and 240.0.0.0/4 are no node's, so nothing is sent there.
func lookupAddress(s string) (string, error) {
	a, err := netip.ParseAddr(s)
	switch {
	case s == "":
		return "", errors.New("address: type the node's IPv4 address")
	case err != nil || !a.Is4():
		return "", fmt.Errorf("address: %q is not an IPv4 address (a node's, like 192.0.2.10; names aren't looked up)", s)
	case a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || thisNetwork.Contains(a) || reserved.Contains(a):
		return "", fmt.Errorf("address: %s is no node's address", s)
	}
	return a.String(), nil
}

func (a addNodeServer) waiting(w http.ResponseWriter, r *http.Request) {
	ws, err := loadWaiting(a.adopt.dataDir)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"nodes": ws})
}

// A node's name, as + Add node takes it (addnode.js NAME), and a chip MAC, as node IDs are.
var (
	nodeName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$`)
	macID    = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
)

// addWaiting takes a board just installed over USB: its name (a config of that name is
// written with its network and the zone primary's zones, as + Add node writes a new name's;
// one there already only when it says the same network), its chip MAC, its address.
func (a addNodeServer) addWaiting(w http.ResponseWriter, r *http.Request) {
	var e waitingNode
	if err := body(w, r, &e); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	e.ID = strings.ToLower(strings.ReplaceAll(e.ID, "-", ":"))
	net := nodecfg.Network{Address: e.Address, Gateway: e.Gateway}
	switch {
	case !nodeName.MatchString(e.Name):
		httpErr(w, http.StatusBadRequest, errors.New("name: lowercase letters, digits and dashes, up to 32"))
		return
	case !macID.MatchString(e.ID):
		httpErr(w, http.StatusBadRequest, fmt.Errorf("id: %q is not a chip MAC (aa:bb:cc:dd:ee:ff)", e.ID))
		return
	}
	if err := net.Check(); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if e.NodeKey != "" {
		if _, err := release.ParseNodeKey(e.NodeKey); err != nil {
			httpErr(w, http.StatusBadRequest, fmt.Errorf("node_key: %v", err))
			return
		}
	}
	s, err := settings.Load(settings.Path(a.adopt.dataDir))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, n := range a.adopt.nodes() {
		if strings.EqualFold(n.ID, e.ID) && listed(s, n.Addr) {
			httpErr(w, http.StatusConflict, fmt.Errorf("node %s is one of your nodes already, at %s", e.ID, n.Addr))
			return
		}
	}
	if rs, _ := pins.Open(a.adopt.dataDir).List(); slices.ContainsFunc(rs, func(p pins.Record) bool { return p.ID == e.ID }) {
		httpErr(w, http.StatusConflict, fmt.Errorf("node %s was added before: add it from + Add node", e.ID))
		return
	}
	host := strings.Split(e.Address, "/")[0]
	if e.Address != "dhcp" && listed(s, host) {
		httpErr(w, http.StatusConflict, fmt.Errorf("%s is one of your nodes' addresses: give this board another", host))
		return
	}
	if err := a.waitingConfig(e, net, s, auth.User(r.Context())); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	e.User, e.Found, e.MAC = auth.User(r.Context()), "", ""
	e.Since, e.State, e.Error = time.Now().UTC(), waitingWait, ""
	err = changeWaiting(a.adopt.dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		if slices.ContainsFunc(ws, func(x waitingNode) bool { return x.Name == e.Name && x.ID != e.ID }) {
			return nil, errNameInUse
		}
		if slices.ContainsFunc(ws, func(x waitingNode) bool { return x.Address != "dhcp" && x.Address == e.Address && x.ID != e.ID }) {
			return nil, errAddressInUse
		}
		return append(slices.DeleteFunc(ws, func(x waitingNode) bool { return x.ID == e.ID }), e), nil
	})
	if errors.Is(err, errAddressInUse) {
		httpErr(w, http.StatusConflict, fmt.Errorf("%s is another board's, waiting to be added: give this board another", host))
		return
	}
	if errors.Is(err, errNameInUse) {
		httpErr(w, http.StatusConflict, fmt.Errorf("%w by another board waiting: choose another name", err))
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, e)
}

var (
	errNameInUse    = errors.New("That name is in use")
	errAddressInUse = errors.New("address in use")
)

// waitingConfig writes configs/<name>.json for e, unless it is there with that network.
func (a addNodeServer) waitingConfig(e waitingNode, net nodecfg.Network, s settings.Settings, user string) error {
	file := e.Name + ".json"
	f, err := configs.Read(a.adopt.dataDir, file)
	if err == nil {
		if f.Config == nil || f.Config.Network == nil || *f.Config.Network != net {
			return fmt.Errorf("%w (%s says another network): choose another name", errNameInUse, file)
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	c := nodecfg.Config{Name: e.Name, Network: &net}
	if zp := s.ZonePrimary(); zp.Address != "" && len(zp.Zones) > 0 {
		zones := slices.Clone(zp.Zones)
		c.Secondary = &nodecfg.Secondary{Primary: zp.Address, Zones: &zones}
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	if _, err := configs.Save(a.adopt.dataDir, file, append(b, '\n'), ""); err != nil {
		return err
	}
	logSave(a.adopt.dataDir, user, file, "new")
	return nil
}

func (a addNodeServer) addFound(w http.ResponseWriter, r *http.Request) {
	err := a.wait.addFound(r.PathValue("id"), auth.User(r.Context()))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		httpErr(w, http.StatusNotFound, fmt.Errorf("no board %s is waiting", r.PathValue("id")))
	case err != nil:
		httpErr(w, http.StatusConflict, err)
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}

func (a addNodeServer) retryWaiting(w http.ResponseWriter, r *http.Request) {
	a.changeOne(w, r, func(ws []waitingNode, i int) []waitingNode {
		ws[i].State, ws[i].Error, ws[i].Found, ws[i].MAC = waitingWait, "", "", ""
		return ws
	})
}

func (a addNodeServer) deleteWaiting(w http.ResponseWriter, r *http.Request) {
	a.changeOne(w, r, func(ws []waitingNode, i int) []waitingNode { return slices.Delete(ws, i, i+1) })
}

// changeOne changes the entry the path names.
func (a addNodeServer) changeOne(w http.ResponseWriter, r *http.Request, f func(ws []waitingNode, i int) []waitingNode) {
	id := r.PathValue("id")
	err := changeWaiting(a.adopt.dataDir, func(ws []waitingNode) ([]waitingNode, error) {
		i := slices.IndexFunc(ws, func(x waitingNode) bool { return strings.EqualFold(x.ID, id) })
		if i < 0 {
			return nil, fs.ErrNotExist
		}
		return f(ws, i), nil
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		httpErr(w, http.StatusNotFound, fmt.Errorf("no board %s is waiting", id))
	case err != nil:
		httpErr(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, map[string]any{"ok": true})
	}
}
