"""Pack the public keys a node trusts into a `keys` partition image (or one slot of it).

Format (firmware/main/keys.h): two 4 KB slots at 0x1b000; each a 256-byte record, "EDKY",
version 1, flags, generation, source, the seq of the KEYS release that wrote it, the release,
recovery and retired public keys and a CRC-32 (as zlib.crc32), the rest 0xFF. Standard
library only. firmware/main/keys.c reads the same bytes; tests/keys_vectors.json checks both.

    python tools/keyspart.py keys/release.pub keys/recovery.pub keys.bin
    python tools/keyspart.py --slot --generation 2 release.pem recovery.pem slot.bin

A key is a raw public key (65 bytes, 0x04 || X || Y, as tools/keygen.py writes) or a PEM
public key (P-256). The partition image has slot A (generation 1 unless given) and slot B
erased, what a USB install writes; --slot writes the 4 KB slot alone.
"""
import argparse
import base64
import struct
import sys
import zlib

PART_OFFSET = 0x1B000
PART_SIZE = 0x2000
SLOT_SIZE = 0x1000
REC_LEN = 256
VERSION = 1
PUB_LEN = 65
GEN_INVALID = 0xFFFFFFFF
FLAG_RETIRED = 0x0001
SRC_USB, SRC_SEED, SRC_RELEASE = 1, 2, 3
MAGIC = b"EDKY"

# P-256 (SEC 2): y^2 = x^3 - 3x + b over GF(p)
_P = 0xFFFFFFFF00000001000000000000000000000000FFFFFFFFFFFFFFFFFFFFFFFF
_B = 0x5AC635D8AA3A93E7B3EBBD55769886BC651D06B0CC53B0F63BCE3C3E27D2604B
# DER SubjectPublicKeyInfo of an uncompressed P-256 key, up to the point
_SPKI_PREFIX = bytes.fromhex("3059301306072a8648ce3d020106082a8648ce3d030107034200")


def point_ok(pub: bytes) -> bool:
    """Whether pub is an uncompressed point on P-256 (as mbedtls_ecp_check_pubkey)."""
    if len(pub) != PUB_LEN or pub[0] != 4:
        return False
    x, y = int.from_bytes(pub[1:33], "big"), int.from_bytes(pub[33:], "big")
    if x >= _P or y >= _P:
        return False
    return (y * y - (x * x * x - 3 * x + _B)) % _P == 0


def load_key(path: str) -> bytes:
    """A raw public key (65 bytes) or a PEM public key, as the 65-byte point."""
    data = open(path, "rb").read()
    if data.lstrip().startswith(b"-----BEGIN PUBLIC KEY-----"):
        body = b"".join(line for line in data.strip().splitlines() if not line.startswith(b"-----"))
        der = base64.b64decode(body)
        if not der.startswith(_SPKI_PREFIX) or len(der) != len(_SPKI_PREFIX) + PUB_LEN:
            raise ValueError(f"{path}: not a P-256 public key")
        data = der[len(_SPKI_PREFIX):]
    elif data.lstrip().startswith(b"-----"):
        raise ValueError(f"{path}: a PEM file, but not a public key (give the public half)")
    if not point_ok(data):
        raise ValueError(f"{path}: not a P-256 public key (65 bytes, 0x04 || X || Y)")
    return data


def pack(release: bytes, recovery: bytes, generation: int = 1, source: int = SRC_USB, seq: int = 0,
         retired: bytes = None) -> bytes:
    """The 256-byte record for a slot."""
    if not point_ok(release) or not point_ok(recovery):
        raise ValueError("release and recovery must be P-256 public keys")
    if release == recovery:
        raise ValueError("release and recovery keys are the same")
    if retired is not None and not point_ok(retired):
        raise ValueError("retired must be a P-256 public key")
    if not 0 <= generation < GEN_INVALID:
        raise ValueError(f"generation {generation}: 0 to {GEN_INVALID - 1}")
    flags = FLAG_RETIRED if retired is not None else 0
    rec = (MAGIC + struct.pack("<HHIB3xQ", VERSION, flags, generation, source, seq) + release + recovery +
           (retired if retired is not None else b"\xff" * PUB_LEN) + bytes(33))
    return rec + struct.pack("<I", zlib.crc32(rec))


