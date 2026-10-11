// Package updates is update availability for the Nodes page: for each node, whether the
// data directory has a newer firmware build for its chip image and board than the one it
// runs (its /status), said as versions (version.go). Offering an update makes a pending
// firmware change (internal/changes) per build, which the apply job sends; nothing here
// pushes to a node.
//
// A node's builds are the ones a rollout would give it: its board's build
// (firmware/builds/<board>, of the node's chip image, or of the sibling image its board runs
// now: boards.Siblings) when there is one, else its chip image's export
// (firmware/images/<image>); a node that reports no board takes any build of its image.
// A board's build is made for the board (its pins, its partitions), so a chip image imported
// later, even a later build, never takes its place.
//
// A later build of the version a node runs is offered as what it is ("A newer build of
// 0.0.1"), never as a version change: it is a Rebuild, which the Nodes page shows in the
// node's row only, without the update banner, and which "Update all" leaves out.
package updates

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/boards"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
)

// A node's state, against the newest build for it.
const (
	Available = "available" // a newer build is there: an update is offered
	Current   = "current"   // it runs the newest build
	Newer     = "newer"     // it runs a newer build than any there
	Unordered = "unordered" // it runs another build, and the two can't be ordered
	NoBuild   = "no_build"  // no build for its image (and board)
	Unknown   = "unknown"   // not seen yet, or its /status says no image
)

// maxName is the most of a node's config name shown (a config name is a file name).
const maxName = 64

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
	// Runs is the firmware it runs (nil: unknown); Newest the newest build for it (nil: none).
	Runs   *Version `json:"runs,omitempty"`
	Newest *Build   `json:"newest,omitempty"`
	State  string   `json:"state"`
	// Rebuild says the update offered is a later build of the version it runs.
	Rebuild bool   `json:"rebuild,omitempty"`
	Why     string `json:"why"` // the state in words
	// Pending is the pending firmware change that names it, if any (its ID and summary).
	Pending        string `json:"pending,omitempty"`
	PendingSummary string `json:"pending_summary,omitempty"`
}

// Offered says whether an update is offered for it now: available, listed and in service,
// none pending.
func (n NodeUpdate) Offered() bool {
	return n.State == Available && n.Listed && !n.OutOfService && n.Pending == ""
}

// Announced says whether the update is one "Update all" and the banner offer: offered,
// and a new version, not a later build of the one it runs.
func (n NodeUpdate) Announced() bool { return n.Offered() && !n.Rebuild }

