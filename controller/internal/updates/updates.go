// Package updates is update availability for the Nodes page: for each node, whether it runs
// an older version than the newest one the controller knows (its /status), said as versions
// (version.go). The newest version is the newest release the forge lists (Latest: asked for
// with nothing downloaded), or a newer chip image held in the data directory (your own
// import). Versions are the same on every chip and board: a node on an older one is out of
// date, whatever it runs on. Offering an update makes a pending firmware change
// (internal/changes) of the node's chip image (images/<image>), which the apply job fetches
// if it isn't held at that version and sends; nothing here pushes to a node.
package updates

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
)

// A node's state, against the newest version.
const (
	Available = "available" // it runs an older version: an update is offered
	Current   = "current"   // it runs the newest version
	Newer     = "newer"     // it runs a newer version than the newest known
	Unknown   = "unknown"   // not seen yet, its version isn't one, or no newest is known
)

// maxName is the most of a node's config name shown (a config name is a file name).
const maxName = 64

// latestFile keeps the newest release version the forge listed (SetLatest), in the data
// directory: a restart without the network still knows it.
const latestFile = "firmware/latest"

// Latest is the newest release version the forge listed ("": none known yet).
func Latest(dataDir string) string {
	b, _ := os.ReadFile(filepath.Join(dataDir, latestFile))
	return strings.TrimSpace(string(b))
}

// SetLatest keeps v as the newest release version the forge lists.
func SetLatest(dataDir, v string) error {
	p := filepath.Join(dataDir, latestFile)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(v+"\n"), 0o644)
}

// Build is one firmware build in the data directory.
type Build struct {
	Source string `json:"source"` // builds/<board> or images/<image>: what a firmware change names
	Image  string `json:"image,omitempty"`
	Board  string `json:"board,omitempty"`
	Version
	Label string `json:"label,omitempty"` // where it came from: "project release v0.0.3", "your build"
	Error string `json:"error,omitempty"` // why it can't be offered
}

// Node is a node to report: its host, whether settings.json lists it (only those take a
// firmware change), and its last /status (nil: not seen yet).
type Node struct {
	Host         string
	Listed       bool
	OutOfService bool
	Online       bool
	Status       *release.NodeStatus
}

// NodeUpdate is one node's update availability.
type NodeUpdate struct {
	Host   string `json:"host"`
	Name   string `json:"name,omitempty"` // the config name it reports
	Image  string `json:"image,omitempty"`
	Board  string `json:"board,omitempty"`
	Listed bool   `json:"listed"`
	Online bool   `json:"online"`
	// OutOfService: listed, but out of service (settings.json "out_of_service"): no update is
	// offered for it; returning it to service brings its firmware up to date.
	OutOfService bool `json:"out_of_service,omitempty"`
	// Runs is the firmware it runs (nil: unknown); Newest the newest version (nil: none known).
	Runs   *Version `json:"runs,omitempty"`
	Newest *Version `json:"newest,omitempty"`
	State  string   `json:"state"`
	Why    string   `json:"why"` // the state in words
	// Pending is the pending firmware change that names it, if any (its ID and summary).
	Pending        string `json:"pending,omitempty"`
	PendingSummary string `json:"pending_summary,omitempty"`
}

// Offered says whether an update is offered for it now: available, listed and in service,
// none pending.
func (n NodeUpdate) Offered() bool {
	return n.State == Available && n.Listed && !n.OutOfService && n.Pending == ""
}

// Report is the fleet's update availability.
type Report struct {
	Scheme string       `json:"scheme"` // how versions are ordered (Scheme)
	Newest string       `json:"newest"` // the newest version ("": none known)
	Nodes  []NodeUpdate `json:"nodes"`
	Builds []Build      `json:"builds"`
	// Available are the hosts an update is offered for ("Update all").
	Available []string `json:"available"`
}

// Builds reads the firmware builds in the data directory (rolling.ReadFirmware).
func Builds(dataDir, catalog string) []Build {
	out := []Build{}
	for _, f := range rolling.ReadFirmware(dataDir, catalog) {
		// An error names files by their place in the data directory, never its path: the
		// read is open before a password is set.
		e := strings.ReplaceAll(f.Error, filepath.Clean(dataDir)+string(filepath.Separator), "")
		b := Build{Source: f.Source, Image: f.Image, Board: f.Board, Label: f.Label, Error: e}
		if f.Error == "" || f.Elf != "" {
			b.Version = Of(f.Version, f.Built, f.Elf)
		}
		out = append(out, b)
	}
	return out
}

// Newest is the newest version: latest (Latest), or a newer chip image among the builds.
func Newest(latest string, builds []Build) Version {
	n := Of(latest, "", "")
	for _, b := range builds {
		if b.Error == "" && b.ok && strings.HasPrefix(b.Source, "images/") && (!n.ok || b.sem.Compare(n.sem) > 0) {
			n = Of(b.Text, "", "")
		}
	}
	return n
}

