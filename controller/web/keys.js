// The keys in the browser (the first run's Make your keys, the account panel): the recovery
// key made here with WebCrypto (ECDSA P-256), its private half offered as recovery.pem and
// never sent anywhere, its public half (the 65-byte point 0x04 || X || Y, as
// firmware/keys/recovery.pub holds it) for POST /api/keys/recovery; a public key file read
// (raw, or a PEM public key); fingerprints as /status names keys (the first 8 bytes of the
// SHA-256 of the 65-byte point, hex); a file handed to the browser to save.
import { h } from "./shell.js";

// The DER of a P-256 SubjectPublicKeyInfo up to the point (tools/keyspart.py _SPKI_PREFIX).
const SPKI_PREFIX = [0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01,
  0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00];

export const hex = b => [...new Uint8Array(b)].map(x => x.toString(16).padStart(2, "0")).join("");
export const base64 = b => btoa(String.fromCharCode(...new Uint8Array(b)));

// canMake: WebCrypto is there (a secure context: this machine, or https).
export const canMake = () => !!(globalThis.crypto && crypto.subtle && globalThis.isSecureContext);

// fingerprint of a 65-byte point, as /status and the controller give it.
export async function fingerprint(raw) {
  return hex(await crypto.subtle.digest("SHA-256", raw)).slice(0, 16);
}

// pem wraps DER in PEM armour, 64 columns.
function pem(label, der) {
  const b = base64(der);
  const lines = b.match(/.{1,64}/g) || [];
  return `-----BEGIN ${label}-----\n${lines.join("\n")}\n-----END ${label}-----\n`;
}

// makeRecovery makes a recovery key pair here: {pem: the private key as PKCS#8 PEM (what
// tools/keygen.py and release.py read), pub: the 65-byte public point, fp}. The private key
// is exported once, for the file; nothing here sends it anywhere.
export async function makeRecovery() {
  const kp = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
  const [pkcs8, raw] = await Promise.all([crypto.subtle.exportKey("pkcs8", kp.privateKey), crypto.subtle.exportKey("raw", kp.publicKey)]);
  const pub = new Uint8Array(raw);
  if (pub.length !== 65 || pub[0] !== 4) throw new Error("the browser gave a public key that isn't an uncompressed P-256 point");
  return { pem: pem("PRIVATE KEY", pkcs8), pub, fp: await fingerprint(pub) };
}

// readPublic reads a public key file: 65 raw bytes (recovery.pub), or a PEM public key. A
// private key file is refused (and read no further than its first line): only the public
// half is given to the controller. The point is checked by importing it.
export async function readPublic(file) {
  const buf = new Uint8Array(await file.arrayBuffer());
  let pub = buf;
  const head = new TextDecoder().decode(buf.slice(0, 64)).trim();
  if (head.startsWith("-----BEGIN")) {
    if (/PRIVATE KEY/.test(head)) throw new Error("That's a private key. Give the public half (recovery.pub): the private one stays offline.");
    if (!head.startsWith("-----BEGIN PUBLIC KEY-----")) throw new Error("Not a public key file.");
    const body = new TextDecoder().decode(buf).split("\n").filter(l => l && !l.startsWith("-----")).join("");
    let der;
    try { der = Uint8Array.from(atob(body.trim()), c => c.charCodeAt(0)); } catch { throw new Error("Not a PEM file."); }
    if (der.length !== SPKI_PREFIX.length + 65 || SPKI_PREFIX.some((x, i) => der[i] !== x)) throw new Error("Not a P-256 public key.");
    pub = der.slice(SPKI_PREFIX.length);
  }
  if (pub.length !== 65 || pub[0] !== 4) throw new Error("Not a P-256 public key: 65 bytes (recovery.pub), or a PEM public key.");
  if (canMake()) {
    try { await crypto.subtle.importKey("raw", pub, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]); } catch {
      throw new Error("Not a point on P-256: the file is damaged, or not a key.");
    }
  }
  return pub;
}

// save hands a file to the browser to save.
export function save(data, name, type = "application/octet-stream") {
  const a = h("a", { href: URL.createObjectURL(new Blob([data], { type })), download: name, class: "offscreen" });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(a.href), 60_000);
}
