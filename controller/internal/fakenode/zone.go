package fakenode

// The fake node's copies of its secondary zones, as firmware/main/zread.c hands them over:
// GET /zone, a page at a time, with a gen that a swapped copy bumps (a 409 for a read that
// passes the old one), and /status "reads": ["zone"] while it holds Copies.

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// SetCopy replaces its copy of a zone, as a refresh does: the zone's gen goes up. Locked:
// call it in Do or ZonePage.
func (n *Node) SetCopy(name string, rrs []dns.RR) {
	if n.Copies == nil {
		n.Copies = map[string][]dns.RR{}
	}
	if n.copyGen == nil {
		n.copyGen = map[string]uint64{}
	}
	n.Copies[name] = rrs
	n.copyGen[name]++
}

// copySerial is the serial of its copy of a zone (its first record's, the SOA), 0 if none.
func (n *Node) copySerial(name string) uint32 {
	if rrs := n.Copies[name]; len(rrs) > 0 {
		if soa, ok := rrs[0].(*dns.SOA); ok {
			return soa.Serial
		}
	}
	return 0
}

// serveZone answers GET /zone. Locked.
func (n *Node) serveZone(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	num := func(k string, def uint64) (uint64, bool) {
		if q.Get(k) == "" {
			return def, true
		}
		v, err := strconv.ParseUint(q.Get(k), 10, 64)
		return v, err == nil
	}
	from, ok1 := num("from", 0)
	limit, ok2 := num("limit", 200)
	gen, ok3 := num("gen", n.copyGen[q.Get("name")])
	w.Header().Set("Content-Type", "application/json")
	rrs, held := n.Copies[q.Get("name")]
	switch {
	case !ok1 || !ok2 || !ok3 || limit == 0 || q.Get("name") == "":
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"name: a zone; from, limit and gen: whole numbers, limit 1 or more"}` + "\n"))
		return
	case !held:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"no such zone held here"}` + "\n"))
		return
	case gen != n.copyGen[q.Get("name")]:
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"the zone changed: read it again from 0"}` + "\n"))
		return
	}
	type rec struct {
		O   string `json:"o"`
		T   uint16 `json:"t"`
		TTL uint32 `json:"ttl"`
		D   string `json:"d"`
	}
	page := []rec{}
	i := from
	for ; i < uint64(len(rrs)) && i-from < min(limit, 500); i++ {
		buf := make([]byte, dns.Len(rrs[i])+16)
		end, err := dns.PackRR(rrs[i], buf, 0, nil, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		h := rrs[i].Header()
		page = append(page, rec{strings.TrimSuffix(h.Name, "."), h.Rrtype, h.Ttl, hex.EncodeToString(buf[end-int(h.Rdlength) : end])})
	}
	name := q.Get("name")
	json.NewEncoder(w).Encode(map[string]any{"name": name, "serial": n.copySerial(name), "gen": n.copyGen[name],
		"records": len(rrs), "from": from, "rrs": page, "next": i, "more": i < uint64(len(rrs))})
	if n.ZonePage != nil {
		n.ZonePage(n)
	}
}