def slot(rec: bytes) -> bytes:
    """A record as its whole 4 KB sector."""
    return rec.ljust(SLOT_SIZE, b"\xff")


def partition(rec_a: bytes = None, rec_b: bytes = None) -> bytes:
    """The 8 KB partition image: each slot's record, or erased."""
    return (slot(rec_a) if rec_a else b"\xff" * SLOT_SIZE) + (slot(rec_b) if rec_b else b"\xff" * SLOT_SIZE)


def parse(rec: bytes):
    """(fields, None) for a valid record, else (None, reason): keys.c's keys_parse."""
    if len(rec) < REC_LEN:
        return None, "short slot"
    if rec[:4] != MAGIC:
        return None, "not a keys record"
    version, flags, generation, source, seq = struct.unpack_from("<HHIB3xQ", rec, 4)
    if version != VERSION:
        return None, "unsupported version"
    if zlib.crc32(rec[:252]) != struct.unpack_from("<I", rec, 252)[0]:
        return None, "bad CRC"
    release, recovery, retired = rec[24:89], rec[89:154], rec[154:219]
    if flags & ~FLAG_RETIRED:
        return None, "unknown flags"
    if any(rec[13:16]) or any(rec[219:252]):
        return None, "reserved bytes set"
    if generation == GEN_INVALID:
        return None, "invalid generation"
    if not SRC_USB <= source <= SRC_RELEASE:
        return None, "unknown source"
    if not point_ok(release):
        return None, "release key is not a P-256 point"
    if not point_ok(recovery):
        return None, "recovery key is not a P-256 point"
    if release == recovery:
        return None, "release and recovery keys are the same"
    if flags & FLAG_RETIRED:
        if not point_ok(retired):
            return None, "retired key is not a P-256 point"
    elif retired != b"\xff" * PUB_LEN:
        return None, "retired key set without its flag"
    return {"flags": flags, "generation": generation, "source": source, "seq": seq, "release": release,
            "recovery": recovery, "retired": retired if flags & FLAG_RETIRED else None}, None


def pick(a: bytes, b: bytes):
    """("ok", 0 or 1, fields), or (state, None, None) for the two slots' contents: keys.c's
    keys_pick (the valid slot with the higher generation, A on a tie). state is "blank" (both
    erased), "foreign" (no valid slot and neither has the magic: never a keys record, so the
    firmware erases and seeds it as if blank) or "corrupt" (no valid slot, one with the magic:
    a damaged record, never seeded over)."""
    blank = [all(x == 0xFF for x in s) for s in (a, b)]
    if all(blank):
        return "blank", None, None
    got = [None if blank[i] else parse(s)[0] for i, s in enumerate((a, b))]
    if got[0] and (not got[1] or got[0]["generation"] >= got[1]["generation"]):
        return "ok", 0, got[0]
    if got[1]:
        return "ok", 1, got[1]
    if a[:4] == MAGIC or b[:4] == MAGIC:
        return "corrupt", None, None
    return "foreign", None, None


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("release", help="release public key: raw .pub or PEM")
    ap.add_argument("recovery", help="recovery public key: raw .pub or PEM")
    ap.add_argument("out", help="where to write the image")
    ap.add_argument("--slot", action="store_true", help="write one 4 KB slot, not the 8 KB partition")
    ap.add_argument("--generation", type=int, default=1)
    ap.add_argument("--source", type=int, default=SRC_USB, choices=(SRC_USB, SRC_SEED, SRC_RELEASE))
    a = ap.parse_args()
    try:
        rec = pack(load_key(a.release), load_key(a.recovery), a.generation, a.source)
    except (OSError, ValueError) as e:
        sys.exit(str(e))
    with open(a.out, "wb") as f:
        f.write(slot(rec) if a.slot else partition(rec))


if __name__ == "__main__":
    main()
