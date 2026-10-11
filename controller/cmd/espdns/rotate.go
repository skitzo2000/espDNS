package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// espdns key rotate prepare -data /data [-new-release generate|<file>] [-new-recovery <pub>] [-recovery-pub <pub>] [-compromised]
// espdns key rotate sign -dir <rotation> -key <recovery.pem or ->      (offline: docker run --network none)
// espdns key rotate push -data /data -dir <rotation> [-node <address> ...]
// espdns key rotate discard -data /data -dir <rotation>
//
// The fleet's keys, rotated (internal/rotation; docs/security.md, Rotating keys): prepare
// writes an unsigned KEYS release for every node in settings.json into
// <data>/keys/rotation/<id>/ (and, with a new release key, that key into
// <data>/keys/release.next.pem); sign signs them offline with the recovery private key every
// node trusts, which it reads from a file or standard input and never writes; push sends
// them one node at a time under the fleet lock, signs again with the new release key what
// each node stores, and swaps the controller's keys once every listed node has the new ones.
// discard removes a rotation no node has taken.
func keyRotate(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: espdns key rotate prepare|sign|push|discard [flags]")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "prepare":
		return rotatePrepare(args, os.Stdout)
	case "sign":
		return rotateSign(args, os.Stdin, os.Stdout)
	case "push":
		return rotatePush(args)
	case "discard":
		return rotateDiscard(args, os.Stdout)
	}
	return fmt.Errorf("unknown key rotate command %q (prepare, sign, push or discard)", sub)
}

func rotatePrepare(args []string, w io.Writer) error {
	fl := flag.NewFlagSet("key rotate prepare", flag.ExitOnError)
	dataDir := dataFlag(fl)
	settingsFile := fl.String("settings", "", "the settings file whose nodes are rotated (default: settings.json in -data)")
	newRel := fl.String("new-release", "", "the new release key: generate (made here, into <data>/keys/release.next.pem), or a private key file "+
		"(PEM, or base64 of one); none: the release key stays")
	newRec := fl.String("new-recovery", "", "the new recovery PUBLIC key (raw 65 bytes, or PEM); none: the recovery key stays")
	recPub := fl.String("recovery-pub", "", "the recovery public key the nodes trust now (default: <data>/keys/recovery.pub, or -new-recovery when it is that key)")
	compromised := fl.Bool("compromised", false, "the release key is compromised: the nodes don't keep it as their retired key, and every node "+
		"must have what it stores signed again (needs -new-release)")
	timeout := fl.Duration("timeout", 30*time.Second, "how long to wait for the nodes' /status")
	fl.Parse(args)
	if fl.NArg() > 0 {
		return fmt.Errorf("takes no arguments (got %q)", fl.Args())
	}
	s, err := settings.Load(settingsPath(*settingsFile, *dataDir))
	if err != nil {
		return err
	}
	cur, err := keys.FileSource{Path: keys.Path(*dataDir)}.Key()
	if err != nil {
		return err
	}
	o := rotation.PrepareOptions{DataDir: *dataDir, Current: cur, Compromised: *compromised,
		LastSeq: rotation.LastSeq(*dataDir), Repush: rotation.Repush(*dataDir)}
	switch *newRel {
	case "":
	case "generate":
		if o.NewRelease, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			return err
		}
	default:
		b, err := os.ReadFile(*newRel)
		if err != nil {
			return err
		}
		if o.NewRelease, err = keys.Parse(*newRel, b); err != nil {
			return err
		}
	}
	readPub := func(p string) ([]byte, error) {
		if p == "" {
			return nil, nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		return rotation.ParsePublic(p, b)
	}
	if o.NewRecovery, err = readPub(*newRec); err != nil {
		return err
	}
	given, err := readPub(*recPub)
	if err != nil {
		return err
	}
	if err := rotation.RefuseOutOfService(s.OutOfService); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if o.Nodes, err = rotation.Targets(ctx, *dataDir, s.Nodes, &fleet.Client{}); err != nil {
		return err
	}
	if o.CurrentRecovery, err = rotation.CurrentRecovery(*dataDir, given, o.NewRecovery, o.Nodes); err != nil {
		return err
	}
	dir, p, err := rotation.Prepare(o)
	if err != nil {
		return err
	}
	printPlan(w, p)
	fmt.Fprintf(w, "prepared in %s (nothing sent to a node)\n", dir)
	if p.NewReleaseKey {
		fmt.Fprintf(w, "the new release key %s is in %s (mode 0600); the controller signs with it for each node once it has its new keys\n",
			p.New.Release, keys.NextPath(*dataDir))
	}
	fmt.Fprintf(w, "next: sign it offline with the recovery key %s, from the controller's directory:\n  %s\n", p.Old.Recovery, rotation.SignCommand(p.ID))
	fmt.Fprintf(w, "then: espdns key rotate push -data %s -dir %s (or Rotate keys in the account panel)\n", *dataDir, p.ID)
	return nil
}

