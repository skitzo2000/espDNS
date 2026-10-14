package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"slices"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// espdns key generate -data /data
// espdns key import -data /data [-key -] [-pub release.pub] [-recovery-pub recovery.pub] [-mdns 0] [-replace]
// espdns key status -data /data [-pub ...] [-mdns 0]
//
// The controller's release key: <data>/keys/release.pem, mode 0600 (internal/keys).
// generate makes a new one there, for a new fleet: never over a key there, and never while
// settings.json lists a node (it trusts another key: import that one). import reads one from
// standard input (the PEM, or base64 of it), never an argument or the environment, and
// writes it only if the fleet trusts it: the firmware's built-in release key (-pub), or a node
// in settings.json listing it in slot 0 of its /status "keys" (a node found only over mDNS
// never vouches: any host can advertise one); with no node listed yet, any key but the
// recovery key (a new fleet's first: keys.Trust). The recovery key is refused: -recovery-pub,
// by default <data>/keys/recovery.pub (the first run's), is needed for that once a node is
// listed, so the check never rests on which nodes happen to answer. status says whether the
// key is there and usable, and who trusts it. None of them changes a node: they only read
// /status.
func cmdKey(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: espdns key generate|import|status|rotate -data <dir> [flags]")
	}
	sub, args := args[0], args[1:]
	if sub == "generate" {
		return keyGenerate(args)
	}
	if sub == "rotate" {
		return keyRotate(args)
	}
	if sub != "import" && sub != "status" {
		return fmt.Errorf("unknown key command %q (generate, import, status or rotate)", sub)
	}
	fl := flag.NewFlagSet("key "+sub, flag.ExitOnError)
	dataDir := dataFlag(fl)
	settingsFile := fl.String("settings", "", "the settings file whose nodes are asked for the keys they trust (default: settings.json in -data)")
	mdns := fl.Duration("mdns", 0, "browse mDNS this long for nodes besides the settings' (0: don't); a node found only so is reported, and refuses the recovery key, but never vouches for a key")
	pub := fl.String("pub", "", "your release public key, raw (e.g. your firmware/keys/release.pub): a key that is it is trusted")
	recPub := fl.String("recovery-pub", "", "the recovery public key, raw (default: <data>/keys/recovery.pub, if there): that key is never imported; import needs it once a node is listed")
	in := keyStdin
	replace := false
	if sub == "import" {
		fl.StringVar(&in, "key", keyStdin, "the key: - reads it from standard input (PEM, or base64 of it); or a PEM file")
		fl.BoolVar(&replace, "replace", false, "replace a different key imported before")
	}
	fl.Parse(args)
	path := keys.Path(*dataDir)
	if *recPub == "" {
		if _, err := os.Stat(keys.RecoveryPath(*dataDir)); err == nil {
			*recPub = keys.RecoveryPath(*dataDir)
		}
	}
	if sub == "import" && *recPub == "" {
		s, err := settings.Load(settingsPath(*settingsFile, *dataDir))
		if err != nil {
			return err
		}
		if len(s.Nodes) > 0 {
			return errors.New("key import: -recovery-pub recovery.pub is needed (the recovery public key your nodes trust), so the recovery key is refused whatever the nodes say")
		}
	}

	var k *ecdsa.PrivateKey
	var err error
	switch {
	case sub == "status":
		k, err = keys.FileSource{Path: path}.Key()
		if err != nil {
			return err
		}
		fmt.Printf("%s: mode 0600, owned by this user, a P-256 key\n", path)
	case in == keyStdin:
		k, err = readKey("-key - (standard input)", os.Stdin)
	default:
		var b []byte
		if b, err = os.ReadFile(in); err == nil {
			k, err = keys.Parse(in, b)
		}
	}
	if err != nil {
		return err
	}

	r, err := trustCheck(k, settingsPath(*settingsFile, *dataDir), *mdns, *pub, *recPub)
	for _, l := range r.Lines() {
		fmt.Println(l)
	}
	if err != nil {
		return err
	}
	if sub == "status" {
		return nil
	}
	if fi, err := os.Stat(*dataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("-data %s: not a directory (make it before you start the controller: docs/getting-started.md)", *dataDir)
	}
	same, err := keys.Import(path, k, replace)
	if err != nil {
		return err
	}
	if same {
		fmt.Printf("%s: already this key; nothing changed\n", path)
	} else {
		fmt.Printf("imported to %s (mode 0600): the controller signs with it from its next action\n", path)
	}
	return nil
}

// keyGenerate is espdns key generate: a new release key in <data>/keys/release.pem, for a
// new fleet (the first run's Make your keys does the same from the browser).
func keyGenerate(args []string) error {
	fl := flag.NewFlagSet("key generate", flag.ExitOnError)
	dataDir := dataFlag(fl)
	settingsFile := fl.String("settings", "", "the settings file (default: settings.json in -data): with a node listed, no key is made")
	fl.Parse(args)
	if fi, err := os.Stat(*dataDir); err != nil || !fi.IsDir() {
		return fmt.Errorf("-data %s: not a directory (make it before you start the controller: docs/getting-started.md)", *dataDir)
	}
	s, err := settings.Load(settingsPath(*settingsFile, *dataDir))
	if err != nil {
		return err
	}
	if len(s.Nodes) > 0 {
		return fmt.Errorf("key generate: settings.json lists %d node(s), which trust a release key already: import that one (espdns key import); a new one would sign releases they all refuse", len(s.Nodes))
	}
	k, err := keys.Generate(keys.Path(*dataDir))
	if err != nil {
		return err
	}
	fmt.Printf("made %s (mode 0600): release key fingerprint %s\n", keys.Path(*dataDir), keys.Fingerprint(k))
	if _, err := keys.ReadRecovery(*dataDir); err != nil {
		fmt.Println("next: the recovery key, made in your browser at the first run (Make your keys)")
	}
	return nil
}

// trustCheck asks the nodes (the settings' and mDNS's) which keys they trust, and checks k
// against them and the public key files.
func trustCheck(k *ecdsa.PrivateKey, settingsFile string, mdns time.Duration, pubFile, recFile string) (keys.TrustReport, error) {
	readPub := func(p string) ([]byte, error) {
		if p == "" {
			return nil, nil
		}
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("%s: no such file", p)
		}
		return b, err
	}
	pub, err := readPub(pubFile)
	if err != nil {
		return keys.TrustReport{}, err
	}
	rec, err := readPub(recFile)
	if err != nil {
		return keys.TrustReport{}, err
	}
	s, err := settings.Load(settingsFile)
	if err != nil {
		return keys.TrustReport{}, err
	}
	c := &fleet.Client{Logf: log.Printf}
	ctx, cancel := context.WithTimeout(context.Background(), mdns+30*time.Second)
	defer cancel()
	ns, err := c.Discover(ctx, mdns, s.Nodes)
	if err != nil && mdns > 0 {
		log.Printf("%v: going on with the listed nodes only", err)
		ns, err = c.Discover(ctx, 0, s.Nodes)
	}
	if err != nil {
		return keys.TrustReport{}, err
	}
	var nk []keys.NodeKeys
	for _, n := range ns {
		x := keys.NodeKeys{Addr: n.Addr, Listed: slices.Contains(s.Nodes, n.Addr)}
		if n.Status != nil {
			x.Keys = n.Status.Keys
		} else {
			x.Error = n.Error
		}
		nk = append(nk, x)
	}
	return keys.Trust(k, pubFile, pub, rec, nk)
}
