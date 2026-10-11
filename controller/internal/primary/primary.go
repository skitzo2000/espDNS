// Package primary is the zone primary the nodes copy their secondary zones from
// (docs/zones.md). The nodes are plain RFC secondaries: each copies its zones from the
// primary by zone transfer (AXFR/IXFR), polls the zone's SOA for changes, and takes the
// primary's NOTIFYs if it sends them. So any standards-compliant primary works, run the
// standard way: it is set up once to allow zone transfers (and, if wanted, NOTIFY) from
// the networks the nodes are on, and adding a node changes nothing there.
//
// What the controller keeps of it is settings.json's "primary" (Config): the primary's
// address, the networks it allows zone transfers from, as you set it up, and the zones to
// copy from it, which a new node's config starts with. Nothing is
// ever sent to the primary but DNS queries (a zone lookup's SOA, a zone's records read by
// AXFR when asked). Adoption only says, once, when a node's address is in none of those
// networks (Advice).
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
// node config's secondary.primary names it), the networks it allows zone transfers from
// (IPv4, each as its network, a.b.c.0/nn), as you set the primary up, and the zones to copy
// from it (lower case, no final dot): a node added starts with them; the node configs say
// what each node copies.
type Config struct {
	Address string   `json:"address,omitempty"`
	Allows  []string `json:"allows,omitempty"`
	Zones   []string `json:"zones,omitempty"`

	// ignored are the fields of an older settings.json read and no longer used (an API
	// driver's "kind", "url" and "cert_sha256"): never written back.
	ignored []string
}

// old is the "primary" an older settings.json has: an API driver's kind, its address and
// its certificate's pin, read only to be ignored.
type old struct {
	Address    string   `json:"address"`
	Allows     []string `json:"allows"`
	Zones      []string `json:"zones"`
	Kind       *string  `json:"kind"`
	URL        *string  `json:"url"`
	CertSHA256 *string  `json:"cert_sha256"`
}

// UnmarshalJSON reads "primary", unknown fields refused but for an older file's ("kind",
// "url", "cert_sha256"), which are accepted and ignored (Ignored). An older file's API
// address on an IP address gives Address, when none is set.
func (c *Config) UnmarshalJSON(b []byte) error {
	var o old
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&o); err != nil {
		return err
	}
	*c = Config{Address: o.Address, Allows: o.Allows, Zones: o.Zones}
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
	return nil
}

// Ignored are the older fields read and ignored ("kind", "url", "cert_sha256"), if any.
func (c Config) Ignored() []string { return slices.Clone(c.ignored) }

// IsZero: no address, networks or zones set.
func (c Config) IsZero() bool { return c.Address == "" && len(c.Allows) == 0 && len(c.Zones) == 0 }

// label is one label of a zone name.
var label = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// Check refuses an address that isn't an IPv4 address, a network that isn't one, a zone
// that isn't a zone name in lower case without its final dot, and anything given twice.
func (c Config) Check() error {
	if c.Address != "" {
		if a, err := netip.ParseAddr(c.Address); err != nil || !a.Is4() || a.String() != c.Address {
			return fmt.Errorf(`"address": %q is not an IPv4 address (the nodes copy their zones from one, as 192.0.2.254)`, c.Address)
		}
	}
	for i, v := range c.Allows {
		p, err := netip.ParsePrefix(v)
		switch {
		case err != nil || !p.Addr().Is4():
			return fmt.Errorf(`"allows": %q is not an IPv4 network with its prefix length (192.0.2.0/24)`, v)
		case p.Masked() != p:
			return fmt.Errorf(`"allows": %s is not a network (%s is)`, v, p.Masked())
		case slices.Contains(c.Allows[:i], v):
			return fmt.Errorf(`"allows": %s twice`, v)
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

// Covers is the network in Allows that addr is in, if any: the primary allows zone
// transfers from it.
func (c Config) Covers(addr netip.Addr) (netip.Prefix, bool) {
	for _, v := range c.Allows {
		if p, err := netip.ParsePrefix(v); err == nil && p.Contains(addr.Unmap()) {
			return p, true
		}
	}
	return netip.Prefix{}, false
}

// Advice is what to set up on the zone primary for a node at addr, which is in none of the
// networks it allows (Covers): its network, addr's with bits (24 when bits is 0), allowed
// there once. addr may be a name (a node on DHCP found by its name): then the network is
// left for you to name.
func Advice(addr string, bits int) string {
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return fmt.Sprintf("Your zone primary must allow zone transfers from %s: allow its network there once.", addr)
	}
	if bits <= 0 || bits > a.BitLen() {
		bits = 24
	}
	n, _ := a.Prefix(bits)
	return fmt.Sprintf("Your zone primary must allow zone transfers from %s: allow its network (e.g. %s) there once.", a, n)
}

// Uncovered is Advice for a node at addr when the primary allows none of its networks, ""
// when it does (or addr is a name, whose network can't be known: then Advice too).
func (c Config) Uncovered(addr string, bits int) string {
	if a, err := netip.ParseAddr(addr); err == nil {
		if _, ok := c.Covers(a); ok {
			return ""
		}
	}
	return Advice(addr, bits)
}