// For is the report for the nodes, against the newest version (Newest), from the builds and
// the pending changes.
func For(ns []Node, builds []Build, latest string, pending []changes.Change) Report {
	newest := Newest(latest, builds)
	r := Report{Scheme: Scheme, Nodes: []NodeUpdate{}, Builds: builds, Available: []string{}}
	if r.Builds == nil {
		r.Builds = []Build{}
	}
	if newest.ok {
		r.Newest = newest.Text
	}
	for _, n := range ns {
		u := node(n, newest)
		for _, c := range pending {
			if c.Kind == changes.Firmware && slices.Contains(c.Nodes, n.Host) {
				u.Pending, u.PendingSummary = c.ID, c.Summary
				break
			}
		}
		if u.Offered() {
			r.Available = append(r.Available, u.Host)
		}
		r.Nodes = append(r.Nodes, u)
	}
	return r
}

func node(n Node, newest Version) NodeUpdate {
	u := NodeUpdate{Host: n.Host, Listed: n.Listed, OutOfService: n.OutOfService, Online: n.Online}
	st := n.Status
	if st == nil {
		u.State, u.Why = Unknown, "Not seen yet: the firmware it runs is unknown"
		return u
	}
	u.Image, u.Board = st.Image, st.Board
	if st.Config != nil {
		u.Name = Clean(st.Config.Name, maxName)
	}
	runs := Of(st.Version, st.Built, st.ElfSHA256)
	u.Runs = &runs
	if !newest.ok {
		u.State, u.Why = Unknown, "No release known yet: the controller asks for the newest when it starts, daily, and when a new node shows up"
		return u
	}
	u.Newest = &newest
	if !runs.ok {
		u.State, u.Why = Unknown, fmt.Sprintf("It runs %q, not a version: which is newer can't be told", runs.Text)
		return u
	}
	switch c := runs.sem.Compare(newest.sem); {
	case c < 0:
		u.State, u.Why = Available, "Update available: "+runs.Text+" → "+newest.Text
	case c == 0:
		u.State, u.Why = Current, "Up to date: it runs "+runs.Text
	default:
		u.State, u.Why = Newer, fmt.Sprintf("It runs %s, newer than the newest release (%s)", runs.Text, newest.Text)
	}
	return u
}

// Offer is one firmware change to make: a chip image and the nodes it goes to.
type Offer struct {
	Source  string   `json:"firmware"`
	Nodes   []string `json:"nodes"`
	To      Version  `json:"to"`
	Summary string   `json:"summary"`
}

// Plan is the firmware changes that update the hosts (none: every one offered), one per
// chip image (images/<image>), sorted by it.
func (r Report) Plan(hosts []string) ([]Offer, error) {
	if len(hosts) == 0 {
		hosts = r.Available
		if len(hosts) == 0 {
			return nil, errors.New("no node has an update available")
		}
	}
	var offers []Offer
	names := map[string][]string{}
	from := map[string][]string{}
	for i, h := range hosts {
		if h == "" || slices.Contains(hosts[:i], h) {
			return nil, fmt.Errorf("nodes: %q empty or twice", h)
		}
		j := slices.IndexFunc(r.Nodes, func(u NodeUpdate) bool { return u.Host == h })
		if j < 0 || !r.Nodes[j].Listed {
			return nil, fmt.Errorf("%s is not in settings.json: an update goes only to the nodes listed there", h)
		}
		u := r.Nodes[j]
		switch {
		case u.OutOfService:
			return nil, fmt.Errorf("%s is out of service: returning it to service updates its firmware", h)
		case u.Pending != "":
			return nil, fmt.Errorf("%s already has a firmware update pending (%s): discard that one first", h, u.PendingSummary)
		case u.State != Available:
			return nil, fmt.Errorf("%s: no update to offer: %s", h, u.Why)
		case u.Image == "":
			return nil, fmt.Errorf("%s: its /status names no chip image: the firmware to send can't be told", h)
		}
		src := "images/" + u.Image
		k := slices.IndexFunc(offers, func(o Offer) bool { return o.Source == src })
		if k < 0 {
			offers = append(offers, Offer{Source: src, To: *u.Newest})
			k = len(offers) - 1
		}
		offers[k].Nodes = append(offers[k].Nodes, h)
		name := u.Name
		if name == "" {
			name = h
		}
		names[src] = append(names[src], name)
		if !slices.Contains(from[src], u.Runs.Text) {
			from[src] = append(from[src], u.Runs.Text)
		}
	}
	slices.SortStableFunc(offers, func(a, b Offer) int { return strings.Compare(a.Source, b.Source) })
	for i := range offers {
		o := &offers[i]
		o.Summary = summary(names[o.Source], from[o.Source], o.To.Text)
	}
	return offers, nil
}

// summary says an update in words, as the header lists it: "Update dns2 from 0.0.1 to
// 0.0.4", the nodes counted when they don't fit.
func summary(names, from []string, to string) string {
	f := ""
	if len(from) == 1 {
		f = " from " + from[0]
	}
	s := "Update " + strings.Join(names, ", ") + f + " to " + to
	if len(s) > changes.MaxSummary {
		s = fmt.Sprintf("Update %d nodes%s to %s", len(names), f, to)
	}
	return s
}
