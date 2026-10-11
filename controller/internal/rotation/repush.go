package rotation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
	"github.com/skitzo2000/espdns/controller/internal/zones"
)

// Item is a release a node stores and checks again at every boot (its config, hosted zones,
// overrides, blocklist), as the controller has its bytes: pushed again, signed with the new
// release key, so the node's stored copy is current. Record, if set, notes the push where
// the controller keeps what each node runs (configs.Pushed, zonefiles.Pushed).
type Item struct {
	Kind    release.Kind
	What    string
	Payload []byte
	Record  func(host, nodeID string, seq uint64) error
}

// Collect is what the node whose /status is st stores, as the controller has it: the items
// to push again, and in words what it stores that the controller can't sign again (it has
// no copy of those bytes). The order is an apply's: config, hosted zones, overrides,
// blocklist.
func Collect(dataDir string, st release.NodeStatus) (items []Item, missing []string) {
	// The config: the one last pushed to it, while its seq is the node's.
	if c := st.Config; c != nil && c.Source == "node" && c.Seq > 0 {
		p, err := configs.LoadPushed(dataDir, st.NodeID)
		switch {
		case err != nil:
			missing = append(missing, fmt.Sprintf("its config (seq %d): %v", c.Seq, err))
		case p == nil || p.Seq != c.Seq:
			missing = append(missing, fmt.Sprintf("its config (seq %d): the controller has no record of it (pushed some other way)", c.Seq))
		default:
			file := p.File
			items = append(items, Item{Kind: release.Config, What: "config " + file, Payload: p.Payload,
				Record: func(host, id string, seq uint64) error {
					return configs.RecordPushed(dataDir, configs.Pushed{NodeID: id, Host: host, File: file, Seq: seq, Payload: p.Payload, By: "controller"})
				}})
		}
	}
	// The hosted zones: the bundle of the files here for the zones it serves, if that is the
	// one it runs (by the hash it reports).
	if h := st.Hosted; h != nil && h.Seq > 0 && h.State != "off" {
		if it, err := hostedItem(dataDir, st); err != nil {
			missing = append(missing, fmt.Sprintf("its hosted zones (seq %d): %v", h.Seq, err))
		} else {
			items = append(items, it)
		}
	}
	// The lists: a compiled file here with the hash it reports.
	for _, k := range []release.Kind{release.Overrides, release.Blocklist} {
		l := st.List(k)
		if l == nil || l.Seq == 0 || l.State == "off" {
			continue
		}
		what := "allowed and blocked sites (overrides)"
		if k == release.Blocklist {
			what = "blocklist"
		}
		path := listFile(dataDir, l.SHA256)
		if path == "" {
			why := "no compiled list here has the hash it reports"
			if l.SHA256 == "" {
				why = "it reports no hash (firmware from before)"
			}
			missing = append(missing, fmt.Sprintf("its %s (seq %d): %s", what, l.Seq, why))
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			missing = append(missing, fmt.Sprintf("its %s (seq %d): %v", what, l.Seq, err))
			continue
		}
		items = append(items, Item{Kind: k, What: what + " " + filepath.Base(path), Payload: b})
	}
	return items, missing
}

// hostedItem is the node's hosted zones bundle, rebuilt from the zone files here.
func hostedItem(dataDir string, st release.NodeStatus) (Item, error) {
	h := st.Hosted
	if h.SHA256 == "" {
		return Item{}, fmt.Errorf("it reports no hash of the bundle it runs (state %s)", h.State)
	}
	files, err := zonefiles.List(dataDir)
	if err != nil {
		return Item{}, err
	}
	set := &zones.Set{}
	hashes := map[string]string{}
	for _, hz := range h.Zones {
		name := strings.ToLower(strings.TrimSuffix(hz.Name, ".")) + ".zone"
		i := slices.IndexFunc(files, func(f zonefiles.File) bool { return f.Name == name })
		if i < 0 || files[i].Parsed == nil {
			return Item{}, fmt.Errorf("no zone file here for %s", hz.Name)
		}
		set.Zones = append(set.Zones, files[i].Parsed)
		hashes[name] = files[i].Hash
	}
	b, err := set.Bundle(0)
	if err != nil {
		return Item{}, err
	}
	sum := sha256.Sum256(b)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), h.SHA256) {
		return Item{}, fmt.Errorf("the zone files here are not the bundle it runs (by its hash): one changed since")
	}
	what := "hosted zones (none)"
	if len(set.Zones) > 0 {
		var names []string
		for _, z := range set.Zones {
			names = append(names, z.Name())
		}
		what = "hosted zones " + strings.Join(names, ", ")
	}
	return Item{Kind: release.Zones, What: what, Payload: b, Record: func(host, id string, seq uint64) error {
		return zonefiles.RecordPushed(dataDir, zonefiles.Pushed{NodeID: id, Host: host, Seq: seq, Files: hashes, By: "controller"})
	}}, nil
}

// listFile is the compiled list in <data>/lists whose SHA-256 is sum, "" if none.
func listFile(dataDir, sum string) string {
	if sum == "" {
		return ""
	}
	dir := filepath.Join(dataDir, blocking.ListsDir)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".bin") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		s := sha256.Sum256(b)
		if strings.EqualFold(hex.EncodeToString(s[:]), sum) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// Words are the items in words, for the plan.
func Words(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.What)
	}
	return out
}
