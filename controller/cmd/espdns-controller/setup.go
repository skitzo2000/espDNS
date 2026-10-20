package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/blocking"
	"github.com/skitzo2000/espdns/controller/internal/changes"
	"github.com/skitzo2000/espdns/controller/internal/configs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
	"github.com/skitzo2000/espdns/controller/internal/primary"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
	"github.com/skitzo2000/espdns/controller/internal/settings"
	"github.com/skitzo2000/espdns/controller/internal/zonefiles"
)

// The first run and the controller's settings from the browser (easy to stand up,
// no hand-edited config files). The first run's password is
// auth's (POST /api/setup/password: only while none is set, only from this machine); the
// rest is here:
//
//	GET  /api/setup      what is set up: the password, settings.json, the keys (the release
//	                     key, the recovery public key), the zone primary's address, the
//	                     nodes, the zones of your own (hosted here, copied, forwarded);
//	                     first_run while there is neither a password nor settings.json; the
//	                     first run's steps, and "done" once every one is
//	POST /api/setup/step {"step": "keys" | "zones" | "router" | "blocking"}: a step the
//	                     browser can't see done (recovery.pem saved offline, the router told,
//	                     blocking chosen) or one skipped (zones, blocking): marked done in
//	                     setup.json. keys only once both keys are there.
//	GET  /api/settings   settings.json as the form edits it (every field, empty ones too), its
//	                     version, and the zone primary as the Zones page shows it
//	POST /api/settings   {"settings": {...}, "version"}: settings.json replaced whole if still
//	                     the version read; the form again
//
// The GETs are open as the other reads are while no password is set (the first run reads
// them before one is); with a password they need a login. The POST needs a login. Nothing
// here goes to a node: settings.json is the controller's and the CLI's, and a change applies
// at once (the nodes listed are polled from the next poll; a job or the CLI reads the file
// when it starts), with no restart and no pending change.

type setupServer struct {
	dataDir string
	auth    *auth.Auth
	key     keys.Source
	// changes is the pending changes: a zone added in the first run waits there until Apply
	// and counts as set up meanwhile (nil: the data directory's).
	changes *changes.Store
	// listed, if set, hears settings.json's nodes after a save, so they are polled at once
	// (otherwise within the 10 s the controller reads the file again).
	listed func([]string)
	// saveIf, if set, stands in for settings.SaveIf (a test fails the write with it).
	saveIf func(path, version string, s settings.Settings, before, after func() error) error
}

func (s setupServer) routes(mux *routes) {
	mux.HandleFunc("GET /api/setup", s.setup)
	mux.HandleFunc("POST /api/setup/step", needLogin("the first run's steps need", s.mark))
	mux.HandleFunc("GET /api/settings", s.get)
	mux.HandleFunc("POST /api/settings", needLogin("changing the settings needs", s.save))
}

// A first-run step, in the order the browser walks them; done when what it sets up is
// there. Optional ones may be skipped.
type setupStep struct {
	Step     string `json:"step"`
	Done     bool   `json:"done"`
	Optional bool   `json:"optional,omitempty"`
}

func (s setupServer) setup(w http.ResponseWriter, r *http.Request) {
	pwSet, pwErr := s.auth.State()
	st, _, exists, err := settings.Read(settings.Path(s.dataDir))
	_, keyErr := s.key.Key()
	_, recErr := keys.ReadRecovery(s.dataDir)
	marked := readMarks(s.dataDir)
	out := map[string]any{
		"first_run":      !pwSet && !exists,
		"password_set":   pwSet,
		"settings_saved": exists,
		"release_key":    keyErr == nil,
		"recovery_key":   recErr == nil,
		"keys_saved":     marked["keys"],
		"primary":        st.ZonePrimary().Address,
		"nodes":          len(st.Nodes),
	}
	defs, _, _ := blocking.Load(s.dataDir)
	nz := s.configuredZones()
	steps := []setupStep{
		{Step: "password", Done: pwSet && pwErr == nil},
		// Make your keys: the release key (made here or imported), the recovery key's public
		// half (made in the browser), and recovery.pem saved somewhere safe offline.
		{Step: "keys", Done: keyErr == nil && recErr == nil && marked["keys"]},
		{Step: "first_node", Done: len(st.Nodes) >= 1},
		{Step: "second_node", Done: len(st.Nodes) >= 2},
		// Add your zone: one of your own (hosted here, copied from a DNS server you run, or
		// forwarded), or skipped.
		{Step: "zones", Done: nz > 0 || marked["zones"], Optional: true},
		{Step: "router", Done: marked["router"]},
		{Step: "blocking", Done: len(defs.Lists) > 0 || marked["blocking"], Optional: true},
	}
	done := true
	for _, x := range steps {
		done = done && x.Done
	}
	out["steps"], out["done"], out["blocklists"], out["zones"] = steps, done, len(defs.Lists), nz
	if pwErr != nil {
		out["password_error"] = pwErr.Error()
	}
	if err != nil {
		out["settings_error"] = err.Error()
	}
	writeJSON(w, out)
}

