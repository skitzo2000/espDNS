package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/skitzo2000/espdns/controller/internal/adoption"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/inventory"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// Every zone the fleet answers for, in one place (internal/inventory), for the Zones page:
//
//	GET /api/zone-inventory           every zone (hosted, secondary, forward), its source and the fleet's state; the zone primary
//	GET /api/zone-lookup?name=<zone>  where a typed zone lives: the fleet's already, a configured primary's (asked over DNS), or none
//	GET /api/zone-records?name=<zone> a copied (secondary) zone's records, read-only: a node's copy (GET /zone), else one AXFR from the primary the configs name
//
// Each needs a login. The lookup sends each configured primary one SOA query, without
// recursion; the records are read from a node that holds the zone, or by one zone transfer
// from the zone's primary when none can hand it over, only when asked; nothing else leaves
// the controller.

// lookupTimeout bounds one lookup, every primary asked.
const lookupTimeout = 6 * time.Second

type inventoryServer struct {
	dataDir string
	nodes   func() []nodes.Node
	dns     inventory.Resolver // nil: UDP to port 53 of each primary
	xfr     zoneTransfer       // nil: AXFR over TCP to port 53 of the zone's primary
	client  *fleet.Client      // nil: the defaults; reads a node's copy of a zone
}

func (v inventoryServer) routes(mux *routes) {
	need := func(h http.HandlerFunc) http.HandlerFunc { return needLogin("the zone inventory needs", h) }
	mux.HandleFunc("GET /api/zone-inventory", need(v.list))
	mux.HandleFunc("GET /api/zone-lookup", need(v.lookup))
	mux.HandleFunc("GET /api/zone-records", need(v.records))
}

// build is the inventory of the fleet: the nodes in settings.json (any not seen yet
// counted as unread), or every node the controller knows when it lists none.
func (v inventoryServer) build() (inventory.Inventory, settings.Settings, error) {
	s, err := settings.Load(settings.Path(v.dataDir))
	if err != nil {
		return inventory.Inventory{}, s, err
	}
	known := v.nodes()
	var fl []inventory.Node
	node := func(n nodes.Node) inventory.Node {
		in := inventory.Node{Host: n.Addr}
		if st, ok := status(n); ok {
			in.Status = &st
		}
		return in
	}
	if len(s.Nodes) == 0 {
		for _, n := range known {
			fl = append(fl, node(n))
		}
	}
	for _, h := range s.Nodes {
		in := inventory.Node{Host: h}
		for _, n := range known {
			if n.Addr == h {
				in = node(n)
				break
			}
		}
		fl = append(fl, in)
	}
	inv, err := inventory.Build(v.dataDir, fl)
	return inv, s, err
}

// primaryFacts is settings.json's zone primary as the pages show it: its address, the
// networks it allows zone transfers from, the zones to copy from it, and the nodes in settings.json whose address is
// in none of them (each must be allowed there once).
func primaryFacts(s settings.Settings) map[string]any {
	zp := s.ZonePrimary()
	uncovered := []string{}
	for _, h := range s.Nodes {
		a, err := netip.ParseAddr(adoption.HostOnly(h))
		if err != nil {
			continue // a name: its address isn't known here
		}
		if _, ok := zp.Covers(a); !ok {
			uncovered = append(uncovered, a.String())
		}
	}
	return map[string]any{"set": !zp.IsZero(), "address": zp.Address, "allows": append([]string{}, zp.Allows...),
		"zones": append([]string{}, zp.Zones...), "uncovered": uncovered}
}

func (v inventoryServer) list(w http.ResponseWriter, r *http.Request) {
	inv, s, err := v.build()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	// The zone primary without the nodes it doesn't cover: the inventory names no node
	// (GET /api/settings and /api/adopt do).
	zp := primaryFacts(s)
	delete(zp, "uncovered")
	writeJSON(w, struct {
		inventory.Inventory
		Primary map[string]any `json:"primary"`
	}{inv, zp})
}

// primaries are the primaries a lookup asks, in order: settings.json's zone primary (its
// address), then each the configs copy zones from.
func primaries(s settings.Settings, inv inventory.Inventory) []string {
	var out []string
	if a := s.ZonePrimary().Address; a != "" {
		out = append(out, a)
	}
	for _, p := range inv.Primaries {
		if p != "" && (len(out) == 0 || out[0] != p) {
			out = append(out, p)
		}
	}
	return out
}

