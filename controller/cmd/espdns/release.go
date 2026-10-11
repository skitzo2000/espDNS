package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/dist"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// espdns release build -version 0.0.1 -images DIR -catalog DIR -out DIR [-include FILE ...]
// espdns release sign -dir DIR -pub release.pub          (the release key on standard input)
// espdns release verify -dir DIR -pub release.pub
// espdns release import -dir DIR -data /data [-pub /keys/project.pub]
// espdns release fetch -data /data [-version v0.0.3] [-url https://github.com/skitzo2000/espdns] [-pub /keys/project.pub]
//
// A release's files (internal/dist, docs/releasing.md). build makes them from the exported
// chip images, key-neutral, with SHA256SUMS, as CI does on a version tag (no factory images:
// every catalog board's chip image must be there, for Install over USB); it uses no key. sign signs SHA256SUMS with a release key, read from standard input (the PEM, or
// base64 of it), never an argument or the environment; only a key that is -pub is taken.
// verify checks the signature with -pub and every file against SHA256SUMS. import checks
// that SHA256SUMS is signed by the project's release key (-pub, /keys/project.pub in the
// controller's image) or by your own (the controller's, in -data), refusing any other, and
// every file, then puts the release's chip images in the data directory (firmware/images),
// each with its provenance, where the builder and the Nodes page's updates find them. fetch
// downloads a published release's chip images (from GitHub, or a mirror: -url, else
// settings.json's "release_url"), checked with the project's key alone, and imports them.
func cmdRelease(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) < 1 {
		return errors.New("usage: espdns release build|sign|verify|import|fetch [flags]")
	}
	sub, args := args[0], args[1:]
	fl := flag.NewFlagSet("release "+sub, flag.ContinueOnError)
	switch sub {
	case "build":
		o := dist.Options{}
		fl.StringVar(&o.Version, "version", "", "the version released, the repository's VERSION (required): every chip image must be it")
		fl.StringVar(&o.Images, "images", "", "the exported chip images, <dir>/<image>/image.json (required)")
		fl.StringVar(&o.Catalog, "catalog", "", "the board catalog (boards/; /catalog in the controller's image): every board's chip image must be in -images (required)")
		fl.StringVar(&o.Out, "out", "", "where the release's files go, missing or empty (required)")
		var inc listFlag
		fl.Var(&inc, "include", "another file the release carries and SHA256SUMS lists (repeatable)")
		if err := parseFlags(fl, args); err != nil {
			return err
		}
		if o.Version == "" || o.Images == "" || o.Catalog == "" || o.Out == "" {
			return errors.New("release build: -version, -images, -catalog and -out are required")
		}
		o.Include = inc
		files, err := dist.Build(o)
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Fprintln(stdout, filepath.Join(o.Out, f))
		}
		fmt.Fprintf(stdout, "%d files; not signed: espdns release sign\n", len(files))
		return nil
	case "import", "fetch":
		return releaseImport(sub, fl, args, stdout)
	case "sign", "verify":
	default:
		return fmt.Errorf("unknown release command %q (build, sign, verify, import or fetch)", sub)
	}

	dir := fl.String("dir", "", "the release's files, with its SHA256SUMS (required)")
	pubFile := fl.String("pub", "", "the public key of the release key that signs it, raw (keys/project.pub for the project's; /keys/project.pub in the controller's image) (required)")
	if err := parseFlags(fl, args); err != nil {
		return err
	}
	if *dir == "" || *pubFile == "" {
		return fmt.Errorf("release %s: -dir and -pub are required", sub)
	}
	pub, err := os.ReadFile(*pubFile)
	if err != nil {
		return err
	}
	if len(pub) != 65 || pub[0] != 4 {
		return fmt.Errorf("%s: %d bytes, not a raw P-256 public key (65)", *pubFile, len(pub))
	}
	fp := release.Fingerprint(pub)

	switch sub {
	case "sign":
		if f, ok := stdin.(*os.File); ok {
			if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				return errors.New("release sign reads the release key from standard input, piped (< release.pem), not typed")
			}
		}
		k, err := readKey("the release key (standard input)", stdin)
		if err != nil {
			return err
		}
		// The whole public key, not its fingerprint (8 bytes of its hash) alone
		if got := release.PublicRaw(k); !bytes.Equal(got, pub) {
			return fmt.Errorf("the key on standard input is %s, not the release key in %s (%s): nodes and users check with that one",
				release.Fingerprint(got), *pubFile, fp)
		}
		if err := dist.Sign(*dir, k); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s: signed with the release key %s\n", filepath.Join(*dir, dist.SigFile), fp)
		return nil
	default: // verify
		c, err := dist.Verify(*dir, pub)
		if err != nil {
			return err
		}
		report(stdout, c, fp)
		return nil
	}
}

