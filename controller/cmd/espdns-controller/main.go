// espdns-controller: the espDNS management dashboard. It finds nodes, shows their state,
// flashes new ones from the browser and runs fleet jobs, one at a time. All state lives in
// the data directory (in Docker, the /data volume mounted from the host), which the espdns
// CLI's fleet targets share. All of it is the controller's user's only: directories 0700,
// files 0600, whatever the umask (internal/secfile); at start, anything in it open to group
// or others (a data directory from before) is made so.
//
//	settings.json            deployment settings (internal/settings), read by the CLI too
//	fleet.lock               the fleet lock (internal/fleetlock): one fleet change at a time
//	log/actions.jsonl        the action log (internal/actionlog): every fleet change
//	log/jobs/<id>.json       each job's record and progress (internal/jobs)
//	firmware/images/<image>/ chip images: imported releases (espdns release import, fetch; Get firmware), each with
//	                         its provenance/ (internal/dist), or your own export (make export-images in firmware/)
//	firmware/latest          the newest release version the forge lists (internal/updates)
//	firmware/builds/<board>/ the production boards' builds, from make fleet-firmware (the rolling push)
//	lists/                   blocklist and overrides files (the rolling push; the Blocking page compiles them)
//	blocking/lists.json      the list definitions (internal/blocking): what each list is compiled from
//	blocking/sources/        local list sources, allow lists and the overrides, edited on the Blocking page
//	zones/<zone>.zone        hosted zones (internal/zonefiles): the zone editor's and the rolling push's
//	boards/                  your own board definitions (the shipped catalog is -catalog)
//	keys/release.pem         the release signing key, mode 0600 (internal/keys; espdns key import)
//	configs/<name>.json      node configs (internal/configs): the editor's and the CLI's config rollouts
//	changes/                 the pending changes (internal/changes): edits waiting for one apply
//	auth.json                the login: the user and an argon2id hash, mode 0600 (espdns passwd)
//
// The nodes don't depend on it: they keep answering DNS when it is off.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/dist"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/observe"
	"github.com/skitzo2000/espdns/controller/internal/rolling"
	"github.com/skitzo2000/espdns/controller/internal/settings"
	"github.com/skitzo2000/espdns/controller/internal/version"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