// The steps the browser marks done (POST /api/setup/step): the ones the controller can't
// see done itself, or that were skipped. Kept in <data>/setup.json, {"done": [...]}. A step
// no longer in the list (an older setup.json's "primary") is read over, and dropped at the
// next write.
var markable = []string{"keys", "zones", "router", "blocking"}

// configuredZones counts the zones of your own, as the Zones page lists them with what
// waits to apply: the hosted zones (a zone file, or one added as a pending change and not
// applied yet; one whose deletion waits is out), and the secondary and forward zones the
// node configs hold as the pending changes make them. The zones a node serves from its
// firmware's defaults, listed in no config, aren't yours and aren't counted. A name in two
// kinds counts twice, as the Zones page shows it. A file that can't be read counts for
// nothing.
func (s setupServer) configuredZones() int {
	store := s.changes
	if store == nil {
		store = changes.New(s.dataDir)
	}
	seen := map[string]bool{}
	files, _ := zonefiles.List(s.dataDir)
	for _, f := range files {
		seen["hosted "+f.Zone] = true
	}
	pending, _ := store.List()
	for _, c := range pending {
		if c.Kind != changes.Zone {
			continue
		}
		if c.Delete {
			delete(seen, "hosted "+zonefiles.ZoneOf(c.Name))
		} else {
			seen["hosted "+zonefiles.ZoneOf(c.Name)] = true
		}
	}
	cfgs, _ := configs.List(s.dataDir)
	for _, f := range cfgs {
		text := f.Text
		if v, err := store.Effective(changes.Config, f.Name); err == nil {
			if !v.Exists {
				continue
			}
			text = v.Text
		}
		var c nodecfg.Config
		if json.Unmarshal(text, &c) != nil {
			continue
		}
		if c.Secondary != nil && c.Secondary.Zones != nil {
			for _, z := range *c.Secondary.Zones {
				seen["secondary "+zoneKey(z)] = true
			}
		}
		if c.ForwardZones != nil {
			for _, z := range *c.ForwardZones {
				seen["forward "+zoneKey(z.Zone)] = true
			}
		}
	}
	return len(seen)
}

// zoneKey is a zone name as one: lower case, without the final dot.
func zoneKey(z string) string { return strings.ToLower(strings.TrimSuffix(z, ".")) }

func marksPath(dataDir string) string { return filepath.Join(dataDir, "setup.json") }

// readMarks is setup.json's steps; none when it isn't there or can't be read (the steps
// are then shown again, which is harmless).
func readMarks(dataDir string) map[string]bool {
	m := map[string]bool{}
	b, err := os.ReadFile(marksPath(dataDir))
	if err != nil {
		return m
	}
	var f struct {
		Done []string `json:"done"`
	}
	if json.Unmarshal(b, &f) == nil {
		for _, x := range f.Done {
			if slices.Contains(markable, x) {
				m[x] = true
			}
		}
	}
	return m
}