// releaseImport is release import and release fetch: the release is taken if the project's
// release key (-pub) or yours (the controller's, in -data) signed it (fetch: the project's
// only), and its chip images go into the data directory with their provenance.
func releaseImport(sub string, fl *flag.FlagSet, args []string, stdout io.Writer) error {
	data := fl.String("data", "", "the controller's data directory (/data in Docker) (required)")
	pubFile := fl.String("pub", dist.ProjectPub, "the project's release public key, raw (keys/project.pub at the top of the repository)")
	dir, ver, forge := "", "", ""
	if sub == "import" {
		fl.StringVar(&dir, "dir", "", "the release's files, with its SHA256SUMS and SHA256SUMS.sig (required)")
	} else {
		fl.StringVar(&ver, "version", "", "the release, v0.0.3 (default: the newest published)")
		fl.StringVar(&forge, "url", "", "the repository the release is published on (default: settings.json's \"release_url\", else "+dist.DefaultForge+")")
	}
	if err := parseFlags(fl, args); err != nil {
		return err
	}
	if *data == "" || (sub == "import" && dir == "") {
		return fmt.Errorf("release %s: -data is required%s", sub, map[bool]string{true: ", and -dir"}[sub == "import"])
	}
	signers, err := dist.LoadSigners(*pubFile, *data)
	if err != nil {
		return err
	}
	if *pubFile != dist.ProjectPub && signers.Project == nil {
		return fmt.Errorf("-pub %s: no such file", *pubFile)
	}
	if forge == "" && sub == "fetch" {
		s, err := settings.Load(settings.Path(*data))
		if err != nil {
			return err
		}
		forge = s.ReleaseURL
	}
	// Under the fleet lock, as every change to the data directory: never under a rollout
	// or a job reading the chip images
	what := []string{"-dir", dir}
	if sub == "fetch" {
		what = []string{"-version", ver, "-url", forge}
	}
	_, end, err := begin(*data, "release "+sub, what, false)
	if err != nil {
		return err
	}
	images := filepath.Join(*data, "firmware", "images")
	var imp dist.Imported
	if sub == "import" {
		imp, err = dist.Import(dir, signers, images)
	} else {
		imp, err = dist.Fetch(context.Background(), dist.FetchOptions{Forge: forge, Version: ver, ProjectPub: signers.Project,
			Logf: func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) }}, filepath.Join(*data, "firmware"), images)
	}
	end(err)
	if err != nil {
		return err
	}
	who := map[string]string{dist.SignerProject: "the project's release key", dist.SignerOwn: "your release key"}[imp.Signer.Who]
	fmt.Fprintf(stdout, "version %s, signed by %s %s; chip images imported into %s: %s\n", imp.Signer.Version, who,
		imp.Signer.Fingerprint, images, strings.Join(imp.Images, ", "))
	return nil
}

func report(w io.Writer, c dist.Checked, fp string) {
	fmt.Fprintf(w, "%s: signed by the release key %s\n", dist.SumsFile, fp)
	for _, f := range c.Files {
		fmt.Fprintf(w, "%s: OK\n", f)
	}
	for _, f := range c.Unlisted {
		fmt.Fprintf(w, "%s: not in %s, not part of the release\n", f, dist.SumsFile)
	}
	fmt.Fprintf(w, "%d files as signed\n", len(c.Files))
}

// parseFlags parses the flags and refuses arguments besides them; -h prints them and exits.
func parseFlags(fl *flag.FlagSet, args []string) error {
	if err := fl.Parse(args); errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	} else if err != nil {
		return err
	}
	if fl.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fl.Args())
	}
	return nil
}
