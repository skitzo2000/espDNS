"""Build and sign espDNS release manifests (format in main/release.h), and KEYS releases.

Signing needs the `cryptography` package, which the espressif/idf image already has.

A KEYS release (main/keys.h) rewrites one node's keys. It is signed with that node's current
recovery key, offline, in two steps (the second needs no network):

    python tools/release.py keys-prepare --node 30:ed:a0:00:00:01 --image esp32s3-octal \\
        --release new-release.pub --recovery new-recovery.pub [--keep-retired] \\
        [--current-recovery <fingerprint>] [--last-seq N] --dir rotation/
        writes rotation/manifest.bin (unsigned), rotation/payload.bin and rotation/keys.json
    python tools/release.py keys-sign --dir rotation/ --key recovery.pem
        checks the manifest and the payload and signs: rotation/release.bin (header and
        payload), what is posted to the node's /release

A key is a raw public key (65 bytes, as tools/keygen.py writes) or a PEM public key.
"""
import argparse
import hashlib
import json
import os
import struct
import sys
import time

try:
    from cryptography.hazmat.primitives import hashes, serialization
    from cryptography.hazmat.primitives.asymmetric import ec
    from cryptography.hazmat.primitives.asymmetric.utils import decode_dss_signature
except ImportError:  # keys-prepare needs none of it
    hashes = serialization = ec = decode_dss_signature = None

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import keyspart  # noqa: E402

MAGIC = b"ESPDNS1\0"
KINDS = {"firmware": 1, "config": 2, "zones": 3, "blocklist": 4, "overrides": 5, "control": 6, "keys": 7}
KEY_RELEASE, KEY_RECOVERY = 0, 1
MANIFEST_LEN, SIG_LEN = 128, 64


def parse_mac(s):
    b = bytes.fromhex(s.replace(":", "").replace("-", ""))
    if len(b) != 6:
        raise ValueError(f"bad node ID {s!r}")
    return b


def manifest(kind, key_id, target, board, seq, payload):
    board_b = board.encode()
    if len(board_b) > 15:
        raise ValueError("board name longer than 15 bytes")
    m = MAGIC + struct.pack("<BB", KINDS[kind], key_id) + target + board_b.ljust(16, b"\0")
    m += struct.pack("<QQ", seq, len(payload)) + hashlib.sha256(payload).digest()
    return m.ljust(MANIFEST_LEN, b"\0")


def sign(private_key, data):
    r, s = decode_dss_signature(private_key.sign(data, ec.ECDSA(hashes.SHA256())))
    return r.to_bytes(32, "big") + s.to_bytes(32, "big")


def header(private_key, kind, key_id, target, board, seq, payload):
    m = manifest(kind, key_id, target, board, seq, payload)
    return m + sign(private_key, m)


# How far past the signer's clock a seq may go (main/release.h REL_SEQ_AHEAD_MS, the
# controller's release.SeqAhead): a seq far in the future leaves the node no seq above it
# for the releases after it.
SEQ_AHEAD_MS = 24 * 3600 * 1000


def next_seq(last, now_ms=None):
    """Milliseconds since 1970, or last + 1 if that is not higher. Time-based so a
    rebuilt controller with an empty database still issues seqs the nodes accept.

    last comes from the node's /status, which anything on its address can send: one more
    than SEQ_AHEAD_MS past this clock (say near 2^64) is refused (ValueError), never
    signed above."""
    now = int(time.time() * 1000) if now_ms is None else now_ms
    if not isinstance(last, int) or isinstance(last, bool) or last < 0:
        raise ValueError(f"the node's last seq {last!r} is not a seq")
    if last >= now + SEQ_AHEAD_MS:
        raise ValueError(f"the node's last seq {last} is more than {SEQ_AHEAD_MS // 3600000} h past this clock "
                         f"({now}): not signing above it (its /status is not the node's, or a clock is wrong)")
    return max(now, last + 1)


def load_private(path):
    with open(path, "rb") as f:
        return serialization.load_pem_private_key(f.read(), password=None)


def public_raw(private_key):
    return private_key.public_key().public_bytes(serialization.Encoding.X962,
                                                 serialization.PublicFormat.UncompressedPoint)


def fingerprint(pub_raw):
    return hashlib.sha256(pub_raw).hexdigest()[:16]


# ---- KEYS releases (main/keys.h) ----

KEYS_MAGIC = b"EDKR"
KEYS_VERSION = 1
KEYS_FLAG_RETIRE = 0x0001
KEYS_LEN = 138


def keys_payload(release_pub, recovery_pub, keep_retired):
    """The 138-byte payload: "EDKR", version, flags (bit 0: keep the release key it replaces
    as the retired key), the new release and recovery public keys."""
    if not keyspart.point_ok(release_pub) or not keyspart.point_ok(recovery_pub):
        raise ValueError("the new release and recovery keys must be P-256 public keys")
    if release_pub == recovery_pub:
        raise ValueError("the new release and recovery keys are the same")
    flags = KEYS_FLAG_RETIRE if keep_retired else 0
    return KEYS_MAGIC + struct.pack("<HH", KEYS_VERSION, flags) + release_pub + recovery_pub


def parse_keys_payload(p):
    """(release, recovery, keep_retired), or ValueError: keys.c's keys_rel_parse."""
    if len(p) != KEYS_LEN:
        raise ValueError("a keys release's payload is 138 bytes")
    if p[:4] != KEYS_MAGIC:
        raise ValueError("not a keys release payload")
    version, flags = struct.unpack_from("<HH", p, 4)
    if version != KEYS_VERSION:
        raise ValueError("unsupported keys release version")
    if flags & ~KEYS_FLAG_RETIRE:
        raise ValueError("unknown keys release flags")
    rel, rec = p[8:73], p[73:138]
    if not keyspart.point_ok(rel):
        raise ValueError("new release key is not a P-256 point")
    if not keyspart.point_ok(rec):
        raise ValueError("new recovery key is not a P-256 point")
    if rel == rec:
        raise ValueError("new release and recovery keys are the same")
    return rel, rec, bool(flags & KEYS_FLAG_RETIRE)


