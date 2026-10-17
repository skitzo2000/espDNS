// Package service is a node taken out of service, returned to it, or removed from the group
// (settings.json "out_of_service" and "nodes"; docs/operations.md, Taking a node out of
// service): the rules each one follows, and what a node returned to service is sent first.
// The controller's jobs ("node-out", "node-in", "node-remove"; "node-restore" sends a node
// installed again over USB what node-in sends) and the CLI (espdns node)
// both take them from here.
//
// A node out of service stays listed, polled and shown with its last status, but nothing is
// sent to it (an apply, a rollout, a refresh and a config push skip it, and say so) and it
// isn't counted for the last-healthy-node rule: one that is off, or broken, doesn't hold up
// the others. Returning it needs it to answer and to trust the controller's release key;
// then it is brought up to date, alone, with what it missed (CatchUp), each rollout a dry run
// first, before it counts again. Removing one takes it out of settings.json only: its config
// file is kept, and the node itself isn't touched.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/settings"
	"github.com/skitzo2000/espdns/controller/internal/updates"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
)

// CheckOut refuses to take addr out of service: it isn't one of the nodes, it is out
// already, or a key rotation under way includes it (it would be left on the old keys).
func CheckOut(dataDir string, s settings.Settings, addr string) error {
	switch {
	case !slices.Contains(s.Nodes, addr):
		return fmt.Errorf("%s is not in settings.json", addr)
	case s.IsOutOfService(addr):
		return fmt.Errorf("%s is out of service already", addr)
	}
	if p := rotation.Includes(dataDir, addr); p != nil {
		return fmt.Errorf("%s is in the key rotation under way (%s): finish or discard it first", addr, p.ID)
	}
	return nil
}

// CheckIn refuses to start returning addr to service before the node is read: it isn't one
// of the nodes, or it is in service.
func CheckIn(s settings.Settings, addr string) error {
	switch {
	case !slices.Contains(s.Nodes, addr):
		return fmt.Errorf("%s is not in settings.json", addr)
	case !s.IsOutOfService(addr):
		return fmt.Errorf("%s is in service already", addr)
	}
	return nil
}

// CheckRemove refuses to remove addr from the group: it isn't one of the nodes, a key
// rotation under way includes it, or a pending firmware change names it (the apply would
// refuse a node no longer listed: discard that change first).
func CheckRemove(dataDir string, s settings.Settings, addr string, pending []changes.Change) error {
	if !slices.Contains(s.Nodes, addr) {
		return fmt.Errorf("%s is not in settings.json", addr)
	}
	if p := rotation.Includes(dataDir, addr); p != nil {
		return fmt.Errorf("%s is in the key rotation under way (%s): finish or discard it first", addr, p.ID)
	}
	for _, c := range pending {
		if c.Kind == changes.Firmware && slices.Contains(c.Nodes, addr) {
			return fmt.Errorf("the pending change %q names %s: discard it first", c.Summary, addr)
		}
	}
	return nil
}

// ErrNotAnswering: a node to return to service doesn't answer; it stays out of service.
var ErrNotAnswering = errors.New("it doesn't answer")

// Ready refuses to return a node to service unless it answered (st, read now, with err) and
// trusts the release key the controller signs with (fingerprint fp) in its release slot:
// a node that doesn't would refuse everything it is sent.
func Ready(addr string, st release.NodeStatus, err error, fp string) error {
	if err != nil {
		return fmt.Errorf("%s: %w (%v): it stays out of service; check its power and network, then try again", addr, ErrNotAnswering, err)
	}
	if int(release.KeyRelease) >= len(st.Keys) || st.Keys[release.KeyRelease] != fp {
		have := "none"
		if int(release.KeyRelease) < len(st.Keys) && st.Keys[release.KeyRelease] != "" {
			have = st.Keys[release.KeyRelease]
		}
		return fmt.Errorf("%s trusts the release key %s, not the controller's (%s): it stays out of service; "+
			"write its keys over USB, or remove it from the group and add it again", addr, have, fp)
	}
	return nil
}

// Step is one rollout a node returned to service is sent: what it is, in words, and the
// rollout as the Push page asks for it (rolling.Web), for that node alone.
type Step struct {
	What string
	Web  rolling.Web
}

