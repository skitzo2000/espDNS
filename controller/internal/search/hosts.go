package search

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/miekg/dns"

	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
)

// Hosts names the query log's devices (top sites and top
// devices, named from the zones' records where they can be): each address an A or AAAA
// record of a hosted zone has, as the zone files are now (not as pending changes make them),
// with the first of its record names in order; an address a PTR record of a hosted (reverse)
// zone names takes that name instead (the first of them), as a reverse lookup would. A zone
// file that can't be read or parsed is left out: the Zones page says why. It reads only the
// data directory.
func Hosts(dataDir string) (map[string]string, error) {
	names, err := zonefiles.Store(dataDir).Names()
	if err != nil {
		return nil, err
	}
	all, ptrs := map[string][]string{}, map[string][]string{}
	for _, name := range names {
		v, err := changes.Current(dataDir, changes.Zone, name)
		if err != nil || !v.Exists {
			continue
		}
		z, err := zonefiles.Parse(name, v.Text)
		if err != nil {
			continue
		}
		for _, rec := range z.Records {
			rr, err := text(rec)
			if err != nil {
				continue
			}
			owner := strings.TrimSuffix(rr.Header().Name, ".")
			if a := address(rr); a != "" && !strings.HasPrefix(owner, "*") {
				all[a] = addOnce(all[a], owner)
			}
			if p, ok := rr.(*dns.PTR); ok {
				if a := ptrAddr(owner); a != "" {
					ptrs[a] = addOnce(ptrs[a], strings.ToLower(strings.TrimSuffix(p.Ptr, ".")))
				}
			}
		}
	}
	out := make(map[string]string, len(all)+len(ptrs))
	for a, hs := range all {
		out[a] = slices.Min(hs)
	}
	for a, hs := range ptrs {
		out[a] = slices.Min(hs)
	}
	return out, nil
}

// ptrAddr is the address a reverse name stands for ("40.2.0.192.in-addr.arpa" is
// 192.0.2.40; ip6.arpa's 32 nibbles an IPv6 address), "" for any other name.
func ptrAddr(owner string) string {
	o := strings.ToLower(owner)
	if v4, ok := strings.CutSuffix(o, ".in-addr.arpa"); ok {
		l := strings.Split(v4, ".")
		if len(l) != 4 {
			return ""
		}
		var b [4]byte
		for i, x := range l {
			n, err := strconv.ParseUint(x, 10, 8)
			if err != nil || x != strconv.FormatUint(n, 10) {
				return ""
			}
			b[3-i] = byte(n)
		}
		return netip.AddrFrom4(b).String()
	}
	if v6, ok := strings.CutSuffix(o, ".ip6.arpa"); ok {
		l := strings.Split(v6, ".")
		if len(l) != 32 {
			return ""
		}
		var b [16]byte
		for i, x := range l {
			n, err := strconv.ParseUint(x, 16, 8)
			if err != nil || len(x) != 1 {
				return ""
			}
			j := 31 - i // the nibbles go least significant first
			if j%2 == 0 {
				b[j/2] |= byte(n) << 4
			} else {
				b[j/2] |= byte(n)
			}
		}
		return netip.AddrFrom16(b).String()
	}
	return ""
}
