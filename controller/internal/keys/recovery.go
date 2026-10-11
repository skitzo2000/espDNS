package keys

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/skitzo2000/espdns/controller/internal/keyspart"
	"github.com/skitzo2000/espdns/controller/internal/release"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// The recovery key's public half (key slot 1), in the data directory beside the release
// key: <data>/keys/recovery.pub, the 65-byte point (0x04 || X || Y) as firmware/keys holds
// it. Only the public half: the private one (recovery.pem) is made in the browser at the
// first run, or with tools/keygen.py, and kept offline; the controller never has it. A USB
// install writes both public keys into the node's keys partition (internal/keyspart).
const RecoveryFile = "recovery.pub"

// RecoveryPath is the recovery public key file in dataDir.
func RecoveryPath(dataDir string) string { return filepath.Join(dataDir, Dir, RecoveryFile) }

// ErrNoRecovery: no recovery public key is set up yet.
var ErrNoRecovery = errors.New("no recovery public key")

// ReadRecovery is the recovery public key in dataDir: ErrNoRecovery (errors.Is) if there is
// none, another error if the file isn't a P-256 point.
func ReadRecovery(dataDir string) ([]byte, error) {
	p := RecoveryPath(dataDir)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s doesn't exist (the first run's Make your keys)", ErrNoRecovery, p)
	}
	if err != nil {
		return nil, err
	}
	if !keyspart.PointOK(b) {
		return nil, fmt.Errorf("%s: not a P-256 public key (65 bytes, 0x04 || X || Y)", p)
	}
	return b, nil
}

// ErrRecoveryExists: SetRecovery was given a different key than the one there.
var ErrRecoveryExists = errors.New("a different recovery public key is already set up")

// SetRecovery writes pub as the recovery public key in dataDir. pub must be a P-256 point
// and not the release key (releasePub, nil if there is none yet). The same key again
// changes nothing (same). A different one is refused unless replace, and even then while
// nodes are listed (listed): they trust the key there, and changing it on them is a
// rotation (a KEYS release signed with the recovery key), not a file written here.
func SetRecovery(dataDir string, pub, releasePub []byte, replace, listed bool) (same bool, err error) {
	if !keyspart.PointOK(pub) {
		return false, errors.New("not a P-256 public key: 65 bytes, 0x04 || X || Y")
	}
	if releasePub != nil && bytes.Equal(pub, releasePub) {
		return false, errors.New("this is the release key: the recovery key is another one")
	}
	old, err := ReadRecovery(dataDir)
	switch {
	case err == nil && bytes.Equal(old, pub):
		return true, nil
	case err == nil && !replace:
		return false, fmt.Errorf("%w (fingerprint %s): replace it only on purpose, before any node is added", ErrRecoveryExists, release.Fingerprint(old))
	case err != nil && !errors.Is(err, ErrNoRecovery) && !replace:
		return false, fmt.Errorf("%v: replace it on purpose to write another", err)
	}
	if err == nil && listed {
		return false, fmt.Errorf("%w (fingerprint %s), and nodes are listed in settings.json: they trust it; changing it on them is a key rotation", ErrRecoveryExists, release.Fingerprint(old))
	}
	return false, secfile.Write(RecoveryPath(dataDir), pub)
}

// ErrKeyExists: Generate found a release key already there.
var ErrKeyExists = errors.New("a release key is already there")

// Generate makes a new release key at path (mode 0600, its directory 0700). It never
// replaces one: anything at path, usable or not, is ErrKeyExists (errors.Is).
func Generate(path string) (*ecdsa.PrivateKey, error) {
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%w: %s (never replaced: move it away first if you mean to)", ErrKeyExists, path)
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	b, err := Encode(k)
	if err != nil {
		return nil, err
	}
	if err := secfile.Create(path, b); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrKeyExists, path)
		}
		return nil, err
	}
	return k, nil
}

// ErrNoKeys: Public found the release key or the recovery public key missing.
var ErrNoKeys = errors.New("set up your keys first")

// Public is the two public keys a new node is given at a USB install: the release key's
// (from src) and the recovery key's (from dataDir). Either missing or unusable is an
// error wrapping ErrNoKeys, saying which.
func Public(src Source, dataDir string) (releasePub, recoveryPub []byte, err error) {
	k, err := src.Key()
	if err != nil {
		return nil, nil, fmt.Errorf("%w (the first run's Make your keys): the release key: %v", ErrNoKeys, err)
	}
	rec, err := ReadRecovery(dataDir)
	if err != nil {
		return nil, nil, fmt.Errorf("%w (the first run's Make your keys): the recovery key: %v", ErrNoKeys, err)
	}
	pub := release.PublicRaw(k)
	if bytes.Equal(pub, rec) {
		return nil, nil, fmt.Errorf("%w: the release and recovery keys are the same key", ErrNoKeys)
	}
	return pub, rec, nil
}