// loopbackListen says whether a listen address is on loopback only.
func loopbackListen(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func main() {
	// Localhost: the dashboard is plain HTTP (its login cookie can't be Secure), and browsers
	// only allow USB flashing (Web Serial) on localhost or HTTPS.
	listen := flag.String("listen", "127.0.0.1:8480", "address to serve the dashboard on")
	dataDir := flag.String("data", "data", "data directory")
	catalog := flag.String("catalog", "../boards", "the shipped board catalog")
	browse := flag.Bool("browse-mdns", true, "browse mDNS for nodes besides settings.json's (false: only those are polled; the browser tests, which must reach no node)")
	mdns := flag.Duration("check-mdns", 3*time.Second, "how long a check job browses mDNS for nodes besides the settings' (0: don't)")
	health := flag.Bool("healthcheck", false, "only check that the controller on -listen answers (exit 0) or not (exit 1): the container's health check")
	projectPub := flag.String("project-pub", dist.ProjectPub, "the project's release public key (keys/project.pub): releases signed with it, or with your release key, are imported")
	releaseURL := flag.String("release-url", dist.DefaultForge, "the repository Get firmware fetches releases from when settings.json has no \"release_url\"")
	flag.Parse()
	rolling.ProjectPub = *projectPub
	if *health {
		if err := healthcheck(*listen, 5*time.Second); err != nil {
			log.Printf("healthcheck: %v", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if !loopbackListen(*listen) {
		log.Fatalf("-listen %s: the controller listens on localhost only (127.0.0.1, [::1] or localhost)", *listen)
	}

	log.Printf("espdns-controller %s", version.Version)
	if err := prepareData(*dataDir, log.Printf); err != nil {
		log.Fatalf("%v: the controller stores everything there, so it doesn't start (docker compose: the directory must exist and be owned by ESPDNS_UID:ESPDNS_GID, .env)", err)
	}
	st, err := loadSettings(*dataDir, log.Printf)
	if err != nil {
		log.Fatalf("settings: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reg := nodes.New(st.Nodes)
	reg.SetOutOfService(st.OutOfService)
	relist := func() {
		if s, err := settings.Load(settings.Path(*dataDir)); err == nil {
			reg.SetListed(s.Nodes)
			reg.SetOutOfService(s.OutOfService)
		}
	}
	// The dashboard and the query log (internal/observe): after each poll of /status, each
	// node that answered has its /metrics and new query log entries read, in memory only. A
	// poll that comes while the last read still runs is skipped.
	obs := observe.New(&fleet.Client{})
	polled := make(chan []nodes.Node, 1)
	reg.OnPoll(func(list []nodes.Node) {
		select {
		case polled <- list:
		default:
		}
	})
	// A node not seen before (its ID and address) has the newest release asked for (newRelease).
	newNode := make(chan struct{}, 1)
	seen := map[string]bool{}
	reg.OnPoll(func(list []nodes.Node) {
		for _, n := range list {
			if k := n.ID + " " + n.Addr; n.Online && !seen[k] {
				seen[k] = true
				select {
				case newNode <- struct{}{}:
				default:
				}
			}
		}
	})
	go obs.Run(ctx, polled)
	go reg.Run(ctx, *browse)
	// settings.json read again every 10 s: a node an adoption added (or one added by hand) is
	// polled without a restart. A file that doesn't parse leaves the list as it was.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			relist()
		}
	}()

	// The release key (internal/keys): a file in the data directory for now. Read again at
	// every action, so a key imported while the controller runs is used from then on.
	keySrc := keys.Source(keys.FileSource{Path: keys.Path(*dataDir)})
	if k, err := keySrc.Key(); err != nil {
		log.Printf("release key: %v: the controller only reads nodes until one is imported", err)
	} else {
		log.Printf("release key: %s, fingerprint %s", keySrc, keys.Fingerprint(k))
	}
	login := auth.New(auth.Path(*dataDir))
	if set, err := login.State(); !set {
		log.Printf("login: no password set (the first run in the browser, or espdns passwd): read-only until one is")
	} else if err != nil {
		log.Printf("login: %v: nobody can log in until it is fixed", err)
	}

	// The job kinds: check (read-only), and the node actions (actions.go).
	newClient := func() *fleet.Client { return &fleet.Client{} }
	act := actions{dataDir: *dataDir, key: keySrc, client: newClient, known: func() []string {
		var out []string
		for _, n := range reg.List() {
			out = append(out, n.Addr)
		}
		if s, err := settings.Load(settings.Path(*dataDir)); err == nil {
			out = append(out, s.Nodes...)
		}
		return out
	}}
	kinds := act.kinds()
	kinds["check"] = checker{dataDir: *dataDir, mdns: *mdns, client: newClient}.kind
	// Compiling a list (blocking.go): blocklist.Run, as espdns blocklist.
	kinds["blocklist"] = compileKind{dataDir: *dataDir}.kind
	// A config push (configs.go) names the dry run before it: the runner's job.
	var runner *jobs.Runner
	kinds["config-push"] = configPush{actions: act, catalog: *catalog, secret: newSecret(),
		job: func(id string) (jobs.Job, bool) { return runner.Get(id) }}.kind
	// The rolling push (push.go), which names its dry run too.
	push := pushKind{actions: act, catalog: *catalog, secret: newSecret(), nodes: reg.List, used: &usedRuns{},
		job: func(id string) (jobs.Job, bool) { return runner.Get(id) }}
	kinds["rollout"] = push.kind
	// Adoption (adopt.go).
	adopt := adoptKind{actions: act, catalog: *catalog, secret: newSecret(), nodes: reg.List, used: &usedRuns{},
		job: func(id string) (jobs.Job, bool) { return runner.Get(id) }}
	kinds["adopt"] = adopt.kind
	kinds["settings-add"] = adopt.settingsAdd
	// The pending changes and the apply (changes.go, apply.go): every change sent as one
	// rolling change.
	pending := changes.New(*dataDir)
	// Get firmware (firmware.go): a published release fetched, checked and imported; the
	// apply gets an update's chip image so.
	fetch := fetchKind{dataDir: *dataDir, forge: *releaseURL, catalog: *catalog, nodes: reg.List}
	kinds["fetch-release"] = fetch.kind
	ak := applyKind{actions: act, catalog: *catalog, store: pending, images: fetch}
	kinds["apply"] = ak.kind
	// The scheduled blocklist refresh (refresh.go): a downloaded list that changed, sent as
	// an apply of it alone.
	kinds["refresh"] = refreshKind{apply: ak}.kind
	// A node taken out of service, returned to it, or removed from the group (service.go).
	for name, k := range (serviceKind{apply: ak, relist: relist, jobs: func() []jobs.Job { return runner.List() }}).kinds() {
		kinds[name] = k
	}
	log.Printf("project key: %s", projectPubThere(*projectPub))
	runner = jobs.New(*dataDir, kinds)
	jobsDone := make(chan struct{})
	go func() { runner.Run(ctx); close(jobsDone) }()
	// The newest release, asked for at start, every day and when a new node shows up: an
	// update to it is offered with nothing downloaded.
	go func() {
		for {
			newRelease(ctx, fetch, log.Printf)
			select {
			case <-ctx.Done():
				return
			case <-newNode:
			case <-time.After(releaseEvery):
			}
		}
	}()
	refresh := newRefresher(*dataDir, pending, runner)
	go refresh.Run(ctx, refreshFirst+time.Duration(rand.Int64N(int64(refreshFirst))), refreshTick)

	srv := &server{dataDir: *dataDir, releaseURL: *releaseURL, nodes: reg.List, runner: runner, auth: login, key: keySrc, catalog: *catalog, push: push, adopt: adopt, observe: obs, changes: pending, listed: func([]string) { relist() }, lookup: reg.Lookup, refresh: refresh,
		builder: newBuilder(*catalog, filepath.Join(*dataDir, "boards"), filepath.Join(*dataDir, "firmware", "images"))}
	h, _ := srv.handler()

	hs := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		<-jobsDone // a running job is stopped and recorded first
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()
	log.Printf("dashboard on http://%s (data %s; settings %s: %d node(s), %d DNS peer(s))", *listen, *dataDir,
		settings.Path(*dataDir), len(st.Nodes), len(st.DNSPeers))
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// loadSettings reads settings.json at startup, and clears what an older controller left
// for a zone primary's API: a "primary" with an API's fields ("kind", "url",
// "cert_sha256") loads, and is said once to be no longer used; a token file
// (keys/primary.token) is deleted, saying to revoke the token on the primary. The
// controller no longer talks to a primary's API, so it keeps no token.
func loadSettings(dataDir string, logf func(format string, args ...any)) (settings.Settings, error) {
	removed, rerr := keys.RemoveOldTokens(dataDir)
	for _, rel := range removed {
		logf("removed %s, the unused zone primary API token: revoke that token on your zone primary", rel)
	}
	if rerr != nil {
		logf("warning: the unused zone primary API token: %v: delete it by hand, and revoke it on your zone primary", rerr)
	}
	st, err := settings.Load(settings.Path(dataDir))
	if err != nil {
		return st, err
	}
	if old := st.ZonePrimary().Ignored(); len(old) > 0 {
		logf(`settings.json: "primary" fields %s are no longer used and are ignored: the nodes copy zones by zone transfer, `+
			`and your primary allows them once (Zones, Zone primary)`, strings.Join(old, ", "))
	}
	return st, nil
}
