package main

// The dashboard's and the Query log page's APIs (internal/observe): GET only, JSON. The
// dashboard is open as the node list is (read-only without a password); the query log and
// the names in it need a login, as the configs do: they say who asked for what.
//
//	GET /api/dashboard        the fleet's rates now and over the hour, and each node's
//	GET /api/querylog         ?node=<key>&client=&device=&name=&q=&result=<a,b>&qtype=&after=<id>&run=<its run>&limit=
//	                          the newest entries that match; names: the devices' names, from the hosted zones
//	GET /api/querylog/top     ?node=<key>: the sites and devices asked most; names as above
//	GET /api/querylog/askers  ?name=<site>: the devices that asked for it (the most recent
//	                          first, at most observe.AskersMax), each named from the zones or
//	                          the nodes, and marked when it is a gateway (Blocking's "check a site")

import (
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/observe"
	"github.com/skitzo2000/espdns/controller/internal/search"
)

type observeServer struct {
	store   *observe.Store
	dataDir string
	hosts   *hostNames
	nodes   func() []nodes.Node
}

// asker is one device that asked for a site, as Blocking's "check a site" lists it.
type asker struct {
	observe.Asker
	Name string `json:"name,omitempty"` // its name, from the zones' records or the nodes
	// Gateway: the address is a gateway in a node's config or the address a node runs on;
	// it asks for the devices behind it.
	Gateway bool `json:"gateway,omitempty"`
}

// askersOf is the store's read with each device's name and whether it is a gateway.
func askersOf(v observe.Askers, names map[string]string, gws map[string]bool) any {
	out := []asker{}
	for _, a := range v.Askers {
		x := asker{Asker: a}
		if a.Client != nil {
			x.Name, x.Gateway = names[*a.Client], gws[*a.Client]
		}
		out = append(out, x)
	}
	type view observe.Askers // its fields, but for the askers
	return struct {
		view
		Askers []asker `json:"askers"`
	}{view(v), out}
}

// deviceNames names addresses from the hosted zones' records (search.Hosts: PTR, else A
// and AAAA) and the nodes' own addresses (a node's name). Copied zones are left out: the
// controller holds none of their records (it reads them from the primary only when the
// Zones page asks).
func deviceNames(hosts map[string]string, list []nodes.Node) map[string]string {
	out := make(map[string]string, len(hosts)+len(list))
	for _, n := range list {
		a := n.Addr
		if h, _, err := net.SplitHostPort(a); err == nil {
			a = h
		}
		name := strings.TrimSuffix(n.Hostname, ".local")
		if c, ok := n.Status["config"].(map[string]any); ok {
			if s, _ := c["name"].(string); s != "" {
				name = s
			}
		}
		if a != "" && name != "" {
			out[a] = name
		}
	}
	for a, n := range hosts {
		if _, ok := out[a]; !ok {
			out[a] = n
		}
	}
	return out
}

// gateways are the addresses that are a gateway to the nodes: each config's
// "network": {"gateway"} and the gateway each node last said it runs with (/status
// config.gateway, set on the node or when it was flashed).
func gateways(dataDir string, list []nodes.Node) map[string]bool {
	out := map[string]bool{}
	add := func(s string) {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil {
			out[a.String()] = true
		}
	}
	if fs, err := configs.List(dataDir); err == nil {
		for _, f := range fs {
			if f.Config != nil && f.Config.Network != nil {
				add(f.Config.Network.Gateway)
			}
		}
	} else {
		log.Printf("check a site: the configs' gateways: %v", err)
	}
	for _, n := range list {
		if c, ok := n.Status["config"].(map[string]any); ok {
			if s, _ := c["gateway"].(string); s != "" {
				add(s)
			}
		}
	}
	return out
}

// hostNames are the hosted zones' addresses and names (search.Hosts), read again at most
// every hostsFor: the query log is followed every few seconds.
type hostNames struct {
	mu   sync.Mutex
	at   time.Time
	data map[string]string
}

const hostsFor = 15 * time.Second

func (h *hostNames) get(dataDir string) map[string]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.data == nil || time.Since(h.at) >= hostsFor {
		d, err := search.Hosts(dataDir)
		if err != nil {
			log.Printf("query log: the devices' names: %v", err)
		}
		if d == nil {
			d = map[string]string{}
		}
		h.data, h.at = d, time.Now()
	}
	return h.data
}

// namesOf are the names of the addresses in addrs that have one.
func namesOf(hosts map[string]string, addrs func(yield func(string))) map[string]string {
	out := map[string]string{}
	addrs(func(a string) {
		if n, ok := hosts[a]; ok {
			out[a] = n
		}
	})
	return out
}

func (o observeServer) routes(mux *routes) {
	if o.hosts == nil {
		o.hosts = &hostNames{}
	}
	mux.HandleFunc("GET /api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, o.store.Dashboard())
	})
	mux.HandleFunc("GET /api/querylog", needLogin("the query log needs", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f := observe.Filter{Node: q.Get("node"), Client: q.Get("client"), Device: q.Get("device"), Name: q.Get("name"),
			Text: strings.TrimSpace(q.Get("q")), Result: q.Get("result"), QType: q.Get("qtype"), Run: q.Get("run")}
		if s := q.Get("after"); s != "" {
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				httpErr(w, http.StatusBadRequest, errors.New("after: a whole number"))
				return
			}
			f.After = n
		}
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				httpErr(w, http.StatusBadRequest, errors.New("limit: a whole number, 1 or more"))
				return
			}
			f.Limit = n
		}
		hosts := o.hosts.get(o.dataDir)
		if f.Text != "" {
			// The box finds a device by its name too.
			t := strings.ToLower(f.Text)
			f.TextClients = map[string]bool{}
			for a, n := range hosts {
				if strings.Contains(strings.ToLower(n), t) {
					f.TextClients[a] = true
				}
			}
		}
		v := o.store.Log(f)
		writeJSON(w, struct {
			observe.LogView
			Names map[string]string `json:"names"`
		}{v, namesOf(hosts, func(yield func(string)) {
			for _, e := range v.Entries {
				if e.Client != nil {
					yield(*e.Client)
				}
			}
		})})
	}))
	mux.HandleFunc("GET /api/querylog/askers", needLogin("the query log needs", func(w http.ResponseWriter, r *http.Request) {
		name, ok := blocklist.Canon(r.URL.Query().Get("name"))
		if !ok {
			httpErr(w, http.StatusBadRequest, errors.New("name: a site's name, like ads.example.com"))
			return
		}
		var list []nodes.Node
		if o.nodes != nil {
			list = o.nodes()
		}
		v := o.store.Askers(name, observe.AskersMax)
		writeJSON(w, askersOf(v, deviceNames(o.hosts.get(o.dataDir), list), gateways(o.dataDir, list)))
	}))
	mux.HandleFunc("GET /api/querylog/top", needLogin("the query log needs", func(w http.ResponseWriter, r *http.Request) {
		t := o.store.Top(r.URL.Query().Get("node"))
		hosts := o.hosts.get(o.dataDir)
		writeJSON(w, struct {
			observe.Top
			Names map[string]string `json:"names"`
		}{t, namesOf(hosts, func(yield func(string)) {
			for _, c := range t.Clients {
				yield(c.Name)
			}
		})})
	}))
}
