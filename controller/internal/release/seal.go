package release

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
)

// sealInfo is HKDF's info before the node ID (firmware/main/nodekey.h NODEKEY_INFO).
const sealInfo = "espdns wifi.sealed v1"

// ParseNodeKey reads a node key as /status "node_key" gives it: base64 of an uncompressed
// P-256 point (firmware/main/nodekey.h).
func ParseNodeKey(s string) (*ecdh.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		var k *ecdh.PublicKey
		if k, err = ecdh.P256().NewPublicKey(b); err == nil {
			return k, nil
		}
	}
	return nil, fmt.Errorf("node key %q: not a P-256 public key", s)
}

// sealAEAD is the cipher and nonce for the ECDH secret z and node: HKDF-SHA256 (no salt,
// info sealInfo and the node ID), 32 bytes of AES-256-GCM key, then 12 of nonce.
func sealAEAD(z []byte, node [6]byte) (cipher.AEAD, []byte, error) {
	okm, err := hkdf.Key(sha256.New, z, nil, sealInfo+string(node[:]), 44)
	if err != nil {
		return nil, nil, err
	}
	b, err := aes.NewCipher(okm[:32])
	if err != nil {
		return nil, nil, err
	}
	a, err := cipher.NewGCM(b)
	return a, okm[32:], err
}

// Seal seals a Wi-Fi password to node's key pub: a one-time key E, then the password
// encrypted, then the tag (firmware/main/nodekey.h). Only that node opens it.
func Seal(pub *ecdh.PublicKey, node [6]byte, password []byte) ([]byte, error) {
	e, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return sealWith(e, pub, node, password)
}

func sealWith(e *ecdh.PrivateKey, pub *ecdh.PublicKey, node [6]byte, password []byte) ([]byte, error) {
	z, err := e.ECDH(pub)
	if err != nil {
		return nil, err
	}
	a, nonce, err := sealAEAD(z, node)
	if err != nil {
		return nil, err
	}
	return a.Seal(e.PublicKey().Bytes(), nonce, password, nil), nil
}

// Open is Seal undone with the node's private key (the fake node, tests).
func Open(priv *ecdh.PrivateKey, node [6]byte, blob []byte) ([]byte, error) {
	if len(blob) < 65+16 {
		return nil, errors.New("not a sealed password")
	}
	e, err := ecdh.P256().NewPublicKey(blob[:65])
	if err != nil {
		return nil, err
	}
	z, err := priv.ECDH(e)
	if err != nil {
		return nil, err
	}
	a, nonce, err := sealAEAD(z, node)
	if err != nil {
		return nil, err
	}
	return a.Open(nil, nonce, blob[65:], nil)
}

// SealConfig is a config payload as it goes to the node: its Wi-Fi password, if any, as
// wifi.sealed to the node key (nodeKey, as /status gives it) and node ID; an empty one (an
// open network) left out. A node takes no password in plain text. A payload without one is
// returned as it is.
func SealConfig(payload []byte, nodeKey string, node [6]byte) ([]byte, error) {
	c, err := nodecfg.Parse(payload)
	if err != nil {
		return nil, err
	}
	if c.Wifi == nil || c.Wifi.Password == nil {
		return payload, nil
	}
	if pw := *c.Wifi.Password; pw != "" {
		pub, err := ParseNodeKey(nodeKey)
		if err != nil {
			return nil, err
		}
		b, err := Seal(pub, node, []byte(pw))
		if err != nil {
			return nil, err
		}
		s := base64.StdEncoding.EncodeToString(b)
		c.Wifi.Sealed = &s
	}
	c.Wifi.Password = nil
	return c.Payload()
}
