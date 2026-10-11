"""Regenerate tests/keys_vectors.json: keys partition records (main/keys.h) packed and read by
tools/keyspart.py, which main/keys.c (test_core) and the controller must match byte for byte
and answer for answer. Test keys only: two from release_vectors.h and P-256's generator point.

    python3 tests/gen_keys_vectors.py > tests/keys_vectors.json

Each slot in "picks" is a record (hex, the rest of its 4 KB sector 0xFF), or "" for an erased
slot. A pick's "seed" says whether the firmware seeds the range: "blank" (both erased) and
"foreign" (no valid slot, neither with the magic: bytes an earlier flash left, erased first);
never "corrupt" (a damaged record with the magic). Every expectation here is written out by hand and checked against keyspart as the
vectors are made, so the file never just records what the tool happens to do.
"""
import hashlib
import json
import os
import struct
import sys
import zlib

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "tools"))
import keyspart as kp  # noqa: E402

KEYS = {
    "k0": bytes.fromhex("04a3a54161b507308d74c4b06ab7d0825dcd7ad07b9a23dbaebac949f4e543734f"
                        "80a9ecd21b7d221743da360af80d8baf84751d60222e3c6ae61821a86520fef6"),
    "k1": bytes.fromhex("04c6c8e212eb8f390b140d5ef88fb067537757eba1f49a8f6cc8fbab1a813fcbfa"
                        "220f2f7c715853bcb739231c1c835d14f158b69af6d618fb250993d29ea1a918"),
    "k2": bytes.fromhex("046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296"
                        "4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5"),
}
K0, K1, K2 = KEYS["k0"], KEYS["k1"], KEYS["k2"]


def fp(pub):
    return hashlib.sha256(pub).hexdigest()[:16]


def crc(rec):
    """rec with its CRC set again (for a record changed after packing)."""
    rec = bytes(rec[:252])
    return rec + struct.pack("<I", zlib.crc32(rec))


def setb(rec, off, b, fix=True):
    r = bytearray(rec)
    r[off:off + len(b)] = b
    return crc(r) if fix else bytes(r)


def off_curve(pub):
    return pub[:-1] + bytes([pub[-1] ^ 1])


GOOD = kp.pack(K0, K1)

packs = [
    ("usb install", dict(release=K0, recovery=K1, generation=1, source=kp.SRC_USB, seq=0)),
    ("seeded", dict(release=K0, recovery=K1, generation=1, source=kp.SRC_SEED, seq=0)),
    ("keys release with a retired key", dict(release=K1, recovery=K2, generation=7, source=kp.SRC_RELEASE,
                                             seq=1_790_000_000_123, retired=K0)),
    ("generation 0", dict(release=K2, recovery=K0, generation=0, source=kp.SRC_USB, seq=0)),
    ("highest generation", dict(release=K0, recovery=K2, generation=0xFFFFFFFE, source=kp.SRC_RELEASE,
                                seq=2**64 - 1)),
]

parses = [
    ("good", GOOD, None),
    ("bad magic", setb(GOOD, 0, b"EDKX"), "not a keys record"),
    ("version 2", setb(GOOD, 4, b"\x02\x00"), "unsupported version"),
    ("bad CRC", setb(GOOD, 30, b"\x00", fix=False), "bad CRC"),
    ("CRC flipped", setb(GOOD, 252, bytes([GOOD[252] ^ 0x80]), fix=False), "bad CRC"),
    ("unknown flags", setb(GOOD, 6, b"\x02\x00"), "unknown flags"),
    ("reserved after source", setb(GOOD, 14, b"\x01"), "reserved bytes set"),
    ("reserved before CRC", setb(GOOD, 251, b"\x01"), "reserved bytes set"),
    ("generation 0xFFFFFFFF", setb(GOOD, 8, b"\xff\xff\xff\xff"), "invalid generation"),
    ("source 0", setb(GOOD, 12, b"\x00"), "unknown source"),
    ("source 4", setb(GOOD, 12, b"\x04"), "unknown source"),
    ("release off the curve", setb(GOOD, 24, off_curve(K0)), "release key is not a P-256 point"),
    ("release compressed", setb(GOOD, 24, b"\x02"), "release key is not a P-256 point"),
    ("recovery off the curve", setb(GOOD, 89, off_curve(K1)), "recovery key is not a P-256 point"),
    ("recovery all zero", setb(GOOD, 89, bytes(65)), "recovery key is not a P-256 point"),
    ("recovery X = p", setb(GOOD, 90, (kp._P).to_bytes(32, "big")), "recovery key is not a P-256 point"),
    ("same keys", setb(GOOD, 89, K0), "release and recovery keys are the same"),
    ("retired without flag", setb(GOOD, 154, K2), "retired key set without its flag"),
    ("flag without retired", setb(GOOD, 6, b"\x01\x00"), "retired key is not a P-256 point"),
    ("retired off the curve", setb(setb(GOOD, 6, b"\x01\x00"), 154, off_curve(K2)), "retired key is not a P-256 point"),
    ("retired with flag", setb(setb(GOOD, 6, b"\x01\x00"), 154, K2), None),
    ("all zero", bytes(256), "not a keys record"),
]