func (s setupServer) mark(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Step string `json:"step"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	if !slices.Contains(markable, body.Step) {
		fieldErr(w, "step", fmt.Errorf("one of %v (the others are done when what they set up is there)", markable))
		return
	}
	if body.Step == "keys" {
		// "I saved recovery.pem": only once there is a recovery key (and a release key) to
		// have saved.
		if _, err := s.key.Key(); err != nil {
			fieldErr(w, "step", fmt.Errorf("make or import the release key first: %v", err))
			return
		}
		if _, err := keys.ReadRecovery(s.dataDir); err != nil {
			fieldErr(w, "step", fmt.Errorf("make the recovery key first: %v", err))
			return
		}
	}
	m := readMarks(s.dataDir)
	m[body.Step] = true
	if err := writeMarks(s.dataDir, m); err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	log.Printf("first run: %s marks %s done", auth.User(r.Context()), body.Step)
	s.setup(w, r)
}

func writeMarks(dataDir string, m map[string]bool) error {
	done := []string{}
	for _, x := range markable {
		if m[x] {
			done = append(done, x)
		}
	}
	b, _ := json.Marshal(map[string][]string{"done": done})
	return secfile.WriteFile(marksPath(dataDir), append(b, '\n'))
}

// unmark takes a step's mark away (a new recovery key: the one saved isn't it), if it has one.
func unmark(dataDir, step string) {
	m := readMarks(dataDir)
	if !m[step] {
		return
	}
	delete(m, step)
	if err := writeMarks(dataDir, m); err != nil {
		log.Printf("first run: %s: not unmarked: %v", step, err)
	}
}

// settingsForm is settings.json with every field, an empty one as [] or "" (primary null):
// what the form shows and sends back.
type settingsForm struct {
	Nodes        []string        `json:"nodes"`
	OutOfService []string        `json:"out_of_service"`
	DNSPeers     []string        `json:"dns_peers"`
	DNSPeerZones []string        `json:"dns_peer_zones"`
	Canary       string          `json:"canary"`
	NoDHCP       []string        `json:"no_dhcp"`
	Primary      *primary.Config `json:"primary"`
	// InternalSources: the internal allowlist for blocklist URL sources
	InternalSources []string `json:"internal_sources"`
	// BlocklistRefresh: how often downloaded blocklists are checked ("" the default)
	BlocklistRefresh string `json:"blocklist_refresh"`
}

func formOf(s settings.Settings) settingsForm {
	nz := func(v []string) []string {
		if v == nil {
			return []string{}
		}
		return v
	}
	return settingsForm{Nodes: nz(s.Nodes), OutOfService: nz(s.OutOfService), DNSPeers: nz(s.DNSPeers), DNSPeerZones: nz(s.DNSPeerZones),
		Canary: s.Canary, NoDHCP: nz(s.NoDHCP), Primary: s.Primary, InternalSources: nz(s.InternalSources),
		BlocklistRefresh: s.BlocklistRefresh}
}

func (s setupServer) view() (map[string]any, error) {
	st, version, exists, err := settings.Read(settings.Path(s.dataDir))
	out := map[string]any{"settings": formOf(st), "version": version, "exists": exists}
	if err != nil {
		if !exists && version == "" {
			return nil, err // not there and not readable: nothing to show
		}
		out["error"] = err.Error() // shown, and replaced whole by the next save
	}
	// The zone primary as the Zones page shows it.
	out["primary"] = primaryFacts(st)
	return out, nil
}

func (s setupServer) get(w http.ResponseWriter, r *http.Request) {
	v, err := s.view()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, v)
}

// fieldErr answers a refused field: 400, the error and the field's name.
func fieldErr(w http.ResponseWriter, field string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error(), "field": field})
}

func (s setupServer) save(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Settings json.RawMessage `json:"settings"`
		Version  *string         `json:"version"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	switch {
	case body.Version == nil:
		fieldErr(w, "version", errors.New(`the version read (GET /api/settings "version"; "" while there is no file)`))
		return
	case len(bytes.TrimSpace(body.Settings)) == 0 || bytes.Equal(bytes.TrimSpace(body.Settings), []byte("null")):
		fieldErr(w, "settings", errors.New("the whole settings, as GET /api/settings has them"))
		return
	}
	st, err := settings.Parse(body.Settings)
	if err != nil {
		var fe *settings.FieldError
		if errors.As(err, &fe) {
			fieldErr(w, fe.Field, err)
		} else {
			fieldErr(w, "settings", err)
		}
		return
	}
	path := settings.Path(s.dataDir)
	var old settings.Settings
	save := settings.SaveIf
	if s.saveIf != nil {
		save = s.saveIf
	}
	err = save(path, *body.Version, st, func() error {
		// The file as it is now (the version checked, the writes held off): what changed.
		old, _, _, _ = settings.Read(path)
		return nil
	}, nil)
	var fe *settings.FieldError
	switch {
	case errors.As(err, &fe):
		fieldErr(w, fe.Field, err)
		return
	case errors.Is(err, settings.ErrChanged):
		httpErr(w, http.StatusConflict, err)
		return
	case err != nil:
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	what := changedFields(old, st)
	user := auth.User(r.Context())
	log.Printf("settings: saved by %s: %v", user, what)
	if err := actionlog.Append(s.dataDir, actionlog.Entry{Event: "save", ID: "settings-" + time.Now().UTC().Format("20060102-150405"),
		Source: "controller", Who: user, Action: "settings", Args: what, Result: "ok"}); err != nil {
		log.Printf("settings: saved, but not in the action log: %v", err)
	}
	if s.listed != nil {
		s.listed(st.Nodes)
	}
	s.get(w, r)
}

// changedFields names the fields of settings.json that differ (never their values: the
// action log says what changed, the file says to what).
func changedFields(a, b settings.Settings) []string {
	var out []string
	for _, f := range []struct {
		name string
		same bool
	}{
		{"nodes", slices.Equal(a.Nodes, b.Nodes)},
		{"out_of_service", slices.Equal(a.OutOfService, b.OutOfService)},
		{"dns_peers", slices.Equal(a.DNSPeers, b.DNSPeers)},
		{"dns_peer_zones", slices.Equal(a.DNSPeerZones, b.DNSPeerZones)},
		{"canary", a.Canary == b.Canary},
		{"no_dhcp", slices.Equal(a.NoDHCP, b.NoDHCP)},
		{"primary", a.ZonePrimary().Address == b.ZonePrimary().Address && slices.Equal(a.ZonePrimary().Zones, b.ZonePrimary().Zones)},
		{"internal_sources", slices.Equal(a.InternalSources, b.InternalSources)},
		{"blocklist_refresh", a.BlocklistRefresh == b.BlocklistRefresh},
	} {
		if !f.same {
			out = append(out, f.name)
		}
	}
	if out == nil {
		out = []string{"nothing changed"}
	}
	return out
}
