package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
	"github.com/skitzo2000/espdns/controller/internal/service"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// espdns node out -node 192.0.2.52 -data /data      (out of service: sent nothing, not counted)
// espdns node in -node 192.0.2.52 -data /data       (back in service: brought up to date first)
// espdns node remove -node 192.0.2.52 -data /data   (out of settings.json; the node isn't touched)
//
// As the node page's buttons (the controller's jobs node-out, node-in and node-remove;
// internal/service): each under the fleet lock, in the action log. out and remove change
// settings.json only, so they work while the node is offline. in needs the node to answer
// and to trust the release key (-key), then sends it, alone, what it missed (its config, the
// hosted zones, the overrides, its blocklist, newer firmware from the data directory), every
// rollout a dry run first, before it counts again.
func cmdNode(args []string) (err error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("espdns node out|in|remove -node <address> -data /data")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("node "+sub, flag.ExitOnError)
	dataDir := dataFlag(fs)
	node := fs.String("node", "", "the node's address, as settings.json lists it")
	var key, catalog *string
	if sub == "in" {
		key = fs.String("key", defaultKey, "release signing key: a PEM file, or - to read it (PEM or base64) from standard input")
		catalog = fs.String("catalog", defaultCatalog, "the board catalog: for the memory plan of a node on firmware from before it, and the image its board runs")
	}
	fs.Parse(args)
	if *node == "" {
		return errors.New("need -node: the node's address, as settings.json lists it")
	}
	path := settings.Path(*dataDir)
	s, err := settings.Load(path)
	if err != nil {
		return err
	}
	switch sub {
	case "out":
		if err := service.CheckOut(*dataDir, s, *node); err != nil {
			return err
		}
	case "remove":
		pending, err := changes.New(*dataDir).List()
		if err != nil {
			return err
		}
		if err := service.CheckRemove(*dataDir, s, *node, pending); err != nil {
			return err
		}
	case "in":
		if err := service.CheckIn(s, *node); err != nil {
			return err
		}
	default:
		return fmt.Errorf("node %s: out, in or remove", sub)
	}
	ctx, end, err := begin(*dataDir, "node", append([]string{sub}, args...), false)
	if err != nil {
		return err
	}
	defer func() { end(err) }()
	switch sub {
	case "out":
		if _, err := settings.SetOutOfService(path, *node, true); err != nil {
			return err
		}
		fmt.Printf("%s: out of service: still listed, sent nothing, not counted while another node is changed\n", *node)
	case "remove":
		if _, err := settings.RemoveNode(path, *node); err != nil {
			return err
		}
		fmt.Printf("%s: removed from settings.json; its config file is kept. The node itself isn't touched: if it is up it "+
			"still answers DNS, so take it off your router's DNS servers\n", *node)
	case "in":
		if err := returnNode(ctx, *dataDir, *catalog, *key, *node); err != nil {
			return err
		}
		if _, err := settings.SetOutOfService(path, *node, false); err != nil {
			return err
		}
		fmt.Printf("%s: back in service\n", *node)
	}
	return nil
}

// returnNode brings node up to date (service.CatchUp): every rollout a dry run first, then
// each pushed, as the controller's node-in job does.
func returnNode(ctx context.Context, dataDir, catalog, key, node string) error {
	p, err := pusher(key, false, dataDir)
	if err != nil {
		return err
	}
	c := &fleet.Client{Poll: time.Second, Logf: log.Printf, Pusher: p}
	fp := release.Fingerprint(release.PublicRaw(p.Key))
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	st, err := c.Status(sctx, node)
	cancel()
	if err := service.Ready(node, st, err, fp); err != nil {
		return err
	}
	steps, notes, err := service.CatchUp(dataDir, catalog, node, st, changes.New(dataDir).ListSums)
	if err != nil {
		return err
	}
	for _, n := range notes {
		log.Printf("%s: %s", node, n)
	}
	var reqs []rolling.Request
	for _, sp := range steps {
		sp.Web.DryRun = true
		r, err := sp.Web.Request(dataDir, catalog)
		if err != nil {
			return fmt.Errorf("%s: %w", sp.What, err)
		}
		reqs = append(reqs, r)
	}
	// Every dry run before the node is touched, then each pushed: the same request.
	for i, r := range reqs {
		b, err := rolling.Build(ctx, c, r, log.Printf)
		if err == nil {
			_, err = c.Rollout(ctx, b.Plan, b.Change)
		}
		if err != nil {
			return fmt.Errorf("%s: the check before any push: %w", steps[i].What, single(err))
		}
		log.Printf("%s: checked", steps[i].What)
	}
	for i, r := range reqs {
		r.DryRun = false
		b, err := rolling.Build(ctx, c, r, log.Printf)
		if err != nil {
			return fmt.Errorf("%s: %w", steps[i].What, err)
		}
		log.Printf("%s: to %s", steps[i].What, node)
		res, err := c.Rollout(ctx, b.Plan, b.Change)
		rolling.Report(log.Printf, b.Plan, res)
		if r.Kind == release.Config {
			rolling.RecordPushed(ctx, c, dataDir, b.Specs, res.Done, "cli", log.Printf)
		}
		if r.Kind == release.Zones {
			rolling.RecordZones(ctx, c, r, res.Done, "cli", log.Printf)
		}
		if err != nil {
			return fmt.Errorf("%s: %w (%s stays out of service)", steps[i].What, single(err), node)
		}
	}
	return nil
}
