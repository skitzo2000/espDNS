package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
	"github.com/skitzo2000/espdns/controller/internal/jobs"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// Rotate keys, the account panel's (internal/rotation; docs/security.md, Rotating keys): the
// controller prepares a rotation and pushes it; the recovery key signs it offline, never here.
//
//	GET  /api/keys/rotation                {"pending": the rotation under way or null, "rotations": the last ones}
//	POST /api/keys/rotation                {"new_release": "generate"|"keep", "new_recovery": base64 of the new
//	                                       recovery public key or "", "recovery_pub": base64 of the one the
//	                                       nodes trust, if the controller doesn't have it, "compromised": false}:
//	                                       prepared for every node in settings.json (each one's /status read now),
//	                                       as espdns key rotate prepare
//	POST /api/keys/rotation/{id}/signed    {"release": base64 of a node's signed release.bin}: a node's KEYS
//	                                       release signed elsewhere, checked and kept
//	POST /api/keys/rotation/{id}/discard   {}: removed, while no node has taken its KEYS release
//
// The push is the job "key-rotate" {"id": "<rotation>"} (actions.keyRotate): one node at a
// time under the fleet lock, as espdns key rotate push.
type rotationServer struct {
	dataDir string
	key     keys.Source
	http    *http.Client // reaches the nodes; nil: a 10 s timeout
}

func (s rotationServer) routes(mux *routes) {
	mux.HandleFunc("GET /api/keys/rotation", s.get)
	mux.HandleFunc("POST /api/keys/rotation", needLogin("a key rotation needs", s.prepare))
	mux.HandleFunc("POST /api/keys/rotation/{id}/signed", needLogin("a key rotation needs", s.signed))
	mux.HandleFunc("POST /api/keys/rotation/{id}/discard", needLogin("a key rotation needs", s.discard))
}

// rotationView is a rotation as the page shows it.
type rotationView struct {
	*rotation.Plan
	Signed      map[string]bool `json:"signed"` // by node ID: its release.bin is there and checks
	SignedAll   bool            `json:"signed_all"`
	SignCommand string          `json:"sign_command"`
	Dir         string          `json:"dir"` // in the data directory
}

func viewOf(dataDir string, p *rotation.Plan) rotationView {
	dir := filepath.Join(rotation.Root(dataDir), p.ID)
	v := rotationView{Plan: p, Signed: map[string]bool{}, SignedAll: true, SignCommand: rotation.SignCommand(p.ID),
		Dir: filepath.ToSlash(filepath.Join("keys", "rotation", p.ID))}
	for _, n := range p.Nodes {
		_, err := p.Signed(dir, n)
		v.Signed[n.NodeID] = err == nil
		if n.State == rotation.StatePrepared && err != nil {
			v.SignedAll = false
		}
	}
	return v
}

func (s rotationServer) get(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"pending": nil}
	var all []rotationView
	for i, p := range rotation.List(s.dataDir) {
		if i == 10 {
			break
		}
		all = append(all, viewOf(s.dataDir, p))
	}
	if p := rotation.Pending(s.dataDir); p != nil {
		out["pending"] = viewOf(s.dataDir, p)
	}
	out["rotations"] = all
	writeJSON(w, out)
}

func (s rotationServer) logAction(r *http.Request, action string, args ...string) {
	user := auth.User(r.Context())
	if err := actionlog.Append(s.dataDir, actionlog.Entry{Event: "save", ID: "keys-" + time.Now().UTC().Format("20060102-150405"),
		Source: "controller", Who: user, Action: action, Args: args, Result: "ok"}); err != nil {
		log.Printf("keys: %s, but not in the action log: %v", action, err)
	}
}

