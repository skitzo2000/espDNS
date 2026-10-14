// Package settings is the deployment's settings file, <data>/settings.json: the one file
// the controller and the espdns CLI both read (docs/reference/settings.md). The controller
// polls its nodes and the CLI counts them as peers; both count its DNS peers for the
// last-healthy-node rule. In Docker the data directory is /data, mounted from the host
// (controller/data), so the CLI's fleet targets and the controller read the same file.
package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// File is the settings file's name in the data directory.
const File = "settings.json"

// Settings is settings.json. Every field is optional; unknown fields are refused, so a
// misspelt one (a DNS peer that would silently not count) is an error, not a default.
type Settings struct {
	// Nodes are node addresses (host, or host:port for an HTTP port other than 80): the
	// controller polls them even if mDNS doesn't find them, and with -mdns 0 (every fleet
	// target that changes a node) they are the only nodes the CLI counts as peers. A host
	// given by name must be one the node answers to (the name in its node config, or its
	// .local name): a node refuses a request whose Host names anything else (421, against
	// DNS rebinding; firmware/main/httpguard.h). An address always works.
	Nodes []string `json:"nodes,omitempty"`
	// OutOfService are nodes taken out of service (each one of Nodes): still listed, polled
	// and shown with their last status, but sent nothing (no apply, rollout, refresh or
	// config push changes them) and not counted for the last-healthy-node rule, so one that
	// is off or broken doesn't hold up the others. Returning one to service brings it up to
	// date first (docs/operations.md, Taking a node out of service). Empty: every node is in
	// service.
	OutOfService []string `json:"out_of_service,omitempty"`
	// DNSPeers are resolvers clients use besides the nodes (the zone primary, 192.0.2.254, or
	// addr:port for a DNS port other than 53): each counts as one answering peer while it
	// resolves a forwarded name and answers its zones' SOA authoritatively (the CLI's
	// -dns-peer).
	DNSPeers []string `json:"dns_peers,omitempty"`
	// DNSPeerZones are the zones the DNS peers must answer for (-dns-peer-zone); empty: the
	// secondary zones of the node about to be changed.
	DNSPeerZones []string `json:"dns_peer_zones,omitempty"`
	// Canary is the node the controller's Push page changes first unless you choose
	// another (the Makefile's CANARY); one of Nodes. Empty: the first node changed.
	Canary string `json:"canary,omitempty"`
	// NoDHCP are the networks with no DHCP server (IPv4, each as its network, a.b.c.0/nn): a
	// node there is never given a config on DHCP, by adoption from the CLI or the
	// controller's Adopt page. Empty (the default): none, and DHCP is offered wherever the
	// node is, with its reservation.
	NoDHCP []string `json:"no_dhcp,omitempty"`
	// Primary is the zone primary the nodes copy their secondary zones from
	// (internal/primary): {"address": "192.0.2.254", "allows": ["192.0.2.0/24"], "zones":
	// ["home.example"]}, its address, the networks it allows zone transfers from, as you
	// set it up, and the zones to copy from it (a new node's config starts with them).
	// Adoption says once when a node's address is in none of the networks. An older file's API fields
	// ("kind", "url", "cert_sha256") are read and ignored, and dropped when it is written.
	// Absent: none set up.
	Primary *primary.Config `json:"primary,omitempty"`
	// InternalSources is the internal allowlist: the hosts (a name or an IP address, no
	// scheme or port) a blocklist URL source may be fetched from although it is inside
	// (a private, loopback or link-local address) or over plain http; such a fetch goes to
	// that host only (internal/blocklist, fetch.go). Empty (the default): sources are https
	// from public addresses only.
	InternalSources []string `json:"internal_sources,omitempty"`
	// BlocklistRefresh is how often the controller checks the blocklists it downloads
	// (a list's URL sources) for a new version, and sends a changed one to the nodes:
	// "off", or a time from 1h to 720h (30 days) as Go writes one ("6h", "24h", "168h").
	// Empty (the default): DefaultRefresh, every day.
	BlocklistRefresh string `json:"blocklist_refresh,omitempty"`
	// ReleaseURL is the repository the project's releases are fetched from (espdns release
	// fetch, the pages' Get firmware): https://<host>/<owner>/<repo>, a GitHub
	// repository (a fork's, say); "" for the project's own, https://github.com/skitzo2000/espdns.
	ReleaseURL string `json:"release_url,omitempty"`
}

// DefaultRefresh is how often downloaded blocklists are checked when blocklist_refresh
// isn't set; MinRefresh and MaxRefresh bound a time set.
const (
	DefaultRefresh = 24 * time.Hour
	MinRefresh     = time.Hour
	MaxRefresh     = 30 * 24 * time.Hour
)

// Refresh is how often downloaded blocklists are checked (blocklist_refresh); 0: off.
func (s Settings) Refresh() time.Duration {
	d, _ := ParseRefresh(s.BlocklistRefresh)
	return d
}

