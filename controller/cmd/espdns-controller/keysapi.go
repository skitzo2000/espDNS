package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/actionlog"
	"github.com/skitzo2000/espdns/controller/internal/auth"
	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/keyspart"
	"github.com/skitzo2000/espdns/controller/internal/nodes"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/rotation"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The keys from the browser (docs/architecture.md, Signed releases and pins; the first run's Make your
// keys). The controller holds the release key (it signs every change) and the recovery key's
// PUBLIC half only: a USB install writes both public keys into the node's keys partition.
//
//	GET  /api/key                 the release key: present, fingerprint, source, error and
//	                              missing (no file); "recovery": the recovery public key's
//	                              present, fingerprint, error; "nodes_listed": settings.json
//	                              lists a node (a key is then never replaced from here);
//	                              "rotation": a key rotation under way, its id and new keys
//	                              ({"id", "release", "recovery"}), or null
//	POST /api/keys/release        {} makes a new release key in <data>/keys/release.pem, as
//	                              espdns key generate: never over one there, never while a
//	                              node is listed (it trusts another key)
//	GET  /api/keys/release.pub    the release key's public half, the 65-byte point
//	POST /api/keys/recovery       {"public_key": base64 of the 65-byte point, "replace": false}:
//	                              the recovery public key, made in the browser (WebCrypto) or
//	                              read from a file there; the private key never comes here. The
//	                              same key again changes nothing; another is refused unless
//	                              replace, and always while a node is listed. A listed node
//	                              that trusts another recovery key refuses it too.
//
// The GETs are open as the other reads are while no password is set; the POSTs need
// a login.
type keysServer struct {
	dataDir string
	key     keys.Source
	nodes   func() []nodes.Node
}

func (s keysServer) routes(mux *routes) {
	mux.HandleFunc("GET /api/key", s.get)
	mux.HandleFunc("POST /api/keys/release", needLogin("making a release key needs", s.generate))
	mux.HandleFunc("GET /api/keys/release.pub", s.releasePub)
	mux.HandleFunc("POST /api/keys/recovery", needLogin("setting the recovery key needs", s.setRecovery))
}

// listed is settings.json's nodes (an unreadable settings file counts as listing some: no key
// is replaced on a guess).
func (s keysServer) listed() ([]string, error) {
	st, _, exists, err := settings.Read(settings.Path(s.dataDir))
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return st.Nodes, nil
}

func (s keysServer) get(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"source": s.key.String()}
	k, err := s.key.Key()
	if err == nil {
		out["present"], out["fingerprint"] = true, keys.Fingerprint(k)
	} else {
		out["present"], out["error"], out["missing"] = false, err.Error(), errors.Is(err, keys.ErrNoKey)
	}
	rec := map[string]any{}
	if pub, err := keys.ReadRecovery(s.dataDir); err == nil {
		rec["present"], rec["fingerprint"] = true, keyspart.Fingerprint(pub)
	} else {
		rec["present"], rec["error"], rec["missing"] = false, err.Error(), errors.Is(err, keys.ErrNoRecovery)
	}
	out["recovery"] = rec
	// A key rotation under way (rotation.go): the keys every node is going to, so a page
	// says which nodes have them yet.
	out["rotation"] = nil
	if p := rotation.Pending(s.dataDir); p != nil {
		out["rotation"] = map[string]any{"id": p.ID, "release": p.New.Release, "recovery": p.New.Recovery}
	}
	ns, err := s.listed()
	out["nodes_listed"] = err != nil || len(ns) > 0
	writeJSON(w, out)
}

func (s keysServer) logAction(r *http.Request, action, fp string) {
	user := auth.User(r.Context())
	log.Printf("keys: %s by %s, fingerprint %s", action, user, fp)
	if err := actionlog.Append(s.dataDir, actionlog.Entry{Event: "save", ID: "keys-" + time.Now().UTC().Format("20060102-150405"),
		Source: "controller", Who: user, Action: action, Args: []string{fp}, Result: "ok"}); err != nil {
		log.Printf("keys: %s, but not in the action log: %v", action, err)
	}
}

func (s keysServer) generate(w http.ResponseWriter, r *http.Request) {
	ns, err := s.listed()
	switch {
	case err != nil:
		httpErr(w, http.StatusInternalServerError, err)
		return
	case len(ns) > 0:
		httpErr(w, http.StatusConflict, fmt.Errorf("settings.json lists %d node(s): they trust a release key already; "+
			"import that one (espdns key import), a new one would sign releases they all refuse", len(ns)))
		return
	}
	src, ok := s.key.(keys.FileSource)
	if !ok {
		httpErr(w, http.StatusConflict, fmt.Errorf("the release key comes from %s, not a file here: made there", s.key))
		return
	}
	k, err := keys.Generate(src.Path)
	if errors.Is(err, keys.ErrKeyExists) {
		httpErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	s.logAction(r, "key generate", keys.Fingerprint(k))
	s.get(w, r)
}

func (s keysServer) releasePub(w http.ResponseWriter, r *http.Request) {
	k, err := s.key.Key()
	if err != nil {
		httpErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=release.pub")
	w.Write(release.PublicRaw(k))
}

func (s keysServer) setRecovery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PublicKey string `json:"public_key"`
		Replace   bool   `json:"replace"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	d.DisallowUnknownFields() // a private key sent by mistake ("private_key", "d", ...) is refused, not ignored
	if err := d.Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, err)
		return
	}
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.PublicKey))
	if err != nil || !keyspart.PointOK(pub) {
		fieldErr(w, "public_key", errors.New("base64 of a P-256 public key: 65 bytes, 0x04 || X || Y (the public half only)"))
		return
	}
	ns, err := s.listed()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err)
		return
	}
	// A listed node that reports another recovery key (slot 1): this isn't the fleet's.
	fp := keyspart.Fingerprint(pub)
	for _, n := range s.nodes() {
		if n.Source != nodes.SourceSettings {
			continue
		}
		if st, ok := status(n); ok && len(st.Keys) > 1 && st.Keys[1] != fp {
			httpErr(w, http.StatusConflict, fmt.Errorf("%s trusts the recovery key %s, not %s: give the one your nodes trust", n.Addr, st.Keys[1], fp))
			return
		}
	}
	var relPub []byte
	if k, err := s.key.Key(); err == nil {
		relPub = release.PublicRaw(k)
	}
	same, err := keys.SetRecovery(s.dataDir, pub, relPub, body.Replace, len(ns) > 0)
	switch {
	case errors.Is(err, keys.ErrRecoveryExists):
		httpErr(w, http.StatusConflict, err)
		return
	case err != nil:
		fieldErr(w, "public_key", err)
		return
	}
	if !same {
		s.logAction(r, "key recovery", fp)
		// A new recovery key: the backup of the old recovery.pem doesn't hold it; the first
		// run asks again that this one is saved.
		unmark(s.dataDir, "keys")
	}
	s.get(w, r)
}
