package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// A key rotation (internal/rotation; docs/security.md, Rotating keys) keeps two more files
// beside release.pem while it runs and after:
//
//	release.next.pem  the new release key, from `espdns key rotate prepare` until every
//	                  listed node trusts it; then it becomes release.pem
//	release.prev.pem  the release key it replaced, kept for the record (after a compromise:
//	                  delete it once you have looked at it)
const (
	NextFile = "release.next.pem"
	PrevFile = "release.prev.pem"
)

// NextPath and PrevPath are those files in dataDir.
func NextPath(dataDir string) string { return filepath.Join(dataDir, Dir, NextFile) }
func PrevPath(dataDir string) string { return filepath.Join(dataDir, Dir, PrevFile) }

// Next is the new release key of a rotation under way beside the release key in f (its
// release.next.pem): nil and no error if there is none.
func (f FileSource) Next() (*ecdsa.PrivateKey, error) {
	return ReadNext(filepath.Join(filepath.Dir(f.Path), NextFile))
}

// ReadNext reads a rotation's new release key at path: nil and no error if there is none.
func ReadNext(path string) (*ecdsa.PrivateKey, error) {
	b, err := secfile.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(path, b)
}

// Alternates are the other release keys src holds, for a node that trusts one of them
// (release.Pusher.Alt): the new key of a rotation under way. One that can't be read is left
// out, said through logf.
func Alternates(src Source, logf func(string, ...any)) []*ecdsa.PrivateKey {
	n, ok := src.(interface {
		Next() (*ecdsa.PrivateKey, error)
	})
	if !ok {
		return nil
	}
	k, err := n.Next()
	if err != nil {
		if logf != nil {
			logf("the rotation's new release key: %v: not used", err)
		}
		return nil
	}
	if k == nil {
		return nil
	}
	return []*ecdsa.PrivateKey{k}
}

// parseSEC1 reads the "EC PRIVATE KEY" PEM block in b (openssl ecparam -genkey's, after
// its "EC PARAMETERS" block), P-256 only.
func parseSEC1(name string, b []byte) (*ecdsa.PrivateKey, error) {
	var blk *pem.Block
	for rest := b; ; {
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, fmt.Errorf("%s: not a PEM file", name)
		}
		if blk.Type == "EC PRIVATE KEY" {
			break
		}
	}
	k, err := x509.ParseECPrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if k.Curve != elliptic.P256() {
		return nil, errors.New(name + ": not a P-256 key")
	}
	return k, nil
}