// ParseRefresh reads a blocklist_refresh value: "" the default, "off" 0, else a time
// from MinRefresh to MaxRefresh.
func ParseRefresh(v string) (time.Duration, error) {
	switch v {
	case "":
		return DefaultRefresh, nil
	case "off":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < MinRefresh || d > MaxRefresh {
		return DefaultRefresh, fmt.Errorf("%q: \"off\", or a time from %v to %v (\"6h\", \"24h\", \"168h\")", v, MinRefresh, MaxRefresh)
	}
	return d, nil
}

// Path is the settings file in dataDir.
func Path(dataDir string) string { return filepath.Join(dataDir, File) }

// Load reads the settings file at path; none (the zero Settings) if there is no file.
func Load(path string) (Settings, error) {
	var s Settings
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if s, err = Parse(b); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Parse reads and checks settings.json's contents.
func Parse(b []byte) (Settings, error) {
	var s Settings
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return Settings{}, err
	}
	if d.More() {
		return Settings{}, errors.New("more than one JSON value")
	}
	if err := s.Check(); err != nil {
		return s, err
	}
	return s, nil
}

// FieldError is a setting refused: Field is its name in settings.json ("nodes", "primary").
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }

func refuse(field, format string, a ...any) error {
	return &FieldError{Field: field, Err: fmt.Errorf(format, a...)}
}