// Report is the fleet's update availability.
type Report struct {
	Scheme string       `json:"scheme"` // how versions are ordered (Scheme)
	Nodes  []NodeUpdate `json:"nodes"`
	Builds []Build      `json:"builds"`
	// Available are the hosts a new version is offered for ("Update all"); a later build
	// of the version a node runs is offered for that node alone.
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

// For is the report for the nodes, from the builds and the pending changes.
func For(ns []Node, builds []Build, pending []changes.Change) Report {
	r := Report{Scheme: Scheme, Nodes: []NodeUpdate{}, Builds: builds, Available: []string{}}
	if r.Builds == nil {
		r.Builds = []Build{}
	}
	for _, n := range ns {
		u := node(n, builds)
		for _, c := range pending {
			if c.Kind == changes.Firmware && slices.Contains(c.Nodes, n.Host) {
				u.Pending, u.PendingSummary = c.ID, c.Summary
				break
			}
		}
		if u.Announced() {
			r.Available = append(r.Available, u.Host)
		}
		r.Nodes = append(r.Nodes, u)
	}
	return r
}

// fits says whether a build is one a rollout gives the node: its chip image's, or its
// board's build of the sibling image the board runs now (boards.Siblings).
func fits(b Build, st *release.NodeStatus) bool {
	if b.Error != "" {
		return false
	}
	if b.Image != st.Image {
		return b.Board != "" && b.Board == st.Board && boards.Swappable(st.Image, b.Image)
	}
	return b.Board == "" || st.Board == "" || b.Board == st.Board
}

func node(n Node, builds []Build) NodeUpdate {
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
	if st.Image == "" {
		u.State, u.Why = Unknown, "Its /status names no chip image: no build can be matched to it"
		return u
	}
	// Its board's build, when there is one, else any that fits (its chip image's).
	ofBoard := st.Board != "" && slices.ContainsFunc(builds, func(b Build) bool { return fits(b, st) && b.Board == st.Board })
	var newest *Build
	for i := range builds {
		if !fits(builds[i], st) || ofBoard && builds[i].Board != st.Board {
			continue
		}
		if newest == nil || Later(builds[i].Version, newest.Version) {
			newest = &builds[i]
		}
	}
	if newest == nil {
		u.State = NoBuild
		u.Why = fmt.Sprintf("No build for its chip image %s in the data directory", st.Image)
		if st.Board != "" {
			u.Why = fmt.Sprintf("No build for its board %s (chip image %s) in the data directory", st.Board, st.Image)
		}
		return u
	}
	b := *newest
	u.Newest = &b
	c, ok := Compare(b.Version, runs)
	switch {
	case !ok:
		u.State = Unordered
		u.Why = fmt.Sprintf("It runs %s, another build than the newest here (%s); which is newer can't be told", runs.Text, b.Text)
	case c == 0:
		u.State, u.Why = Current, "Up to date: it runs "+runs.Text
	case c > 0 && runs.Text == b.Text:
		u.State, u.Rebuild, u.Why = Available, true, "A newer build of "+b.Text+builtWhen(b.Version)
	case c > 0:
		u.State, u.Why = Available, "Update available: "+runs.Text+" → "+b.Text
	default:
		u.State = Newer
		u.Why = fmt.Sprintf("It runs %s, newer than any build here (the newest is %s)", runs.Text, b.Text)
		if runs.Text == b.Text {
			u.Why = fmt.Sprintf("It runs %s, built later than any build of it here", runs.Text)
		}
	}
	return u
}

// Offer is one firmware change to make: a build and the nodes it goes to.
type Offer struct {
	Source  string   `json:"firmware"`
	Nodes   []string `json:"nodes"`
	To      Version  `json:"to"`
	Summary string   `json:"summary"`
}

// ErrMoved is a build asked for that is no longer the newest for its node (the API's 409).
var ErrMoved = errors.New("the newest build for it changed: look again")

// Plan is the firmware changes that update the hosts (none: every one offered), one per
// build, in the order of the builds. expect, when it names a host, is the build (Build's
// hash) the page showed for it: a newer one since is refused (ErrMoved).
func (r Report) Plan(hosts []string, expect map[string]string) ([]Offer, error) {
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
		if j < 0 {
			return nil, fmt.Errorf("%s is not in settings.json: an update goes only to the nodes listed there", h)
		}
		u := r.Nodes[j]
		switch {
		case !u.Listed:
			return nil, fmt.Errorf("%s is not in settings.json: an update goes only to the nodes listed there", h)
		case u.OutOfService:
			return nil, fmt.Errorf("%s is out of service: returning it to service updates its firmware", h)
		case u.Pending != "":
			return nil, fmt.Errorf("%s already has a firmware update pending (%s): discard that one first", h, u.PendingSummary)
		case u.State != Available:
			return nil, fmt.Errorf("%s: no update to offer: %s", h, u.Why)
		}
		if want, ok := expect[h]; ok && want != u.Newest.Build {
			return nil, fmt.Errorf("%s: %w (now %s)", h, ErrMoved, u.Newest.Text)
		}
		k := slices.IndexFunc(offers, func(o Offer) bool { return o.Source == u.Newest.Source })
		if k < 0 {
			offers = append(offers, Offer{Source: u.Newest.Source, To: u.Newest.Version})
			k = len(offers) - 1
		}
		offers[k].Nodes = append(offers[k].Nodes, h)
		name := u.Name
		if name == "" {
			name = h
		}
		names[u.Newest.Source] = append(names[u.Newest.Source], name)
		if !slices.Contains(from[u.Newest.Source], u.Runs.Text) {
			from[u.Newest.Source] = append(from[u.Newest.Source], u.Runs.Text)
		}
	}
	slices.SortStableFunc(offers, func(a, b Offer) int {
		return slices.IndexFunc(r.Builds, func(x Build) bool { return x.Source == a.Source }) -
			slices.IndexFunc(r.Builds, func(x Build) bool { return x.Source == b.Source })
	})
	for i := range offers {
		o := &offers[i]
		o.Summary = summary(names[o.Source], from[o.Source], o.To.Text)
	}
	return offers, nil
}

// summary says an update in words, as the header lists it: "Update dns2 from 0.0.1 to
// 0.0.4", the nodes counted when they don't fit; a later build of the version they run,
// "Update dns2 to a newer build of 0.0.1".
func summary(names, from []string, to string) string {
	if len(from) == 1 && from[0] == to {
		s := "Update " + strings.Join(names, ", ") + " to a newer build of " + to
		if len(s) > changes.MaxSummary {
			s = fmt.Sprintf("Update %d nodes to a newer build of %s", len(names), to)
		}
		return s
	}
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

// builtWhen is " (built Oct 7 2026 09:30)", when the build says when it was built.
func builtWhen(v Version) string {
	switch {
	case !v.at.IsZero():
		return " (built " + v.at.Format("Jan 2 2006 15:04") + ")"
	case v.Built != "":
		return " (built " + Clean(v.Built, maxText) + ")"
	}
	return ""
}
