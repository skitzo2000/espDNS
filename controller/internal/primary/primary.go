// Package primary is the zone primary the nodes copy their secondary zones from
// (docs/zones.md). The nodes are plain RFC secondaries: each copies its zones from the
// primary by zone transfer (AXFR/IXFR), polls the zone's SOA for changes, and takes the
// primary's NOTIFYs if it sends them. So any standards-compliant primary works, run the
// standard way: it must allow zone transfers (and, if wanted, send NOTIFY) to the nodes'
// addresses, which you set up on it.
//
// What the controller keeps of it is settings.json's "primary" (Config): the primary's
// address and the zones to copy from it, which a new node's config starts with. Nothing is
// ever sent to the primary but DNS queries (a zone lookup's SOA, a zone's records read by
// AXFR when asked).
package primary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	neturl "net/url"
	"regexp"
	"slices"
	"strings"
)

// Config is settings.json's "primary": the zone primary's address (an IPv4 address, as a
// node config's secondary.primary names it) and the zones to copy from it (lower case, no final dot): a node added starts with them; the node configs say
// what each node copies.
type Config struct {
	Address string   `json:"address,omitempty"`
	Zones   []string `json:"zones,omitempty"`

	// ignored are the fields of an older settings.json read and no longer used (an API
	// driver's "kind", "url" and "cert_sha256", the networks it allowed, "allows"): never
	// written back.
	ignored []string
}

// old is the "primary" an older settings.json has: an API driver's kind, its address and
// its certificate's pin, and the networks the primary allowed, read only to be ignored.
type old struct {
	Address    string          `json:"address"`
	Zones      []string        `json:"zones"`
	Allows     json.RawMessage `json:"allows"`
	Kind       *string         `json:"kind"`
	URL        *string         `json:"url"`
	CertSHA256 *string         `json:"cert_sha256"`
}

// UnmarshalJSON reads "primary", unknown fields refused but for an older file's ("kind",
// "url", "cert_sha256", "allows"), which are accepted and ignored (Ignored). An older file's API
// address on an IP address gives Address, when none is set.
func (c *Config) UnmarshalJSON(b []byte) error {
	var o old
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&o); err != nil {
		return err
	}
	*c = Config{Address: o.Address, Zones: o.Zones}
	if o.Kind != nil {
		c.ignored = append(c.ignored, "kind")
	}
	if o.URL != nil {
		c.ignored = append(c.ignored, "url")
		if u, err := neturl.Parse(*o.URL); c.Address == "" && err == nil {
			if a, err := netip.ParseAddr(u.Hostname()); err == nil && a.Is4() {
				c.Address = a.String()
			}
		}
	}
	if o.CertSHA256 != nil {
		c.ignored = append(c.ignored, "cert_sha256")
	}
	if o.Allows != nil {
		c.ignored = append(c.ignored, "allows")
	}
	return nil
}

// Ignored are the older fields read and ignored ("kind", "url", "cert_sha256", "allows"), if
// any.
func (c Config) Ignored() []string { return slices.Clone(c.ignored) }

// IsZero: no address or zones set.
func (c Config) IsZero() bool { return c.Address == "" && len(c.Zones) == 0 }

// label is one label of a zone name.
var label = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// Check refuses an address that isn't an IPv4 address, a zone
// that isn't a zone name in lower case without its final dot, and anything given twice.
func (c Config) Check() error {
	if c.Address != "" {
		if a, err := netip.ParseAddr(c.Address); err != nil || !a.Is4() || a.String() != c.Address {
			return fmt.Errorf(`"address": %q is not an IPv4 address (the nodes copy their zones from one, as 192.0.2.254)`, c.Address)
		}
	}
	for i, z := range c.Zones {
		ok := z != "" && len(z) <= 253
		for _, l := range strings.Split(z, ".") {
			ok = ok && label.MatchString(l)
		}
		switch {
		case !ok:
			return fmt.Errorf(`"zones": %q is not a zone name (lower case, as home.example, without the final dot)`, z)
		case slices.Contains(c.Zones[:i], z):
			return fmt.Errorf(`"zones": %s twice`, z)
		}
	}
	return nil
}