def _write(path, data):
    """Written whole or not at all, readable by the owner alone."""
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "wb") as f:
        f.write(data)
    os.replace(tmp, path)


def _mac(b):
    return ":".join(f"{x:02x}" for x in b)


def keys_prepare(a):
    node = parse_mac(a.node)
    rel, rec = keyspart.load_key(a.release), keyspart.load_key(a.recovery)
    payload = keys_payload(rel, rec, a.keep_retired)
    seq = a.seq if a.seq is not None else next_seq(a.last_seq)
    if seq <= a.last_seq:
        raise ValueError(f"seq {seq} is not above the node's last KEYS seq {a.last_seq}")
    m = manifest("keys", KEY_RECOVERY, node, a.image, seq, payload)
    os.makedirs(a.dir, exist_ok=True)
    _write(os.path.join(a.dir, "manifest.bin"), m)
    _write(os.path.join(a.dir, "payload.bin"), payload)
    info = {"node": _mac(node), "image": a.image, "seq": seq, "keep_retired": a.keep_retired,
            "release": fingerprint(rel), "recovery": fingerprint(rec),
            "signer": a.current_recovery.lower() if a.current_recovery else None}
    _write(os.path.join(a.dir, "keys.json"), (json.dumps(info, indent=2) + "\n").encode())
    print(f"KEYS release for {info['node']} ({a.image}), seq {seq}: release {info['release']}, "
          f"recovery {info['recovery']}, keep the old release key: {'yes' if a.keep_retired else 'no'}; "
          f"unsigned in {a.dir}")


def keys_sign(a):
    if ec is None:
        raise ValueError("signing needs the cryptography package (the espressif/idf image has it)")
    d = a.dir
    with open(os.path.join(d, "manifest.bin"), "rb") as f:
        m = f.read()
    with open(os.path.join(d, "payload.bin"), "rb") as f:
        payload = f.read()
    with open(os.path.join(d, "keys.json")) as f:
        info = json.load(f)
    if len(m) != MANIFEST_LEN or m[:8] != MAGIC or any(m[80:]):
        raise ValueError("manifest.bin is not a release manifest")
    target, image = m[10:16], m[16:32].split(b"\0")[0].decode()
    seq, plen = struct.unpack_from("<QQ", m, 32)
    if m[8] != KINDS["keys"] or m[9] != KEY_RECOVERY:
        raise ValueError("manifest.bin is not a KEYS release for the recovery key")
    if plen != len(payload) or hashlib.sha256(payload).digest() != m[48:80]:
        raise ValueError("payload.bin does not match the manifest")
    rel, rec, keep = parse_keys_payload(payload)
    if _mac(target) != info.get("node") or image != info.get("image") or seq != info.get("seq") or \
            fingerprint(rel) != info.get("release") or fingerprint(rec) != info.get("recovery"):
        raise ValueError("keys.json does not describe manifest.bin and payload.bin")
    key = load_private(a.key)
    signer = fingerprint(public_raw(key))
    if info.get("signer") and signer != info["signer"]:
        raise ValueError(f"{a.key} is key {signer}, not the node's recovery key {info['signer']}")
    out = m + sign(key, m) + payload
    _write(os.path.join(d, "release.bin"), out)
    print(f"signed with recovery key {signer}: KEYS release for {_mac(target)} ({image}), seq {seq}: "
          f"release {fingerprint(rel)}, recovery {fingerprint(rec)}, keep the old release key: "
          f"{'yes' if keep else 'no'}")
    print(f"wrote {os.path.join(d, 'release.bin')} ({len(out)} bytes): POST it to the node's /release")


def main(argv=None):
    ap = argparse.ArgumentParser(description="KEYS releases (main/keys.h): prepare, then sign offline")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("keys-prepare", help="the unsigned KEYS release for one node")
    p.add_argument("--node", required=True, help="the node ID, as /status node_id")
    p.add_argument("--image", required=True, help="the chip image it runs, as /status image")
    p.add_argument("--release", required=True, help="the new release public key (raw .pub or PEM)")
    p.add_argument("--recovery", required=True, help="the new recovery public key (raw .pub or PEM)")
    p.add_argument("--keep-retired", action="store_true",
                   help="keep the release key being replaced as the retired key, so what the node stored "
                        "still loads until re-pushed (a planned rotation; never after a compromise)")
    p.add_argument("--current-recovery", help="the node's recovery key fingerprint now (/status keys[1]): "
                                              "keys-sign then signs with that key only")
    p.add_argument("--last-seq", type=int, default=0, help="the node's last KEYS seq (/status seq.keys)")
    p.add_argument("--seq", type=int, help="the seq to use (default: this clock, above --last-seq)")
    p.add_argument("--dir", required=True, help="where to write it")
    s = sub.add_parser("keys-sign", help="sign a prepared KEYS release with the recovery key (no network)")
    s.add_argument("--dir", required=True, help="what keys-prepare wrote")
    s.add_argument("--key", required=True, help="the node's recovery private key (PEM)")
    a = ap.parse_args(argv)
    try:
        (keys_prepare if a.cmd == "keys-prepare" else keys_sign)(a)
    except (OSError, ValueError, KeyError) as e:
        sys.exit(str(e))


if __name__ == "__main__":
    main()
