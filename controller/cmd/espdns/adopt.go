package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/skitzo2000/espdns/controller/internal/adoption"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
)

// espdns adopt -host <address> | -node <id>  [-address 192.0.2.52/24 [-gateway ...] | -address dhcp]
//
//	[-config configs/<name>.json] [-name ...] [-reserved] [-identify 30s] [-add-to-settings]
//	[-dry-run]
//
// Adoption and addressing (docs/architecture.md, Addresses and adoption): the node is found on the static address it has
// (its board's default, the one it was flashed with, or its config's; no DHCP lease), and
// that address, or a new one, goes into its node config. The address comes from -address,
// else the config file's network, else what the node runs on now. A new address is kept only
// once the node is reached there. "dhcp" is for a network with a DHCP server (never
// one in settings.json's no_dhcp): the node's address must then be reserved there (-reserved).
//
// The zone primary (internal/primary) is settings.json's "primary": nothing is changed on
// it. For a config with secondary zones, adopt says once that it must allow zone transfers
// to the node's address, and goes on.
//
// The flags make an adoption.Request, which the controller's Adopt page makes too
// (adoption.Web): both are built and run by internal/adoption.
func cmdAdopt(args []string) (err error) {
	req, o, err := adoptRequest(args)
	if err != nil {
		return err
	}
	ctx, end, err := begin(*o.data, "adopt", args, *o.dryRun)
	if err != nil {
		return err
	}
	defer func() { end(err) }()
	c, err := adoptClient(o)
	if err != nil {
		return err
	}
	b, err := adoption.Build(ctx, c, req, log.Printf)
	if err != nil {
		return err
	}
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		b.Adopt.AskReserved = func(addr, mac string) bool {
			return confirmPrompt(fmt.Sprintf("Is %s reserved for MAC %s in the DHCP server?", addr, mac))
		}
	}
	_, err = adoption.Run(ctx, c, b, "cli", log.Printf)
	if errors.Is(err, fleet.ErrReservation) {
		return fmt.Errorf("%w (-reserved)", err)
	}
	return single(err)
}

// adoptClient is the client adopt runs with (a test reaches its fake network through it).
var adoptClient = func(o *fleetOpts) (*fleet.Client, error) { return o.client() }

// adoptRequest reads espdns adopt's flags into the request internal/adoption builds the
// adoption from: the controller's Adopt page makes the same request (adoption.Web).
func adoptRequest(args []string) (adoption.Request, *fleetOpts, error) {
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	o := addFleetFlags(fs)
	host := fs.String("host", "", "the node's address now (the one it was flashed with, or see espdns discover)")
	node := fs.String("node", "", "the node's ID (its MAC, from espdns discover): found over mDNS without -host, else checked")
	address := fs.String("address", "", "its static address with the prefix length (192.0.2.52/24), or dhcp on a network "+
		"with a DHCP server, never one in settings.json's no_dhcp (default: the config's, else the one it runs on now)")
	gateway := fs.String("gateway", "", "the gateway (default: the config's, else the node's now)")
	cfgFile := fs.String("config", "", "the node's config (JSON, controller/internal/nodecfg; a bare file name is the data directory's configs/<name>); its network is set from the address")
	name := fs.String("name", "", "the node's name (default: the config's)")
	reserved := fs.Bool("reserved", false, "with dhcp: the node's address is reserved for its MAC in the DHCP server "+
		"(without it, adopt asks, or stops after saying what to reserve)")
	identify := fs.Duration("identify", 0, "flicker the node's LED this long first, to match it to the board in your hand")
	addSettings := fs.Bool("add-to-settings", false, "once adopted, add the node's address to settings.json (the nodes rollouts count and change)")
	wait := fs.Duration("wait", adoption.DefaultWait, "how long to wait for the node on its new address")
	catalog := fs.String("catalog", defaultCatalog, "the board catalog, for the config's memory plan on a node from before the plan")
	checks := checkFlags(fs)
	fs.Parse(args)
	return adoption.Request{Host: *host, Node: *node, Address: *address, Gateway: *gateway, Config: *cfgFile, Name: *name,
		Reserved: *reserved, Identify: *identify, AddToSettings: *addSettings, Wait: *wait, Catalog: *catalog, Checks: checks(),
		DataDir: *o.data, Settings: *o.settings, MDNS: *o.mdns, Peers: o.peers, DNSPeers: o.dnsPeers.addrs,
		DNSPeerZones: o.dnsPeers.zones, NoDNSPeers: *o.dnsPeers.none, AllowSingle: *o.allowSingle, Force: *o.force,
		DryRun: *o.dryRun}, o, nil
}