// CatchUp is what the node at host (st, read now) is sent before it counts again, in the
// order an apply sends them: its config (the file it runs, by its name or address, or the one
// last pushed to it), the hosted zones, the allowed and blocked sites, its blocklist (the one
// it runs, or one an apply that stopped compiled it from: sums; else the fleet's list), and
// the newest firmware for it if it runs an older one. Each is the whole of what the
// controller has now: a node that has it already is skipped by its rollout. Notes say what
// isn't sent and why.
func CatchUp(dataDir, catalog, host string, st release.NodeStatus, sums func(list string) []string) ([]Step, []string, error) {
	var out []Step
	var notes []string
	one := func(what string, w rolling.Web) {
		w.Nodes, w.Returning = []string{host}, host
		out = append(out, Step{What: what, Web: w})
	}

	// Its config.
	files, err := configs.List(dataDir)
	if err != nil {
		return nil, nil, err
	}
	cfg, text := "", []byte(nil)
	for _, f := range files {
		if configs.Matches(f.Config, host, st) || pushedHere(dataDir, st, f.Name) {
			cfg, text = f.Name, f.Text
			break
		}
	}
	switch sp, err := configs.ParseSpec(host, cfg, text); {
	case cfg == "":
		notes = append(notes, "its config: no config file here is its (by its name or address): none sent")
	case err == nil && moves(sp):
		notes = append(notes, fmt.Sprintf("its config: %s moves it to another address, which is done from its node page: not sent", cfg))
	default:
		one("its config "+cfg, rolling.Web{Kind: "config", Configs: map[string]string{host: cfg}})
	}

	// The hosted zones, as one set.
	zfs, err := zonefiles.List(dataDir)
	if err != nil {
		return nil, nil, err
	}
	var zones []string
	for _, f := range zfs {
		zones = append(zones, f.Name)
	}
	switch {
	case len(zones) > 0:
		one("hosted zones", rolling.Web{Kind: "zones", Zones: zones})
	case st.Hosted != nil && st.Hosted.SHA256 != "":
		one("hosted zones (none: the ones it serves are deleted)", rolling.Web{Kind: "zones", EmptyZones: true})
	}

	// The allowed and blocked sites.
	if exists(blocking.Out(dataDir, blocking.OverridesName)) {
		one("allowed and blocked sites", rolling.Web{Kind: "overrides", File: blocking.OverridesName + ".bin"})
	}

	// Its blocklist: the list it runs (now, or as an apply that stopped found it), else the
	// fleet's.
	defs, _, err := blocking.Load(dataDir)
	if err != nil && len(defs.Lists) > 0 {
		return nil, nil, err
	}
	runs := ""
	if st.Blocking != nil {
		runs = st.Blocking.List.SHA256
	}
	list := ""
	for _, l := range defs.Lists {
		have := sums(l.Name)
		if sum := fileSHA256(blocking.Out(dataDir, l.Name)); sum != "" {
			have = append(have, sum)
		}
		if runs != "" && slices.Contains(have, runs) {
			list = l.Name
			break
		}
	}
	if list == "" && len(defs.Lists) > 0 {
		l, _ := defs.Fleet()
		list = l.Name
	}
	switch {
	case list == "":
	case !exists(blocking.Out(dataDir, list)):
		notes = append(notes, fmt.Sprintf("its blocklist: %s isn't compiled yet: none sent", list))
	default:
		one("blocklist "+list, rolling.Web{Kind: "blocklist", File: list + ".bin"})
	}

	// Its firmware, if it runs an older version than the newest and its chip image of that
	// version is held.
	builds := updates.Builds(dataDir, catalog)
	u := updates.For([]updates.Node{{Host: host, Listed: true, Online: true, Status: &st}}, builds, updates.Latest(dataDir), nil).Nodes[0]
	src := "images/" + st.Image
	held := slices.ContainsFunc(builds, func(b updates.Build) bool {
		return b.Source == src && b.Error == "" && u.Newest != nil && b.Text == u.Newest.Text
	})
	switch {
	case u.State == updates.Available && held:
		one("firmware "+src, rolling.Web{Kind: "firmware", Firmware: []string{src}})
	case u.State == updates.Available:
		notes = append(notes, "its firmware: "+u.Why+": its chip image of that version isn't here yet: update it from Nodes")
	case u.State != updates.Current:
		notes = append(notes, "its firmware: "+u.Why)
	}
	return out, notes, nil
}

// moves says whether the config moves its node to another address (the config rollout
// refuses it).
func moves(sp configs.Spec) bool {
	_, ok := sp.Moves()
	return ok
}

// pushedHere says whether name is the config last pushed to the node, which it runs now.
func pushedHere(dataDir string, st release.NodeStatus, name string) bool {
	if st.Config == nil || st.NodeID == "" {
		return false
	}
	p, err := configs.LoadPushed(dataDir, st.NodeID)
	return err == nil && p != nil && p.Seq == st.Config.Seq && p.File == name
}

func fileSHA256(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func exists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular()
}