def gen(n, a=K0, b=K1, source=kp.SRC_USB):
    return kp.pack(a, b, generation=n, source=source)


BAD_CRC = setb(GOOD, 30, b"\x00", fix=False)
picks = [
    ("both erased", "", "", "blank", None, None),
    ("A only", gen(1), "", "ok", 0, 1),
    ("B only", "", gen(1), "ok", 1, 1),
    ("B newer", gen(1), gen(2, K1, K2), "ok", 1, 2),
    ("A newer", gen(3, K1, K2), gen(2), "ok", 0, 3),
    ("tie: A", gen(2), gen(2, K1, K2), "ok", 0, 2),
    ("A corrupt, B good", BAD_CRC, gen(1), "ok", 1, 1),
    ("A good, B generation 0xFFFFFFFF", gen(1), setb(gen(5), 8, b"\xff\xff\xff\xff"), "ok", 0, 1),
    ("A good, B newer but off the curve", gen(1), setb(gen(9), 24, off_curve(K0)), "ok", 0, 1),
    ("generation 0 against erased", "", gen(0), "ok", 1, 0),
    ("A corrupt, B erased", BAD_CRC, "", "corrupt", None, None),
    ("A erased, B corrupt", "", BAD_CRC, "corrupt", None, None),
    ("both corrupt", BAD_CRC, setb(GOOD, 0, b"EDKX"), "corrupt", None, None),
    ("magic and bad CRC beside stale bytes", bytes(range(256)), BAD_CRC, "corrupt", None, None),
    ("version 2 beside erased", setb(GOOD, 4, b"\x02\x00"), "", "corrupt", None, None),
    ("stale bytes (an old app), B erased", bytes(range(256)), "", "foreign", None, None),
    ("stale bytes in both", bytes(range(256)), bytes(256), "foreign", None, None),
    ("no magic (EDKX), B erased", setb(GOOD, 0, b"EDKX"), "", "foreign", None, None),
    ("stale bytes, B good", bytes(range(256)), gen(1), "ok", 1, 1),
]


def main():
    out = {"keys": {k: v.hex() for k, v in KEYS.items()},
           "fingerprints": {k: fp(v) for k, v in KEYS.items()},
           "packs": [], "parses": [], "picks": []}
    for name, a in packs:
        rec = kp.pack(**a)
        assert kp.parse(rec)[1] is None, name
        names = {v: k for k, v in KEYS.items()}
        out["packs"].append({"name": name, "release": names[a["release"]], "recovery": names[a["recovery"]],
                             "retired": names.get(a.get("retired")), "generation": a["generation"],
                             "source": a["source"], "seq": str(a["seq"]), "record": rec.hex(),
                             "partition_sha256": hashlib.sha256(kp.partition(rec)).hexdigest()})
    for name, rec, want in parses:
        got = kp.parse(rec)[1]
        assert got == want, f"{name}: keyspart says {got!r}, want {want!r}"
        out["parses"].append({"name": name, "record": rec.hex(), "error": want})
    for name, a, b, state, which, generation in picks:
        sa = kp.slot(a) if a else b"\xff" * kp.SLOT_SIZE
        sb = kp.slot(b) if b else b"\xff" * kp.SLOT_SIZE
        got = kp.pick(sa, sb)
        assert (got[0], got[1]) == (state, which), f"{name}: keyspart says {got[:2]}, want {(state, which)}"
        assert generation is None or got[2]["generation"] == generation, name
        out["picks"].append({"name": name, "a": a.hex() if a else "", "b": b.hex() if b else "", "state": state,
                             "slot": which, "generation": generation, "seed": state in ("blank", "foreign")})
    json.dump(out, sys.stdout, indent=1)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
