"""tools/release.py's KEYS releases (main/keys.h): the payload and manifest against
tests/release_vectors.h (which main/keys.c and main/release.c check in test_core, and the
controller's internal/release too), and keys-prepare / keys-sign. Run by `make test`."""
import json
import os
import re
import subprocess
import sys
import tempfile
import unittest

from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives.asymmetric.utils import encode_dss_signature

HERE = os.path.dirname(os.path.abspath(__file__))
TOOLS = os.path.join(HERE, "..", "tools")
sys.path.insert(0, TOOLS)
import release  # noqa: E402


def rd(path):
    with open(path, "rb") as f:
        return f.read()


def wr(path, data):
    with open(path, "wb") as f:
        f.write(data)


def vectors():
    text = rd(os.path.join(HERE, "release_vectors.h")).decode()
    return {m.group(1): bytes(int(x, 16) for x in m.group(2).split(","))
            for m in re.finditer(r"static const uint8_t (\w+)\[\d+\] = \{([^}]*)\};", text)}


V = vectors()


def verify(pub, data, sig):
    k = ec.EllipticCurvePublicKey.from_encoded_point(ec.SECP256R1(), pub)
    k.verify(encode_dss_signature(int.from_bytes(sig[:32], "big"), int.from_bytes(sig[32:], "big")), data,
             ec.ECDSA(hashes.SHA256()))


def pem(key):
    return key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                             serialization.NoEncryption())


class KeysReleaseTest(unittest.TestCase):
    def test_vectors(self):
        p = release.keys_payload(V["V_KEY2"], V["V_KEY3"], True)
        self.assertEqual(p, V["V_KEYS_PAYLOAD"])
        self.assertEqual(len(p), release.KEYS_LEN)
        self.assertEqual(release.parse_keys_payload(p), (V["V_KEY2"], V["V_KEY3"], True))
        m = release.manifest("keys", release.KEY_RECOVERY, V["V_NODE"], "test-board", 4000, p)
        self.assertEqual(m, V["V_KEYS"][:128])
        verify(V["V_KEY1"], m, V["V_KEYS"][128:])
        self.assertEqual(release.KINDS["keys"], 7)
        # without the flag: bit 0 clear
        self.assertEqual(release.keys_payload(V["V_KEY2"], V["V_KEY3"], False)[6:8], b"\0\0")

    def test_payload_refuses(self):
        good = V["V_KEYS_PAYLOAD"]
        bad = [good[:-1], good + b"\0", b"EDKY" + good[4:], good[:4] + b"\x02" + good[5:],
               good[:6] + b"\x02\x00" + good[8:], good[:40] + bytes([good[40] ^ 1]) + good[41:],
               good[:100] + bytes([good[100] ^ 1]) + good[101:], good[:73] + good[8:73]]
        for p in bad:
            with self.assertRaises(ValueError):
                release.parse_keys_payload(p)
        with self.assertRaises(ValueError):
            release.keys_payload(V["V_KEY2"], V["V_KEY2"], False)

    def test_prepare_and_sign(self):
        rec_now = ec.generate_private_key(ec.SECP256R1())
        other = ec.generate_private_key(ec.SECP256R1())
        with tempfile.TemporaryDirectory() as d:
            rel_pub, rec_pub = os.path.join(d, "release.pub"), os.path.join(d, "recovery.pub")
            wr(rel_pub, V["V_KEY2"])
            wr(rec_pub, V["V_KEY3"])
            key, wrong = os.path.join(d, "recovery.pem"), os.path.join(d, "other.pem")
            wr(key, pem(rec_now))
            wr(wrong, pem(other))
            fp = release.fingerprint(release.public_raw(rec_now))
            rot = os.path.join(d, "rotation")
            tool = [sys.executable, os.path.join(TOOLS, "release.py")]

            def prepare(*extra, node="30:ed:a0:00:00:01"):
                return subprocess.run(tool + ["keys-prepare", "--node", node, "--image", "test-board",
                                              "--release", rel_pub, "--recovery", rec_pub, "--dir", rot,
                                              *extra], capture_output=True)

            def sign(k):
                return subprocess.run(tool + ["keys-sign", "--dir", rot, "--key", k], capture_output=True)

            self.assertNotEqual(prepare(node="ff:ff:ff:ff:ff:ff").returncode, 0)  # any node: refused
            r = prepare("--keep-retired", "--current-recovery", fp, "--seq", "4000", "--last-seq", "10")
            self.assertEqual(r.returncode, 0, r.stderr)
            self.assertEqual(rd(os.path.join(rot, "payload.bin")), V["V_KEYS_PAYLOAD"])
            self.assertEqual(rd(os.path.join(rot, "manifest.bin")), V["V_KEYS"][:128])
            info = json.loads(rd(os.path.join(rot, "keys.json")))
            self.assertEqual((info["node"], info["seq"], info["signer"], info["keep_retired"]),
                             ("30:ed:a0:00:00:01", 4000, fp, True))
            self.assertEqual(info["release"], release.fingerprint(V["V_KEY2"]))
            self.assertEqual(os.stat(os.path.join(rot, "manifest.bin")).st_mode & 0o777, 0o600)

            r = sign(wrong)  # not the node's recovery key
            self.assertNotEqual(r.returncode, 0)
            self.assertIn(b"not the node's recovery key", r.stderr)
            self.assertFalse(os.path.exists(os.path.join(rot, "release.bin")))
            r = sign(key)
            self.assertEqual(r.returncode, 0, r.stderr)
            out = rd(os.path.join(rot, "release.bin"))
            self.assertEqual(len(out), 192 + release.KEYS_LEN)
            self.assertEqual(out[:128], V["V_KEYS"][:128])
            verify(release.public_raw(rec_now), out[:128], out[128:192])
            self.assertEqual(out[192:], V["V_KEYS_PAYLOAD"])

            # a payload changed after prepare: refused
            p = bytearray(V["V_KEYS_PAYLOAD"])
            p[6] = 0
            wr(os.path.join(rot, "payload.bin"), bytes(p))
            self.assertNotEqual(sign(key).returncode, 0)
            # a seq not above the node's last: refused
            self.assertNotEqual(prepare("--seq", "10", "--last-seq", "10").returncode, 0)
            # by default the seq is this clock's
            self.assertEqual(prepare().returncode, 0)
            self.assertGreater(json.loads(rd(os.path.join(rot, "keys.json")))["seq"], 1700000000000)


if __name__ == "__main__":
    unittest.main()