func (s rotationServer) prepare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewRelease  string `json:"new_release"`
		NewRecovery string `json:"new_recovery"`
		RecoveryPub string `json:"recovery_pub"`
		Compromised bool   `json:"compromised"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	d.DisallowUnknownFields() // a private key sent by mistake is refused, not ignored
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	cur, err := s.key.Key()
	if err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	st, err := settings.Load(settings.Path(s.dataDir))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	o := rotation.PrepareOptions{DataDir: s.dataDir, Current: cur, Compromised: body.Compromised,
		LastSeq: rotation.LastSeq(s.dataDir), Repush: rotation.Repush(s.dataDir)}
	switch body.NewRelease {
	case "generate":
		if o.NewRelease, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
			httpErr(w, http.StatusInternalServerError, err)
			return
		}
	case "keep", "":
	default:
		fieldErr(w, "new_release", errors.New(`"generate" or "keep"`))
		return
	}
	pub := func(field, v string) ([]byte, bool) {
		if strings.TrimSpace(v) == "" {
			return nil, true
		}
		b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
		if err == nil {
			b, err = rotation.ParsePublic(field, b)
		}
		if err != nil {
			fieldErr(w, field, errors.New("base64 of a P-256 public key: 65 bytes, 0x04 || X || Y (the public half only)"))
			return nil, false
		}
		return b, true
	}
	var ok bool
	if o.NewRecovery, ok = pub("new_recovery", body.NewRecovery); !ok {
		return
	}
	given, ok := pub("recovery_pub", body.RecoveryPub)
	if !ok {
		return
	}
	if err := rotation.RefuseOutOfService(st.OutOfService); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if o.Nodes, err = rotation.Targets(ctx, s.dataDir, st.Nodes, &fleet.Client{HTTP: s.http}); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	if o.CurrentRecovery, err = rotation.CurrentRecovery(s.dataDir, given, o.NewRecovery, o.Nodes); err != nil {
		fieldErr(w, "recovery_pub", err)
		return
	}
	_, p, err := rotation.Prepare(o)
	if err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	s.logAction(r, "key rotate prepare", p.ID, p.New.Release, p.New.Recovery)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, viewOf(s.dataDir, p))
}

// plan is the rotation the path names.
func (s rotationServer) plan(w http.ResponseWriter, r *http.Request) (string, *rotation.Plan, bool) {
	id := r.PathValue("id")
	for _, p := range rotation.List(s.dataDir) {
		if p.ID == id {
			return filepath.Join(rotation.Root(s.dataDir), id), p, true
		}
	}
	httpErr(w, http.StatusNotFound, fmt.Errorf("no rotation %q", id))
	return "", nil, false
}

func (s rotationServer) signed(w http.ResponseWriter, r *http.Request) {
	dir, p, ok := s.plan(w, r)
	if !ok {
		return
	}
	var body struct {
		Release string `json:"release"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Release))
	if err != nil {
		fieldErr(w, "release", errors.New("base64 of a node's signed release.bin"))
		return
	}
	if bytes.Contains(b, []byte("PRIVATE KEY")) {
		fieldErr(w, "release", errors.New("that's a private key: give the signed release.bin, never the recovery key"))
		return
	}
	n, err := p.AddSigned(dir, b)
	if err != nil {
		fieldErr(w, "release", err)
		return
	}
	s.logAction(r, "key rotate signed", p.ID, n.Addr)
	writeJSON(w, viewOf(s.dataDir, p))
}

func (s rotationServer) discard(w http.ResponseWriter, r *http.Request) {
	dir, p, ok := s.plan(w, r)
	if !ok {
		return
	}
	if err := rotation.Discard(s.dataDir, dir); err != nil {
		httpErr(w, http.StatusConflict, err)
		return
	}
	s.logAction(r, "key rotate discard", p.ID)
	writeJSON(w, map[string]any{"discarded": p.ID})
}

// keyRotate is the job "key-rotate" {"id": "<rotation>", "nodes": ["<address>"]}: the
// rotation pushed, as espdns key rotate push (the job holds the fleet lock); "nodes", if
// given, the only ones pushed this time. Refused before it is queued if the rotation
// isn't there, is finished, or a node still to take its KEYS release has it unsigned.
func (a actions) keyRotate(raw json.RawMessage) (jobs.Func, error) {
	var p struct {
		ID   string   `json:"id"`
		Only []string `json:"nodes,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("params: %v", err)
	}
	var plan *rotation.Plan
	for _, x := range rotation.List(a.dataDir) {
		if x.ID == p.ID {
			plan = x
		}
	}
	if plan == nil {
		return nil, fmt.Errorf("no rotation %q", p.ID)
	}
	if !plan.Finished.IsZero() {
		return nil, fmt.Errorf("rotation %s is finished", p.ID)
	}
	if !viewOf(a.dataDir, plan).SignedAll {
		return nil, fmt.Errorf("rotation %s is not signed yet: sign it offline first (%s)", p.ID, rotation.SignCommand(p.ID))
	}
	if _, err := a.signer(); err != nil {
		return nil, err
	}
	dir := filepath.Join(rotation.Root(a.dataDir), p.ID)
	return func(ctx context.Context, run *jobs.Run) (any, error) {
		cur, err := a.signer()
		if err != nil {
			return nil, err
		}
		next, err := keys.ReadNext(keys.NextPath(a.dataDir))
		if err != nil {
			return nil, err
		}
		s, err := settings.Load(settings.Path(a.dataDir))
		if err != nil {
			return nil, err
		}
		c := a.client()
		c.Logf = run.Logf
		pl, err := rotation.Push(ctx, rotation.PushOptions{DataDir: a.dataDir, Dir: dir, Listed: s.Nodes, Current: cur, Next: next,
			Client: c, Logf: run.Logf, Stop: run.Stop(), Healthy: a.healthy, Only: p.Only,
			Progress: func(p *rotation.Plan) { run.SetProgress(viewOf(a.dataDir, p)) }})
		if pl != nil {
			run.SetProgress(viewOf(a.dataDir, pl))
		}
		if err != nil {
			return nil, err
		}
		return viewOf(a.dataDir, pl), nil
	}, nil
}