// Check refuses an empty or repeated entry, and an address that isn't one. The error is a
// *FieldError.
func (s Settings) Check() error {
	for _, l := range []struct {
		name string
		v    []string
		addr bool
	}{{"nodes", s.Nodes, true}, {"out_of_service", s.OutOfService, true}, {"dns_peers", s.DNSPeers, true},
		{"dns_peer_zones", s.DNSPeerZones, false}} {
		for i, v := range l.v {
			switch {
			case strings.TrimSpace(v) != v || v == "" || strings.ContainsAny(v, " \t/"):
				return refuse(l.name, "%q is not a %s", v, map[bool]string{true: "host or host:port", false: "zone name"}[l.addr])
			case slices.Contains(l.v[:i], v):
				return refuse(l.name, "%s twice", v)
			}
			if l.addr && strings.Contains(v, ":") {
				if _, port, err := net.SplitHostPort(v); err != nil || port == "" {
					return refuse(l.name, "%q is not a host or host:port", v)
				}
			}
		}
	}
	for i, v := range s.NoDHCP {
		p, err := netip.ParsePrefix(v)
		switch {
		case err != nil || !p.Addr().Is4():
			return refuse("no_dhcp", "%q is not an IPv4 network with its prefix length (a.b.c.0/nn)", v)
		case p.Masked() != p:
			return refuse("no_dhcp", "%s is not a network (%s is)", v, p.Masked())
		case slices.Contains(s.NoDHCP[:i], v):
			return refuse("no_dhcp", "%s twice", v)
		}
	}
	for i, h := range s.InternalSources {
		if err := blocklist.CheckHost(h); err != nil {
			return refuse("internal_sources", "%v", err)
		}
		if blocklist.InternalHost(h, s.InternalSources[:i]) {
			return refuse("internal_sources", "%s twice", h)
		}
	}
	if _, err := ParseRefresh(s.BlocklistRefresh); err != nil {
		return refuse("blocklist_refresh", "%v", err)
	}
	if s.ReleaseURL != "" {
		u, err := url.Parse(s.ReleaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
			len(strings.Split(strings.Trim(u.Path, "/"), "/")) != 2 {
			return refuse("release_url", "%q is not a repository's page, https://<host>/<owner>/<repo>", s.ReleaseURL)
		}
	}
	if s.Canary != "" && !slices.Contains(s.Nodes, s.Canary) {
		return refuse("canary", "%s is not one of the nodes", s.Canary)
	}
	for _, a := range s.OutOfService {
		if !slices.Contains(s.Nodes, a) {
			return refuse("out_of_service", "%s is not one of the nodes", a)
		}
	}
	if s.Primary != nil {
		if err := s.Primary.Check(); err != nil {
			return &FieldError{Field: "primary", Err: err}
		}
	}
	return nil
}

// InService are the nodes in service: Nodes, in their order, but those out of service.
func (s Settings) InService() []string {
	var out []string
	for _, a := range s.Nodes {
		if !slices.Contains(s.OutOfService, a) {
			out = append(out, a)
		}
	}
	return out
}

// IsOutOfService says whether addr is a node taken out of service.
func (s Settings) IsOutOfService(addr string) bool { return slices.Contains(s.OutOfService, addr) }

// NoDHCPAt is the network in no_dhcp that addr is on, if any: a node there gets a static
// address, never DHCP.
func (s Settings) NoDHCPAt(addr netip.Addr) (netip.Prefix, bool) {
	for _, v := range s.NoDHCP {
		if p, err := netip.ParsePrefix(v); err == nil && p.Contains(addr.Unmap()) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// ZonePrimary is the zone primary set up ("primary"); the zero Config when there is none.
func (s Settings) ZonePrimary() primary.Config {
	if s.Primary != nil {
		return *s.Primary
	}
	return primary.Config{}
}

// mu orders this process's writes of the file: an edit from the browser (SaveIf) and an
// adoption's (AddNode, MoveNode), a node taken out of service or removed (SetOutOfService,
// RemoveNode) each read the file and write it whole, one at a time.
var mu sync.Mutex

// ErrChanged: the file is no longer the version an edit was made on.
var ErrChanged = errors.New("settings.json changed since it was read: read it again")

// Version is the file's version: the SHA-256 of its bytes, "" when there is none.
func Version(b []byte, exists bool) string {
	if !exists {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Read is the settings file at path with its version (Version) and whether it exists. A
// file that doesn't parse gives its version and the error, so it can still be replaced.
func Read(path string) (s Settings, version string, exists bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, "", false, nil
	}
	if err != nil {
		return s, "", false, err
	}
	version = Version(b, true)
	if s, err = Parse(b); err != nil {
		return Settings{}, version, true, fmt.Errorf("%s: %w", path, err)
	}
	return s, version, true, nil
}

// SaveIf writes s to path (Save) if the file is still the version an edit was made on
// (Version; "" for none), else ErrChanged. before and after, each if not nil, run with the
// version checked and no other write of the file in between: before ahead of the write,
// its error stopping it; after once the file is written, its error returned with the file
// saved.
func SaveIf(path, version string, s Settings, before, after func() error) error {
	if err := s.Check(); err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	b, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if Version(b, exists) != version {
		return ErrChanged
	}
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	if err := Save(path, s); err != nil {
		return err
	}
	if after != nil {
		return after()
	}
	return nil
}

// MoveNode changes a node's address in the settings file's nodes, in its place (and as the
// canary, if it is the canary), written whole; false if from isn't there. If to is listed
// already, from is only taken out.
func MoveNode(path, from, to string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := Load(path)
	if err != nil {
		return false, err
	}
	i := slices.Index(s.Nodes, from)
	if i < 0 {
		return false, nil
	}
	if slices.Contains(s.Nodes, to) {
		s.Nodes = slices.Delete(s.Nodes, i, i+1)
	} else {
		s.Nodes[i] = to
	}
	if s.Canary == from {
		s.Canary = to
	}
	if j := slices.Index(s.OutOfService, from); j >= 0 {
		if slices.Contains(s.OutOfService, to) {
			s.OutOfService = slices.Delete(s.OutOfService, j, j+1)
		} else {
			s.OutOfService[j] = to
		}
	}
	if err := Save(path, s); err != nil {
		return false, err
	}
	return true, nil
}

// SetOutOfService takes addr, one of the settings file's nodes, out of service (out), or
// returns it to service, and writes the file whole; false if it was so already. The file is
// read again here, as AddNode does.
func SetOutOfService(path, addr string, out bool) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := Load(path)
	if err != nil {
		return false, err
	}
	if !slices.Contains(s.Nodes, addr) {
		return false, fmt.Errorf("%s is not in settings.json", addr)
	}
	if s.IsOutOfService(addr) == out {
		return false, nil
	}
	if out {
		s.OutOfService = append(s.OutOfService, addr)
	} else {
		s.OutOfService = slices.DeleteFunc(s.OutOfService, func(a string) bool { return a == addr })
	}
	if err := Save(path, s); err != nil {
		return false, err
	}
	return true, nil
}

// RemoveNode takes addr out of the settings file's nodes (out of service too, and as the
// canary if it is: the first node changed goes first then), written whole; false if it
// isn't there.
func RemoveNode(path, addr string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := Load(path)
	if err != nil {
		return false, err
	}
	if !slices.Contains(s.Nodes, addr) {
		return false, nil
	}
	gone := func(a string) bool { return a == addr }
	s.Nodes = slices.DeleteFunc(s.Nodes, gone)
	s.OutOfService = slices.DeleteFunc(s.OutOfService, gone)
	if s.Canary == addr {
		s.Canary = ""
	}
	if err := Save(path, s); err != nil {
		return false, err
	}
	return true, nil
}

// AddNode adds addr to the settings file's nodes, as the last one, and writes it whole
// (Save); false if it is there already. The file is read again here, so an edit made since
// it was last read is kept, and refused as a whole if it no longer parses.
func AddNode(path, addr string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := Load(path)
	if err != nil {
		return false, err
	}
	if slices.Contains(s.Nodes, addr) {
		return false, nil
	}
	s.Nodes = append(s.Nodes, addr)
	if err := Save(path, s); err != nil {
		return false, err
	}
	return true, nil
}

// Save writes the settings to path, whole or not at all (a temporary file renamed over it).
func Save(path string, s Settings) error {
	if err := s.Check(); err != nil {
		return err
	}
	if s.Primary != nil && s.Primary.IsZero() {
		s.Primary = nil // an older file's {"kind": "manual"}: nothing left to write
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(secfile.FileMode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
