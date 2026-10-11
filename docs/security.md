# espDNS security

Who espDNS defends against, what it protects today, and what it doesn't yet. This covers
the 0.0.x alpha, which can change anything between versions: read it again when you
upgrade.

To report a security problem, see [Reporting a vulnerability](#reporting-a-vulnerability).
How the pieces fit together is in [architecture.md](architecture.md).

## The short version

- Nodes apply a change only if it's **signed** with your release key, whose public half is
  in their keys partition. Anyone can send a node a release; only a valid signature gets it
  applied. Firmware images hold no key, and the project's key never signs anything a node
  takes.
- The controller listens on **localhost only**, needs a **login**, and sends a strict
  **Content Security Policy**.
- Controller-to-node traffic is **signed but not encrypted**. Anyone watching your LAN can
  read it, including a Wi-Fi password in a config.
- A node's flash is **not encrypted**. Anyone holding a board can read it.
- The private **release key** is the most important secret. Whoever has it can change every
  node that trusts it.

## What there is to protect

| Asset | Where it lives |
|---|---|
| Correct DNS answers for your clients | The nodes |
| The nodes' availability | The nodes, and the controller's rolling push |
| The release signing key | The controller's data directory (`keys/release.pem`) |
| The recovery key | Offline, kept by you. Never on the controller |
| The controller login | The data directory (`auth.json`, an argon2id hash) |
| Wi-Fi passwords of Wi-Fi nodes | Node configs in the data directory; the node's flash |
| What your clients look up | Each node's query log (RAM), the controller's Query log page (RAM) |
| Your zone data | Your zone primary, the nodes' SD cards, the data directory |

## The keys decide who can change a node

Every change to a node is a signed release (see
[architecture.md, Signed releases and pins](architecture.md#signed-releases-and-pins)):

- A node trusts two **public** keys in its keys partition (two slots with a CRC, apart from
  the firmware): the release key (slot 0) and the recovery key (slot 1). Install over USB
  writes them. Firmware images hold no key and no secret.
- A node accepts a release only if one of those keys signed it, it names this node and this
  chip image, and its sequence number is above the last one taken for that kind.
- The controller signs with the **private release key**. The **private recovery key** stays
  offline. It exists to replace a lost or leaked release key: a **KEYS release** signed with
  it rewrites the node's keys ([Key rotation](#key-rotation)). The release key can never
  sign one.

So your whole fleet's security rests on the private release key: firmware signed with it
can do anything on a node, including change which keys it trusts.

### Key rotation

Over the network, a node's keys change only through a KEYS release (`/release`, kind 7):

- It must be signed with the node's current **recovery** key. One signed with the release
  key is refused, however valid its signature.
- It must name this node by its ID (never "any node"), its chip image, and a sequence
  number above the last KEYS release it took, so it can't be replayed or sent to another
  node.
- It carries a new release key and a new recovery key (either may stay the same). The node
  writes them into its unused keys slot with the next generation number, reads the slot
  back, and only then uses the new keys, at once, without a reboot. A power cut during the
  write leaves the old slot in use.
- A node in setup mode (no keys) refuses it: its keys are written over USB only.

What the node already stores (config, hosted zones, blocklist, overrides) was signed with
the old release key and is checked again at every boot. A routine rotation keeps the old
key as the **retired key**, so those copies still load until the controller re-signs and
pushes them. The retired key is never accepted for anything pushed, and the next rotation
replaces it.

A rotation after a **compromise** keeps no retired key. At the next boot the node's stored
config, zones and lists fail their check, and it runs on board defaults and its built-in
address until they're pushed again with the new key (the controller does that right after
the rotation).

**A rotation only wins if it reaches a node before the attacker does.** With the leaked
release key, a firmware can change which keys a node trusts. Rotate at once.

### Rotating keys

The controller prepares a rotation and pushes it. The recovery key signs it, offline, and
never reaches the controller. The steps are in
[operations.md, Rotating keys](operations.md#rotating-keys).

1. **Prepare** (`espdns key rotate prepare`, or Rotate keys in the account panel). For
   every node in `settings.json`, the controller writes an unsigned KEYS release into
   `keys/rotation/<id>/` in the data directory, addressed to the node ID pinned to the
   node's address, for the chip image its `/status` reports, with the next KEYS seq. It also
   makes a new release key there, `keys/release.next.pem` (mode 0600). The files are the
   bytes `firmware/tools/release.py keys-prepare` writes, so either tool can sign them.
2. **Sign, offline.** `espdns key rotate sign` runs in a container with no network and only
   the rotation's directory mounted. It reads the recovery private key from standard input
   or a file (PKCS#8 or SEC1 PEM, or one line of base64 of either). It refuses any key but
   the recovery key every node in the rotation trusts, shows each node and its new key
   fingerprints, and writes each node's signed `release.bin`. It writes the key nowhere.
3. **Push** (`espdns key rotate push`, or Push in the account panel), one node at a time
   under the fleet lock. For each node:
   - its KEYS release, checked to be signed with the node's recovery key before it's sent;
   - its `/status` read again, which must report the new keys;
   - its stored config, hosted zones, overrides and blocklist signed again with the new
     release key and pushed;
   - its health.

   The first failure stops the push. Nodes already done stay done (the rotation's
   `plan.json` records them), and pushing again carries on from the node that failed.
4. **Swap.** When every node in `settings.json` reports the new keys, `release.next.pem`
   becomes `release.pem`, the old key is kept as `release.prev.pem`, and `recovery.pub` holds
   the new recovery key.

Part way through a rotation, every push signs each node with the key it trusts (slot 0 of
its `/status`): the new key for nodes done, the old one for the rest. The Nodes page shows
**Old keys (rotation pending)** for a node not on the new keys yet, and **Needs keys** for a
node in setup mode.

**Routine or compromised.** Mark a rotation compromised with `-compromised` (or the box in
the panel): it keeps no retired key and needs a new release key. A node that stores
something the controller can't re-sign (it has no copy of those bytes, for example a config
pushed by another tool) is then refused before anything is sent, because it would fail at
its next boot. Push those from the controller first.

The older copies a node keeps in its second slots, for a revert, are not re-signed: they
load after a routine rotation while the retired key is there, but not after a compromise.

**Importing a release key.** The controller only imports a release key the fleet trusts:
it must match the public key file given (`-pub`), or a node listed in `settings.json` must
report it in slot 0. With no node listed and no file given, a new fleet's first key is
taken (no node could vouch for one, and none would refuse it). It refuses the recovery key.
The key is read from standard input, never from an argument or an environment variable.
From `controller/`:

```sh
docker compose run --rm -T -v "$HOME/espdns-keys:/mykeys:ro" --entrypoint /espdns espdns-controller \
  key import -data /data -pub /mykeys/release.pub -recovery-pub /mykeys/recovery.pub < release.pem
```

**Making keys on the first run.** Make your keys can generate the release key in the
controller (`espdns key generate`), only while no node is listed and never over an existing
key. It makes the recovery key in your browser (WebCrypto): the page downloads
`recovery.pem` and sends the controller only the public half, kept as `keys/recovery.pub`.
The private recovery key never reaches the controller. The recovery public key can be
replaced from the page only on purpose and only while no node is listed; with nodes listed
it must be the one they report. After that, the controller changes which keys a node
trusts only by pushing KEYS releases you signed offline ([Rotating keys](#rotating-keys)),
and then updates `recovery.pub` itself.

Without both keys the controller prepares no USB install. A node with no keys in its keys
partition (or a damaged keys record) is in **setup mode**: it answers DNS but refuses every
release and never takes keys over the network. Its keys are written over USB only
([operations.md, A node without keys](operations.md#a-node-without-keys-setup-mode)).

### The project's releases

The project publishes key-neutral chip images. Their `SHA256SUMS` is signed with the
project's key (`keys/project.pub`, `/keys/project.pub` in the controller's image) by a
person, never by CI ([releasing.md](releasing.md)). No node trusts that key.

The controller takes a release only if the project's key or your own release key signed it
(Get firmware and `espdns release fetch` take the project's only), and re-checks the chain
(signature, sums, tarball, and the app it sends) before every firmware update, then signs
the update with your release key. A release swapped or changed after signing is refused
([releasing.md, Using a release](releasing.md#using-a-release)).

**What this doesn't cover:** a malicious or broken release that the project's key really
signed. Your controller would accept it and sign it for your nodes. To limit that:

- check a release before you use it ([releasing.md, Checking a
  release](releasing.md#checking-a-release)) with a controller image built from the source;
- or build your own firmware ([Developer guide](developer.md#build-your-own-firmware));
- update one node first.

Every firmware boots on trial and rolls back if it doesn't come up healthy, and the
controller updates one node at a time.

The controller also refuses firmware that would leave a node without the keys it trusts:
an image that doesn't read the keys partition (a build from before it, with keys built in),
any image for a node in setup mode, and, for a node on firmware from before the keys
partition, an image whose built-in seed isn't that node's keys.

## Threat model

espDNS is built for a home or small-office network. Four attackers are considered.

### An attacker on your LAN

A device on the nodes' network: a compromised laptop, a smart TV, a guest.

**What they can do today:**

- **Send DNS queries.** Nodes answer anyone who can reach them; there's no client access
  list. A node can be used to amplify traffic towards a spoofed source address.
- **Read node status.** `/status`, `/health` and `/metrics` are open to anyone who can reach
  the node: ID, firmware, config name, zone names and counters, but no secrets.
- **Read the query log.** `/querylog` answers any private (RFC 1918) or shared (RFC 6598)
  source address, which on your LAN means anyone. Set `"querylog": {"client": "subnet"}` or
  `"hidden"` in the node config to store less, or `"enabled": false` to turn it off.
- **Watch controller-to-node traffic.** It's plain HTTP: configs, zone bundles and lists
  can be read in transit, and a Wi-Fi node's config holds its Wi-Fi password.
- **Change forwarded answers on the path.** Queries to your forwarders are plain DNS (no DNS
  over TLS), and the node doesn't validate DNSSEC itself.
- **Copy your secondary zones.** Your primary allows zone transfers by network, with no
  TSIG, so any device on an allowed network (or one that takes an address there, for
  example with ARP spoofing) can copy those zones.

**What they can't do:**

- **Change a node.** Every release is checked against the node's public keys before
  anything is written. A wrong signature, node, chip image, or an old sequence number is
  refused. Replays and downgrades are refused even after a reboot.
- **Push to a node from outside your network.** Nodes accept releases (`POST /release`,
  `POST /ota`) and serve `/querylog` only from private source addresses.
- **Lock a node out with a huge sequence number.** Once its clock is set, a node refuses a
  sequence number more than 24 hours past its clock. The controller never signs one more
  than 24 hours past its own.
- **Trick the controller into signing for the wrong node.** It signs only for the node ID
  pinned to an address at adoption, with sequence numbers from its own record, never from
  `/status`. If a different node answers at that address, the push is refused.
- **Tie up a node's HTTP server.** Each request has one deadline (5 seconds for the headers,
  then a time based on size). A release's signature is checked before it takes the update
  lock, and only one release is received at a time.
- **Tie up the DNS workers with a slow forwarder.** No worker waits on a forwarder, and the
  pending table caps what one forwarder group can use.
- **Easily forge forwarder answers from off the path.** Each upstream query uses a random
  source port and ID.
- **Confuse the controller with mDNS.** mDNS answers are hints. Loopback, link-local,
  multicast and broadcast addresses are ignored, at most 64 nodes are listed from mDNS
  alone, and an answer never renames or moves a known node.
- **Reach the controller's web UI.** It listens on localhost only.
- **Push zone data with NOTIFY.** A node takes NOTIFY only from its configured primary's
  address, and a NOTIFY carries no data: it only makes the node check the zone with its
  primary.

### A malicious web page

A page open in the browser of someone who runs the controller, trying cross-site requests
or DNS rebinding (pointing its own host name at a node or the controller).

**What protects you:**

- **The controller checks every request's `Host`.** It must be a localhost name. A change
  must not carry another site's `Origin`. CORS is never answered.
- **Sessions have two halves:** an `HttpOnly`, `SameSite=Strict` cookie plus a token the
  page keeps and sends in the `X-Session-Token` header. Another site can't send that header,
  and another program on another localhost port can't read the token. The cookie alone opens
  no API.
- **A strict Content Security Policy** on every response: only the controller's own scripts
  and styles, no inline scripts, connections only to the controller, no framing. The
  pages that install over USB (Nodes and the first run) also allow exactly two hashed
  inline styles from ESP Web Tools, which is bundled with the controller, not loaded from a
  CDN.
- **Other headers:** `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: no-referrer`.
- **Nodes check `Host` too.** A node serves a request only if its `Host` is the node's IP
  address, its mDNS name, or the `name` in its config; anything else gets `421 Misdirected
  Request`. A rebound page can't read `/status` or `/querylog` through your browser.
- **Blocklist downloads can't be turned against your network.** URL sources must be https
  and connect to public addresses, checked at each connection, after name lookup and after
  every redirect. A redirect or DNS rebinding can't reach the controller, the nodes or
  other LAN hosts. A list server inside your network is fetched only if you add its host to
  `settings.json`'s `"internal_sources"`.

### A compromised controller host

Someone controls the machine that runs the controller, or its data directory.

**This is the most serious case.** The data directory holds the release key. With it, an
attacker can sign any release for any node that trusts it, including firmware. They can
also read node configs with Wi-Fi passwords, and the query logs.

**What limits the damage:**

- **The recovery key is never on the controller.** The controller refuses to import it. If
  the release key leaks, rotate to a new release key as a compromise, from a controller you
  trust again: signed offline with the recovery key, with no retired key ([Rotating
  keys](#rotating-keys)). This works only while a node still trusts your recovery key: a
  firmware signed with the leaked release key could change that. Act quickly.
- **The data directory is private.** Every directory is mode 0700 and every file 0600,
  whatever the umask, and at start the controller removes group and other access from
  anything it owns there. The key file is refused if it's open to others, owned by someone
  else, or a symbolic link.
- **The container is hardened.** It runs as your user, not root, with every Linux
  capability dropped, `no-new-privileges`, a read-only root filesystem, a small `noexec`
  `/tmp` in memory, and memory and process limits. It has no Docker socket and no
  toolchain. Its image is built from an allowlist of the source, never the data directory.
- **No credential for your zone primary is stored.** The controller only sends it DNS
  queries: an SOA lookup when you add a zone, and one zone transfer (AXFR) when you open a
  copied zone's records ([zones.md](zones.md)). An older controller's API token in
  `keys/primary.token` is deleted at start, with a log line; revoke it on the primary too.
  Backups never include it, and a restore skips one an older backup carries.
- **Secrets stay out of logs.** Wi-Fi passwords and the passphrase are never written to a
  page, a reply, the action log or a job record. A backup, or showing a config's Wi-Fi
  password, needs the login password again.
- **Backups are encrypted.** `espdns backup` writes an age-encrypted file, to a passphrase
  (at least 12 characters) or an age key.

**Other users on the same machine** can reach the port, but still need the login. Failed logins back off per client: after 3
failures in a row, each further failure locks that client out for 1 second, doubling up to
60 seconds. Your own browser is tracked separately, so failures from elsewhere don't lock
you out.

**Until you set a password, the controller is read-only.** It shows nodes but refuses every
change. The first password can be set from the browser only from the same machine, and only
while none is set.

### A stolen board

Someone takes a node, or gets a few minutes with it and a USB cable.

**What they get:** the ESP32's flash isn't encrypted and secure boot isn't enabled. Over USB
they can read everything on the board (the node config, a Wi-Fi password from the config or
the Wi-Fi setup in NVS, the SD card's zones and blocklists) and flash their own firmware
onto it.

**What they don't get:** any private key. The board can't sign anything, so it can't be
used to change your other nodes.

**What to do:**

1. Change the Wi-Fi password, if it was a Wi-Fi node.
2. Your zone primary still allows zone transfers from the node's network. A board taken
   away can't copy them from elsewhere, but treat any device on that network as able to.
3. Remove the node from `settings.json`. Its pin in `pins/` stays until you replace it; pin
   the replacement board with `espdns pin` (see
   [architecture.md](architecture.md#signed-releases-and-pins)).
4. Treat any secondary zones it carried as read by the thief.

## What is protected

| Protection | What it does |
|---|---|
| **Signed releases** | ECDSA P-256 over every release. Bound to one node, one chip image, and a sequence number. The payload's SHA-256 is checked as it streams in. |
| **Signed project releases** | `SHA256SUMS` signed by a person with the project's key (never in CI, trusted by no node); provenance checked before every update ([The project's releases](#the-projects-releases)). |
| **Keys partition** | Two public keys apart from the firmware, two slots with a CRC and a generation; written over USB or by a recovery-signed KEYS release. No keys or a damaged record: setup mode. |
| **Pins** | The controller signs only for the node ID pinned to an address, with its own record of sequence numbers. |
| **Sequence bound** | Nodes refuse sequence numbers more than 24 hours ahead of their clock once it is set; signers never sign one. |
| **Firmware rollback** | A new firmware boots on trial and rolls back if it does not come up healthy. |
| **Node HTTP guard** | `Host` check against DNS rebinding, one deadline per request, releases verified before they take any lock, JSON-escaped output. |
| **Release endpoints** | `POST /release`, `POST /ota` and `/querylog` only from private source addresses. |
| **Forwarding** | Random source port and ID per upstream query; no worker waits on a forwarder; per-group caps. |
| **Controller on localhost** | Refuses to listen on any other address. |
| **Controller CSP and headers** | Strict CSP, no inline scripts, no framing, no referrer, no CORS; ESP Web Tools bundled, not from a CDN. |
| **Sessions** | argon2id password hash; `HttpOnly` `SameSite=Strict` cookie plus a header token; 2 hours idle, 12 hours at most; all sessions end on restart or password change; login back-off; password asked again for backups and revealed Wi-Fi passwords. |
| **No zone primary credential** | The controller sends the primary DNS queries only; it stores no token for it, and deletes an old one at start. |
| **SSRF guard** | Blocklist URL sources are https from public addresses only, checked at every connection; internal list servers only from an explicit allowlist. |
| **mDNS treated as a hint** | Bad addresses ignored, entries capped, known nodes never moved or renamed by an answer. |
| **File modes** | Data directory 0700, files 0600, fixed at start; secret files refused if they are open to others or links. |
| **Container hardening** | Non-root, no capabilities, `no-new-privileges`, read-only root, `noexec` `/tmp`, memory and process limits. |
| **Secrets in, never out** | The password, release key and backup passphrase are read from standard input or a file, never from arguments; never logged or shown. |

## What is not protected yet

Known gaps in the 0.0.x alpha:

- **Controller-to-node traffic is plain HTTP.** Signed, so it can't be changed, but readable
  on the path, including a Wi-Fi node's password in its config (wired nodes have none).
  Signed reads of `/status` and `/querylog` are planned but not built.
- **No DNS over TLS.** Nodes speak plain DNS on port 53, to clients and to forwarders.
- **No client access list,** and responses can be larger than queries. Do not expose
  nodes to the internet.
- **`/status`, `/health` and `/metrics` are open**, and `/querylog` to any private address.
- **No flash encryption, secure boot or anti-rollback** on the boards.
- **Zone transfers are not authenticated** (no TSIG). Your primary allows the nodes by
  network.
- **A release may target "any node".** The controller signs for one node each time, but a
  node accepts an any-node release for every kind except config.
- **Off-path spoofing** of forwarder and SOA replies is limited by the random ID and source
  port. More hardening is planned.
- **A compromise rotation needs the controller's copies** of what each node stores, and
  revert copies stop loading ([Rotating keys](#rotating-keys)).

## Running it safely

- **Keep the release key off the controller host when you can.** Import it when you need
  it. Keep the recovery key offline, never on the controller host.
- **Do not expose the controller.** It listens on localhost on purpose. To use it from
  another machine, use an SSH tunnel rather than forwarding the port.
- **Do not expose the nodes to the internet.** Keep port 53 and port 80 on your LAN.
- **Put the nodes and the controller on a trusted network segment** if you can, since
  controller-to-node traffic is not encrypted.
- **Prefer wired nodes.** Then no Wi-Fi password is in any config.
- **Use `"querylog": {"client": "subnet"}` or `"hidden"`** if you don't need full client
  addresses, or turn the query log off.
- **Back up the data directory** with `espdns backup`, and store the backup away from the
  host.
- **Check a release before you use it.** See [releasing.md, Checking a
  release](releasing.md#checking-a-release).

## Reporting a vulnerability

Please do not report security problems in a public issue. Follow the steps in
[SECURITY.md](../SECURITY.md) to report one privately.
