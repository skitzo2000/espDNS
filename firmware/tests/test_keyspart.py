"""tools/keyspart.py: the keys partition records it packs and reads, against
tests/keys_vectors.json (which main/keys.c checks in test_core), and its command line. Run by
`make test`."""
import base64
import json
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
TOOLS = os.path.join(HERE, "..", "tools")
sys.path.insert(0, TOOLS)
import keyspart as kp  # noqa: E402

VECTORS = json.load(open(os.path.join(HERE, "keys_vectors.json")))
KEYS = {k: bytes.fromhex(v) for k, v in VECTORS["keys"].items()}


def spki_pem(pub):
    der = kp._SPKI_PREFIX + pub
    b = base64.b64encode(der).decode()
    return ("-----BEGIN PUBLIC KEY-----\n" + "\n".join(b[i:i + 64] for i in range(0, len(b), 64)) +
            "\n-----END PUBLIC KEY-----\n").encode()


class KeysPartTest(unittest.TestCase):
    def test_vectors_current(self):
        out = subprocess.run([sys.executable, os.path.join(HERE, "gen_keys_vectors.py")], capture_output=True,
                             check=True).stdout
        self.assertEqual(json.loads(out), VECTORS, "keys_vectors.json is stale: run gen_keys_vectors.py")

    def test_packs(self):
        for v in VECTORS["packs"]:
            with self.subTest(v["name"]):
                rec = kp.pack(KEYS[v["release"]], KEYS[v["recovery"]], v["generation"], v["source"], int(v["seq"]),
                              KEYS[v["retired"]] if v["retired"] else None)
                self.assertEqual(rec.hex(), v["record"])
                self.assertEqual(len(kp.partition(rec)), kp.PART_SIZE)

    def test_parses(self):
        for v in VECTORS["parses"]:
            with self.subTest(v["name"]):
                self.assertEqual(kp.parse(bytes.fromhex(v["record"]))[1], v["error"])

    def test_picks(self):
        for v in VECTORS["picks"]:
            with self.subTest(v["name"]):
                a = kp.slot(bytes.fromhex(v["a"])) if v["a"] else b"\xff" * kp.SLOT_SIZE
                b = kp.slot(bytes.fromhex(v["b"])) if v["b"] else b"\xff" * kp.SLOT_SIZE
                state, which, got = kp.pick(a, b)
                self.assertEqual((state, which), (v["state"], v["slot"]))
                self.assertEqual(v["seed"], state in ("blank", "foreign"))
                if v["generation"] is not None:
                    self.assertEqual(got["generation"], v["generation"])

    def test_pack_refuses(self):
        k0, k1 = KEYS["k0"], KEYS["k1"]
        for args in [(k0, k0), (k0, k1[:-1] + bytes([k1[-1] ^ 1])), (k0, k1, kp.GEN_INVALID), (k0, k1, -1)]:
            with self.assertRaises(ValueError):
                kp.pack(*args)

    def test_cli(self):
        with tempfile.TemporaryDirectory() as d:
            rel, rec = os.path.join(d, "release.pub"), os.path.join(d, "recovery.pem")
            open(rel, "wb").write(KEYS["k0"])
            open(rec, "wb").write(spki_pem(KEYS["k1"]))
            out = os.path.join(d, "keys.bin")
            subprocess.run([sys.executable, os.path.join(TOOLS, "keyspart.py"), rel, rec, out], check=True)
            img = open(out, "rb").read()
            self.assertEqual(len(img), kp.PART_SIZE)
            self.assertEqual(img[:256].hex(), VECTORS["packs"][0]["record"])
            self.assertEqual(kp.pick(img[:kp.SLOT_SIZE], img[kp.SLOT_SIZE:])[:2], ("ok", 0))
            subprocess.run([sys.executable, os.path.join(TOOLS, "keyspart.py"), "--slot", "--generation", "3",
                            "--source", "2", rel, rec, out], check=True)
            img = open(out, "rb").read()
            self.assertEqual(len(img), kp.SLOT_SIZE)
            self.assertEqual(kp.parse(img)[0]["generation"], 3)
            # a private key, a short file, the same key twice: refused
            priv = os.path.join(d, "release.pem")
            open(priv, "wb").write(b"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n")
            for args in [(priv, rec), (rel[:-4] + ".nope", rec), (rel, rel)]:
                r = subprocess.run([sys.executable, os.path.join(TOOLS, "keyspart.py"), *args, out],
                                   capture_output=True)
                self.assertNotEqual(r.returncode, 0, args)


if __name__ == "__main__":
    unittest.main()