func (v inventoryServer) lookup(w http.ResponseWriter, r *http.Request) {
	name, err := inventory.CheckName(r.URL.Query().Get("name"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	inv, s, err := v.build()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	res := v.dns
	if res == nil {
		res = fleet.UDP{}
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	writeJSON(w, inventory.Find(ctx, res, inv, name, primaries(s, inv)))
}

// zoneTransfer reads a zone whole from a primary (AXFR): its records, the closing SOA left
// out.
type zoneTransfer interface {
	Transfer(ctx context.Context, primary, zone string) ([]dns.RR, error)
}

// Limits of one copied zone's read: the records kept (a node holds far fewer), the time
// (from each node tried, from the nodes together, then from the primary).
const (
	maxTransfer      = 20000
	transferTimeout  = 20 * time.Second
	nodeReadTimeout  = 10 * time.Second
	nodeReadsTimeout = 15 * time.Second
	// maxShownRecords are the records a reply lists; "more" counts the rest.
	maxShownRecords = 5000
)

// axfr is a zone transfer over TCP to port 53 (port: another, for tests), as a node's
// (firmware/main/axfr.h).
type axfr struct{ port string }

func (a axfr) Transfer(ctx context.Context, primary, zone string) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.SetAxfr(dns.Fqdn(zone))
	t := &dns.Transfer{DialTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second}
	port := a.port
	if port == "" {
		port = "53"
	}
	at := net.JoinHostPort(primary, port)
	// Dialed here, not by the transfer, to know the address the primary sees this computer
	// as (the connection's local end): the address to allow on it when it refuses.
	d := net.Dialer{Timeout: t.DialTimeout}
	c, err := d.DialContext(ctx, "tcp", at)
	if err != nil {
		return nil, err
	}
	src := ""
	if la, ok := c.LocalAddr().(*net.TCPAddr); ok {
		src = la.IP.String()
	}
	t.Conn = &dns.Conn{Conn: c}
	rrs, err := readTransfer(ctx, t, m, at)
	if err != nil {
		return nil, &xfrError{source: src, err: err}
	}
	return rrs, nil
}

// readTransfer runs a transfer on its connection, to the end or ctx.
func readTransfer(ctx context.Context, t *dns.Transfer, m *dns.Msg, at string) ([]dns.RR, error) {
	env, err := t.In(m, at)
	if err != nil {
		t.Close()
		return nil, err
	}
	// The read timeouts bound each message, not the whole transfer: past ctx (a primary
	// sending slowly, or the browser gone) the connection is closed, which ends it.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			t.Close()
		case <-done:
		}
	}()
	var out []dns.RR
	var ferr error
	// Read to the end (or the timeouts), whatever is kept: the transfer's goroutine sends
	// until it is done.
	for e := range env {
		switch {
		case ferr != nil:
		case e.Error != nil:
			ferr = e.Error
		case ctx.Err() != nil:
			ferr = ctx.Err()
		case len(out)+len(e.RR) > maxTransfer:
			ferr = fmt.Errorf("over %d records", maxTransfer)
		default:
			out = append(out, e.RR...)
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if ferr != nil {
		return nil, ferr
	}
	// The transfer ends with the SOA it started with.
	if n := len(out); n > 1 && out[n-1].Header().Rrtype == dns.TypeSOA {
		out = out[:n-1]
	}
	return out, nil
}

// xfrError is a failed zone transfer with the address the controller connected from (the
// one the primary sees), when it got that far.
type xfrError struct {
	source string
	err    error
}

func (e *xfrError) Error() string { return e.err.Error() }
func (e *xfrError) Unwrap() error { return e.err }

// Why a copied zone's read failed, for the page to phrase.
const (
	xfrRefused     = "refused"     // the primary refused the transfer to this computer (REFUSED, NOTAUTH)
	xfrUnknown     = "unknown"     // the primary doesn't serve the zone (NXDOMAIN, NOTZONE, no SOA first)
	xfrTimeout     = "timeout"     // no answer in time
	xfrUnreachable = "unreachable" // no connection to it
	xfrFailed      = "failed"      // anything else
)

// xfrFail is a transfer's failure as the records route reports it: its kind, the address
// the primary saw (if known) and the technical detail, kept out of the sentence.
type xfrFail struct {
	Kind, Source, Detail string
}

// xfrFailure sorts a transfer's error.
func xfrFailure(err error) xfrFail {
	f := xfrFail{Kind: xfrFailed, Detail: err.Error()}
	var xe *xfrError
	if errors.As(err, &xe) {
		f.Source = xe.source
	}
	var rcode int
	var ne net.Error
	var oe *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		f.Kind, f.Detail = xfrTimeout, "no answer in time"
	case xfrRcode(err, &rcode):
		f.Detail = fmt.Sprintf("rcode %d", rcode)
		if s, ok := dns.RcodeToString[rcode]; ok {
			f.Detail += " (" + s + ")"
		}
		switch rcode {
		case dns.RcodeRefused, dns.RcodeNotAuth:
			f.Kind = xfrRefused
		case dns.RcodeNameError, dns.RcodeNotZone:
			f.Kind = xfrUnknown
		}
	case errors.Is(err, dns.ErrSoa):
		f.Kind = xfrUnknown
	case errors.As(err, &oe) && oe.Op == "dial":
		f.Kind = xfrUnreachable
	}
	return f
}

// xfrRcode reads the rcode out of a refused transfer's error ("dns: bad xfr rcode: 5"):
// the library gives it only in words.
func xfrRcode(err error, rcode *int) bool {
	msg := err.Error()
	const p = "bad xfr rcode: "
	i := strings.Index(msg, p)
	if i < 0 {
		return false
	}
	_, e := fmt.Sscanf(msg[i+len(p):], "%d", rcode)
	return e == nil
}

// say is the failure in plain words, for a client that shows only "error".
func (f xfrFail) say(primary string) string {
	switch f.Kind {
	case xfrRefused:
		return fmt.Sprintf("your primary DNS server (%s) refused to send this zone to the controller: allow zone transfers from this computer's address on it", primary)
	case xfrUnknown:
		return fmt.Sprintf("your primary DNS server (%s) doesn't serve this zone: it isn't authoritative for it", primary)
	case xfrTimeout:
		return fmt.Sprintf("the controller can't reach your primary DNS server (%s): it didn't answer in time", primary)
	case xfrUnreachable:
		return fmt.Sprintf("the controller can't reach your primary DNS server (%s): it didn't take the connection", primary)
	}
	return fmt.Sprintf("your primary DNS server (%s) didn't send this zone to the controller", primary)
}

// zoneRecord is one record as the Zones page lists it.
type zoneRecord struct {
	Owner string `json:"owner"`
	Type  string `json:"type"`
	TTL   uint32 `json:"ttl"`
	Data  string `json:"data"`
}

// holder is a node to read a copied zone from: its name, address and copy's serial.
type holder struct {
	name, host string
	serial     uint32
}

// holders are the nodes to read a copied zone from, best first: in the fleet (settings.json's
// nodes, or every node known when it lists none), online, in service and answering, serving
// GET /zone, with a copy that hasn't expired; the highest serial first.
func holders(ns []nodes.Node, listed []string, zone string) []holder {
	var out []holder
	for _, n := range ns {
		st, ok := status(n)
		if !ok || !n.Online || n.OutOfService || len(listed) > 0 && !slices.Contains(listed, n.Addr) ||
			st.Health != nil && !st.Health.Answering || !slices.Contains(st.Reads, "zone") {
			continue
		}
		name := n.Addr
		if st.Config != nil && st.Config.Name != "" {
			name = st.Config.Name
		}
		for _, z := range st.Zones {
			if strings.EqualFold(strings.TrimSuffix(z.Name, "."), zone) && !z.Expired && z.Serial != 0 {
				out = append(out, holder{name, n.Addr, z.Serial})
			}
		}
	}
	slices.SortStableFunc(out, func(a, b holder) int { return cmp.Compare(b.serial, a.serial) })
	return out
}

// readFromNode reads the zone from the first holder that hands it over: the records, and
// the node's name ("" if none did).
func (v inventoryServer) readFromNode(ctx context.Context, hs []holder, zone string) (fleet.Zone, string) {
	c := v.client
	if c == nil {
		c = &fleet.Client{}
	}
	ctx, cancel := context.WithTimeout(ctx, nodeReadsTimeout)
	defer cancel()
	for _, h := range hs {
		rctx, cancel := context.WithTimeout(ctx, nodeReadTimeout)
		z, err := c.ReadZone(rctx, h.host, zone)
		cancel()
		if err == nil {
			return z, h.name
		}
	}
	return fleet.Zone{}, ""
}

func (v inventoryServer) records(w http.ResponseWriter, r *http.Request) {
	name, err := inventory.CheckName(r.URL.Query().Get("name"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	inv, s, err := v.build()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	z, ok := inv.Of(name)
	if !ok || z.Kind != inventory.KindSecondary {
		httpErr(w, http.StatusNotFound, fmt.Errorf("%s is not a zone the nodes copy from a primary", name))
		return
	}
	// A node's copy first, the data the fleet answers from; the primary if none hands it over.
	nz, node := v.readFromNode(r.Context(), holders(v.nodes(), s.Nodes, name), name)
	rrs, serial := nz.RRs, nz.Serial
	if node == "" {
		if z.Primary == "" {
			httpErr(w, http.StatusConflict, fmt.Errorf("no node handed over %s and no config names its primary: its records can't be read from here", name))
			return
		}
		xfr := v.xfr
		if xfr == nil {
			xfr = axfr{}
		}
		ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
		defer cancel()
		if rrs, err = xfr.Transfer(ctx, z.Primary, name); err != nil {
			// "error" in words, as every route's; the rest for the page to phrase it.
			f := xfrFailure(err)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			json.NewEncoder(w).Encode(map[string]string{"error": f.say(z.Primary), "kind": f.Kind,
				"primary": z.Primary, "source": f.Source, "detail": f.Detail})
			return
		}
	}
	out := []zoneRecord{}
	for _, rr := range rrs {
		h := rr.Header()
		if soa, ok := rr.(*dns.SOA); ok && serial == 0 {
			serial = soa.Serial
		}
		if len(out) == maxShownRecords {
			continue
		}
		out = append(out, zoneRecord{Owner: strings.ToLower(h.Name), Type: dns.TypeToString[h.Rrtype], TTL: h.Ttl,
			Data: strings.TrimPrefix(rr.String(), h.String())})
	}
	// node: the node the records were read from; "" when they came from the primary.
	writeJSON(w, map[string]any{"name": name, "primary": z.Primary, "node": node, "serial": serial, "records": out, "more": len(rrs) - len(out)})
}