// printPlan says a rotation for a person.
func printPlan(w io.Writer, p *rotation.Plan) {
	retired := "the old release key is kept as each node's retired key (what they store still loads until pushed again)"
	switch {
	case p.Compromised:
		retired = "COMPROMISED: the old release key is dropped at once (not kept as a retired key)"
	case p.Old.Release == p.New.Release:
		retired = "the release key stays"
	}
	fmt.Fprintf(w, "rotation %s: release %s -> %s, recovery %s -> %s; %s\n", p.ID, p.Old.Release, p.New.Release, p.Old.Recovery, p.New.Recovery, retired)
	for _, n := range p.Nodes {
		fmt.Fprintf(w, "  %s (node %s, %s): KEYS seq %d, %s\n", n.Addr, n.NodeID, n.Image, n.Seq, n.State)
		if len(n.Repush) > 0 {
			fmt.Fprintf(w, "    signed again with the new key: %s\n", strings.Join(n.Repush, "; "))
		}
		for _, m := range n.Missing {
			fmt.Fprintf(w, "    NOT signed again (the controller has no copy): %s\n", m)
		}
		if n.Error != "" {
			fmt.Fprintf(w, "    stopped here: %s\n", n.Error)
		}
	}
}

func rotateSign(args []string, in io.Reader, w io.Writer) error {
	fl := flag.NewFlagSet("key rotate sign", flag.ExitOnError)
	dir := fl.String("dir", "", "the rotation's directory (what prepare wrote)")
	key := fl.String("key", keyStdin, "the recovery private key: - reads it from standard input; or a file. PKCS#8 or SEC1 PEM, or base64 of either")
	fl.Parse(args)
	if *dir == "" {
		return errors.New("need -dir: the rotation's directory")
	}
	var b []byte
	var err error
	name := *key
	if *key == keyStdin {
		name = "-key - (standard input)"
		b, err = io.ReadAll(io.LimitReader(in, 64<<10))
	} else {
		b, err = os.ReadFile(*key)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	k, err := keys.Parse(name, b)
	clear(b)
	if err != nil {
		return err
	}
	lines, err := rotation.Sign(*dir, k)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "signed: push it from the controller (espdns key rotate push, or Rotate keys in the account panel)")
	return nil
}

func rotatePush(args []string) (err error) {
	fl := flag.NewFlagSet("key rotate push", flag.ExitOnError)
	dataDir := dataFlag(fl)
	settingsFile := fl.String("settings", "", "the settings file (default: settings.json in -data)")
	dirFlag := fl.String("dir", "", "the rotation: its directory, or its ID in <data>/keys/rotation")
	healthy := fl.Duration("healthy", 2*time.Minute, "how long each node has to be in service again after its keys changed")
	var only listFlag
	fl.Var(&only, "node", "push only this node (its address; repeat for more): the others are left for a later push")
	fl.Parse(args)
	if *dirFlag == "" {
		return errors.New("need -dir: the rotation")
	}
	dir, err := rotation.Find(*dataDir, *dirFlag)
	if err != nil {
		return err
	}
	s, err := settings.Load(settingsPath(*settingsFile, *dataDir))
	if err != nil {
		return err
	}
	cur, err := keys.FileSource{Path: keys.Path(*dataDir)}.Key()
	if err != nil {
		return err
	}
	next, err := keys.ReadNext(keys.NextPath(*dataDir))
	if err != nil {
		return err
	}
	ctx, end, err := begin(*dataDir, "key rotate push", args, false)
	if err != nil {
		return err
	}
	defer func() { end(err) }()
	p, err := rotation.Push(ctx, rotation.PushOptions{DataDir: *dataDir, Dir: dir, Listed: s.Nodes, Current: cur, Next: next,
		Client: &fleet.Client{Logf: log.Printf}, Logf: log.Printf, Healthy: *healthy, Only: only})
	if p != nil {
		printPlan(os.Stdout, p)
	}
	return err
}

func rotateDiscard(args []string, w io.Writer) error {
	fl := flag.NewFlagSet("key rotate discard", flag.ExitOnError)
	dataDir := dataFlag(fl)
	dirFlag := fl.String("dir", "", "the rotation: its directory, or its ID in <data>/keys/rotation")
	fl.Parse(args)
	if *dirFlag == "" {
		return errors.New("need -dir: the rotation")
	}
	dir, err := rotation.Find(*dataDir, *dirFlag)
	if err != nil {
		return err
	}
	if err := rotation.Discard(*dataDir, dir); err != nil {
		return err
	}
	fmt.Fprintf(w, "discarded %s\n", dir)
	return nil
}
