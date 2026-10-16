# Changelog

What changed in each version of espDNS. The node firmware and the controller each have their
own version (`firmware/VERSION`, `controller/VERSION`) and their own sections
(`## [firmware 0.0.3]`, `## [controller 0.0.3]`); 0.0.1 and 0.0.2 covered both under one
version. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the versions
[Semantic Versioning](https://semver.org/spec/v2.0.0.html): 0.0.x while in alpha, when any
version may change anything. Write each change under Unreleased as it merges;
`scripts/bump-version.sh` makes that section the next version's ([docs/releasing.md](docs/releasing.md)).

## [Unreleased]

### Fixed

- **+ Add node** no longer adds a node that trusts another controller's release key. It says
  why, and the node is installed again over USB, which writes your keys.
- The setup wizard keeps **Make the release key** when a node is listed before the keys
  exist, and says why it matters: that node trusts the key it was installed with, so it is
  installed again over USB to take the new one. Before, the step offered only importing a key.
- The node now refuses a config whose status light is on a strapping, USB or serial-console
  pin (ESP32-S3: 0, 3, 45, 46, 19, 20, 43, 44; ESP32-P4: 34 to 38, 24, 25), as the
  controller does. It accepted them before from anything but a current controller. The push
  is refused with the reason, such as `config refused: led: GPIO 45 is a strapping pin`. A
  pin the board's own LED already uses stays allowed, and board definitions are not checked.
  A config already on the node keeps loading at boot.
- A rollout or adoption no longer counts a node found on the network but not added (one
  another controller adopted, say) as a node that must keep answering, and `-all` no longer
  changes it. Only added, adopted nodes count. Nor does an added node that trusts another
  release key (one another controller adopted): its state no longer holds up a rollout or a
  restart from the controller.
- **Write keys only** now restarts the board when it is done. Before, the board could stay in
  its bootloader until you unplugged it.
- A node's page no longer offers Restart, Update firmware or the quick fixes for a node not
  added yet, or one listed but not adopted; the controller refuses them for such a node too.
- A firmware update waiting to apply is dropped for a node that already runs that version,
  such as one installed again over USB. Its row no longer reads `0.0.4 → 0.0.4`.
- A firmware update waiting to apply is dropped for a node online that already runs that
  version, such as one installed again over USB. Its row no longer reads `0.0.4 → 0.0.4`.

## [controller 0.0.6] - 2026-10-16

### Changed

- Installing a board and adding a node are now two steps. Nodes has its own **Install over
  USB** button, and **+ Add node** lists only nodes on your network. Once an installed board
  answers, **Add it now** opens + Add node with it and its address filled in. A node's page
  has **Reinstall over USB**.

## [controller 0.0.5] - 2026-10-15

### Fixed

- The controller now refuses a node's status light on a strapping, USB or serial-console pin
  (ESP32-S3: 0, 3, 45, 46, 19, 20, 43, 44; ESP32-P4: 34 to 38, 24, 25). It accepted them
  before, and an LED there can change the boot mode at reset or cut off USB logs, Improv and
  the console. The node page shows the refusal in the *Status light* row when you save. A pin
  the board's own LED already uses stays allowed. Board definitions still only warn about
  these pins. A node whose config already sets such a light has its next config push or
  settings save refused with that message: move the light to another pin, or pick **Its
  board's** to go back to the board's light. See
  [docs/hardware.md](docs/hardware.md#a-status-light).
- In + Add node, a grey Add node button now says why on hover: the name to pick, the release
  key to make, or what's wrong with the address. The new name is lowercased as you type, and
  the line under the form no longer reads `Saved to its.json` before there is a name.

## [firmware 0.0.4] - 2026-10-15

### Fixed

- A node installed again over USB with new keys, its SD card kept, no longer stays
  `degraded` because of the blocklist, allowed and blocked sites or hosted zones left on the
  card from before. Signed with keys it no longer trusts, they can never load, so at boot the
  node now deletes those stored list files (only its own, in `dns2/block` and `dns2/hosted`;
  never the card or anything else on it). It shows that list as off with the note
  `removed a copy signed with a key this node doesn't trust (from an earlier install)`, stays
  healthy, and gets the list back at the controller's next push. The same happens after a key
  rotation following a compromise. A file it can't read or that is damaged is kept and
  reported as failed, as before. See
  [docs/hardware.md](docs/hardware.md#what-espdns-keeps-on-the-card-and-what-it-deletes).

## [controller 0.0.4] - 2026-10-15

### Added

- The controller can run behind your own HTTPS reverse proxy, so any browser on your
  network can use it (and install over USB from its own machine). Set `ESPDNS_URL` (the
  proxy's `https://` address for it), `ESPDNS_PROXY` (the proxy's source addresses) and
  `ESPDNS_LISTEN` (`0.0.0.0:8480`) in `.env`, or `-url`, `-proxy` and `-listen`. It then
  serves only the proxy and its own machine, only by that name and origin, with a `Secure`
  session cookie; the login back-off counts the address the proxy adds to
  `X-Forwarded-For`. Without them nothing changes: localhost only. See
  [docs/security.md](docs/security.md#behind-a-reverse-proxy).

## [controller 0.0.3] - 2026-10-14

**Breaking: install your nodes again.** A node on 0.0.1 or 0.0.2 can't update to 0.0.3.
Update the controller as usual, then install each node from it with **Install over USB**
(**+ Add node**, on Nodes).

### Added

- Nodes answer DNS only from private addresses: UDP and TCP port 53 take queries from
  IPv4 `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `169.254/16` and `127/8` and IPv6
  `::1`, `fc00::/7` and `fe80::/10` (an IPv4-mapped source by its IPv4 address), and drop
  anything else unanswered, so a node on a public address is no open resolver or
  reflector. The node config's new `allow` key adds up to 16 more networks (IPv4 or IPv6,
  CIDR), live; the controller checks it, and refuses it for a node whose firmware is older.
  The dropped queries are counted in `/status` `queries.refused_source` and `/metrics`
  `espdns_queries_refused_source_total`. See
  [docs/reference/node-config.md](docs/reference/node-config.md#allow).
- A status light on any board: a plain RGB LED on three GPIOs (common anode or cathode),
  driven with PWM, shows the node's health in the controller's colours (green, amber, red;
  blue while booting or updating; white to *Blink its light*). The node page gets a
  **Status light** setting (None, one-colour LED, WS2812 or RGB LED, with its pins), which
  shows the board's own LED and goes out with the other changes to apply; the node changes
  lights live, without a restart. The node config's new `led` key carries it, checked by the
  controller and the node against the board's other pins and the chip's. Board definitions
  take `"kind": "rgb"` too. `/status` `led` now reports the light in effect, its pins, where
  it comes from and the board's own; `failed` is now a field of its own, no longer a kind.
  Wiring in [docs/hardware.md](docs/hardware.md#a-status-light).
- A board library: 18 boards from 6 makers added to the catalog (Adafruit, Espressif,
  LilyGO, M5Stack, Seeed, Waveshare), 22 boards in all, each from the maker's pinout,
  arduino-esp32 or ESPHome, with its sources in `notes`: the wired ESP32-S3 and ESP32-P4
  boards with 8 MB of PSRAM or more and solid pin documentation, and a few Wi-Fi boards
  with an SD slot. Install over USB groups the list by maker and gets a search box (maker,
  model, catalog name, chip image, w5500, poe…); each option says its flash, PSRAM and
  wired or Wi-Fi. The controller loads the catalog once at startup and serves
  `GET /api/boards` from memory with an ETag (304 to `If-None-Match`); your own boards are
  read again when `data/boards` changes or one is saved. espDNS boards have 8 MB of PSRAM
  or more, the sizes tested: a board with less is refused (`psram_mb: espDNS needs at
  least 8 MB of PSRAM`). The library tables are in
  [docs/hardware.md](docs/hardware.md#untested-boards).

### Changed

- The Zones page reads a copied zone's records from a node that holds it (the newest copy,
  through the node's `GET /zone`), and says which node. Your primary is asked by zone
  transfer only when no node can hand them over, so it no longer has to allow transfers to
  the controller's computer.
- `release_url` (and `-release-url`, `espdns release fetch -url`) takes `https` only: an
  `http` URL is refused. Releases were checked with the project's key anyway; https keeps
  what is fetched, or held back, from the network between.
- Releases come from GitHub only: `release_url` is a GitHub repository (a fork's, or one on
  a GitHub Enterprise server).
- Two versions: the firmware and the controller each have their own, in
  `firmware/VERSION` and `controller/VERSION` (the root `VERSION` is gone), and are
  released on their own tags: `firmware-v<version>` builds the chip images,
  `controller-v<version>` the controller's image and `compose.yaml`, each signed as before.
  The controller offers the nodes firmware releases only (`firmware-v*`, and the older
  `v0.0.1` and `v0.0.2`), so a controller release never makes a node out of date. Get
  firmware and `espdns release fetch -version` take a firmware version (`0.0.3`) or its
  tag. A node's `/status` says the protocol it speaks to the controller (`"proto": 1`); a
  node on one the controller doesn't speak is offered no update, and the Nodes page says
  why. `scripts/bump-version.sh` takes the part first (`firmware patch`), and a version's
  changelog section is `## [firmware 0.0.3]` or `## [controller 0.0.3]`
  ([docs/releasing.md](docs/releasing.md)).
- Only the supported chip images are built, released and offered: esp32p4-rev1, esp32p4,
  esp32s3-octal, esp32s3-eth and esp32s3-quad. A board file or import naming another image
  is refused (`not one of the chip images`).
- Updates go by version, whatever the chip. The controller asks GitHub (or `release_url`)
  for the newest firmware release when it starts, every 24 hours and when a new node shows
  up, and keeps only its version, in `firmware/latest`. A node on an older version is
  offered the update; you no longer need Get firmware to see one. Applying it downloads and
  checks the node's chip image of that release if the controller doesn't have it. Per-board
  builds no longer count for updates, and the `unordered` and `no_build` states are gone
  ([docs/operations.md](docs/operations.md#versions)).
- The Nodes page puts each node on one line: its name (the board on hover), a dot, the
  address with a wired or Wi-Fi icon (the MAC on hover), Uptime and Load (q/s). The dot is
  green while the node serves, amber when there's something to do (an update, a reason, a
  node not added yet) and red when it isn't working; hover for its state and reason. A node
  that isn't green gets a short flag, or an amber **Update 0.0.1 → 0.0.2** button that opens
  the Update panel with only it ticked. The banner above the table and the MAC line are
  gone; **Update all** sits by **+ Add node** while two or more nodes are out of date.
- The setup's zone step is about your own zones. "Add your zone", after the second node:
  type a name for your network, like home.example, and host it on espDNS (it starts with
  the records every zone needs); the + Add zone box is the Zones page's. Already run a DNS
  server with your zones? Copying from it is an option there, under "Set it up myself", not
  the step itself as the old "Set up your zone primary" step was. The step is done with a
  zone of your own (hosted here, copied or forwarded, a pending change included) or
  skipped. `GET /api/setup` lists `zones` among the steps, in place of `primary`, and
  counts your zones in `zones`; `POST /api/setup/step` marks `zones` skipped and refuses
  `primary` as it does any other unknown step. An older `setup.json` with `primary` marked
  done loads fine; the mark is ignored.
- Get firmware and `espdns release fetch` download only the chip images needed, not every
  one in the release. Install over USB gets the chosen board's. The account menu and the
  CLI get what your nodes need, from each node's board. If a node hasn't reported yet, they
  also get the chip images the controller already has, and say so. With nothing needed,
  they say so and fetch nothing. The
  signature is still checked first, and other imported chip images stay as they are. The
  `fetch-release` job takes `"images"`, and `release fetch` takes `-image`.

### Security

- The Wi-Fi password no longer crosses the network in plain text. Each Wi-Fi node makes a
  key pair at its first boot (the private key never leaves it) and reports the public key in
  `/status` as `node_key`; the controller pins it when it adopts the node, and sends a
  config's Wi-Fi password only sealed to that key (`wifi.sealed`: ECDH P-256, HKDF-SHA256,
  AES-256-GCM). A node refuses a config release with a plain `wifi.password`. The controller
  refuses to send a Wi-Fi password to a node with no node key pinned (adopt it again) or
  one that reports a different key (reinstalled: adopt it again, or `espdns pin`). Config
  files on the controller still hold the password as before; USB and Improv setup are
  unchanged. See [docs/security.md](docs/security.md#the-wi-fi-password).

## [firmware 0.0.3] - 2026-10-14

**Breaking: install your nodes again.** A node on 0.0.1 or 0.0.2 can't update to 0.0.3.
Update the controller as usual, then install each node from it with **Install over USB**
(**+ Add node**, on Nodes).

### Added

- Nodes answer DNS only from private addresses: UDP and TCP port 53 take queries from
  IPv4 `10/8`, `172.16/12`, `192.168/16`, `100.64/10`, `169.254/16` and `127/8` and IPv6
  `::1`, `fc00::/7` and `fe80::/10` (an IPv4-mapped source by its IPv4 address), and drop
  anything else unanswered, so a node on a public address is no open resolver or
  reflector. The node config's new `allow` key adds up to 16 more networks (IPv4 or IPv6,
  CIDR), live; the controller checks it, and refuses it for a node whose firmware is older.
  The dropped queries are counted in `/status` `queries.refused_source` and `/metrics`
  `espdns_queries_refused_source_total`. See
  [docs/reference/node-config.md](docs/reference/node-config.md#allow).
- A status light on any board: a plain RGB LED on three GPIOs (common anode or cathode),
  driven with PWM, shows the node's health in the controller's colours (green, amber, red;
  blue while booting or updating; white to *Blink its light*). The node page gets a
  **Status light** setting (None, one-colour LED, WS2812 or RGB LED, with its pins), which
  shows the board's own LED and goes out with the other changes to apply; the node changes
  lights live, without a restart. The node config's new `led` key carries it, checked by the
  controller and the node against the board's other pins and the chip's. Board definitions
  take `"kind": "rgb"` too. `/status` `led` now reports the light in effect, its pins, where
  it comes from and the board's own; `failed` is now a field of its own, no longer a kind.
  Wiring in [docs/hardware.md](docs/hardware.md#a-status-light).
- Nodes hand over their copy of a zone: `GET /zone?name=<zone>` on a node returns a
  secondary zone's records as the node holds them, 200 a page (500 at most), from private
  addresses only. `/status` lists `"reads": ["zone"]` on nodes that have it. See
  [docs/reference/node-endpoints.md](docs/reference/node-endpoints.md#zone).
- The Waveshare ESP32-P4-ETH (`ws-p4-eth`) is a tested board.

### Changed

- Two versions: the firmware and the controller each have their own, in
  `firmware/VERSION` and `controller/VERSION` (the root `VERSION` is gone), and are
  released on their own tags: `firmware-v<version>` builds the chip images,
  `controller-v<version>` the controller's image and `compose.yaml`, each signed as before.
  The controller offers the nodes firmware releases only (`firmware-v*`, and the older
  `v0.0.1` and `v0.0.2`), so a controller release never makes a node out of date. Get
  firmware and `espdns release fetch -version` take a firmware version (`0.0.3`) or its
  tag. A node's `/status` says the protocol it speaks to the controller (`"proto": 1`); a
  node on one the controller doesn't speak is offered no update, and the Nodes page says
  why. `scripts/bump-version.sh` takes the part first (`firmware patch`), and a version's
  changelog section is `## [firmware 0.0.3]` or `## [controller 0.0.3]`
  ([docs/releasing.md](docs/releasing.md)).
- Only the supported chip images are built, released and offered: esp32p4-rev1, esp32p4,
  esp32s3-octal, esp32s3-eth and esp32s3-quad. A board file or import naming another image
  is refused (`not one of the chip images`).

### Removed

- The old HMAC key for firmware updates, used only by nodes on firmware from before signed
  releases: `make key-restore`, `secrets/ota.key` and `tools/ota_push.py --legacy-key`.
  `make ota` now refuses such a node; install it over USB.
- Releases for any node: a node no longer takes a release addressed to
  `ff:ff:ff:ff:ff:ff`. Every release must name the node's own ID, whatever its kind.
  Nothing signed such a release.

### Fixed

- Nodes answer a query with an EDNS version other than 0 with BADVERS (RFC 6891), not
  with an answer.
- Nodes answer FORMERR to a query with an OPT record outside the additional section, or
  more than one.
- Nodes pass on forwarded records over 1024 bytes, where they fit, and set TC where they
  don't, instead of answering SERVFAIL. A malformed upstream answer no longer leaves part
  of itself in the SERVFAIL reply.
- `/metrics` counts a forwarder's SERVFAIL, passed on, as `servfail`, not `forwarded`.
- A refused NOTIFY counts as refused too: in `/status` `queries.refused` and `/metrics`
  `espdns_queries_total{result="refused"}`.
- One slow name no longer takes all of its forwarders' places for queries waiting on an
  upstream answer: the queries for one question wait in at most 8 of the 32 places, and
  past that are answered SERVFAIL at once, so queries for other names still wait for theirs.
- Slow TCP clients no longer hold up a node's TCP port 53. A node has 2 TCP workers and a
  queue of 4 connections waiting for them. Once a worker takes a connection, the client has
  2 s to start its first query and 3 s to send each query whole, and is closed when idle
  while other connections wait. A connection may wait in the queue first, so it can be held
  a little longer. One client may hold only 3 of the 6 places. These closes are counted in
  `/status` `queries.tcp_closed` and `/metrics` `espdns_tcp_closed_total`.

### Security

- The Wi-Fi password no longer crosses the network in plain text. Each Wi-Fi node makes a
  key pair at its first boot (the private key never leaves it) and reports the public key in
  `/status` as `node_key`; the controller pins it when it adopts the node, and sends a
  config's Wi-Fi password only sealed to that key (`wifi.sealed`: ECDH P-256, HKDF-SHA256,
  AES-256-GCM). A node refuses a config release with a plain `wifi.password`. The controller
  refuses to send a Wi-Fi password to a node with no node key pinned (adopt it again) or
  one that reports a different key (reinstalled: adopt it again, or `espdns pin`). Config
  files on the controller still hold the password as before; USB and Improv setup are
  unchanged. See [docs/security.md](docs/security.md#the-wi-fi-password).
- Forged upstream replies are harder to slip in: a node asks its forwarders with the
  name's letters in random case and takes only a reply that names it in that very case
  (a forwarder that doesn't keep case is asked once more without it). The SOA check of a
  copied zone now asks from a random port and takes only a reply to the question it asked.
  Clients still see the name in their own case.

## [0.0.2] - 2026-10-11

### Added

- Nodes out of service: the node page's Take out of service, Return to service and Remove
  from the group (the jobs `node-out`, `node-in`, `node-remove`; `espdns node out|in|remove`),
  each usable while the node is offline. A node out of service (`settings.json`
  `"out_of_service"`) stays listed and shown, muted, but an apply, a rollout, a refresh, a
  config push and the fleet check skip it, and the safety rules don't count it. Returning it
  needs it to answer and trust the release key, and sends it what it missed first (config,
  hosted zones, overrides, blocklist, newer firmware), each a dry run then a push. Removing
  one takes it out of `settings.json` only. A key rotation isn't prepared while a node is
  out of service ([operations.md](docs/operations.md#taking-a-node-out-of-service)).

- Key-neutral chip images (summary of the entries below): released firmware is the same
  for everyone and ships prebuilt. A node's two public keys live in its `keys` partition,
  not the firmware; the controller writes them with Install over USB (or Write keys only),
  and a node without them is in setup mode and takes nothing over the network. The first
  run's Make your keys makes the release key in the controller and the recovery key in the
  browser, kept offline. The project's key (`keys/project.pub`, `ceccd5865252c2df`) only
  signs a release's `SHA256SUMS`: the controller fetches a release (Get firmware, `espdns
  release fetch`), checks it, keeps its provenance, and signs every update with your
  release key. Keys change over the network only through a KEYS release signed offline with
  the recovery key (`espdns key rotate`). Nodes from before move over with one seeded build.
  Getting started now needs no git, no ESP-IDF and no Docker build: download the release's
  `compose.yaml`, `docker compose up -d`, and the setup runs in the browser. Building your
  own firmware is optional (docs/developer.md).
- Key rotation from the controller: `espdns key rotate prepare` (or Rotate keys in the
  account panel) writes an unsigned KEYS release for every node in `settings.json` into
  `<data>/keys/rotation/<id>/`, byte for byte what `tools/release.py keys-prepare` writes,
  and a new release key into `keys/release.next.pem`; `espdns key rotate sign` signs them
  offline (`docker run --network none`) with the recovery private key, read from standard
  input or a file (PKCS#8 or SEC1 PEM, or a line of base64 of either), refusing any other
  key and writing it nowhere; `espdns key rotate push` (or the job `key-rotate`) pushes them
  one node at a time under the fleet lock, then signs again with the new key and pushes each
  node's stored config, hosted zones, overrides and blocklist, then checks its health. It
  stops at the first failure and goes on from there when run again. When every listed node
  has the new keys, `release.next.pem` becomes `release.pem` (the old one kept as
  `release.prev.pem`) and `recovery.pub` the new recovery key. `-compromised` keeps no
  retired key on the nodes, and refuses a node storing what the controller can't sign
  again. `espdns key rotate discard` removes a rotation no node took.
- While a rotation is part way, every push signs each node with the release key it trusts
  (`release.Pusher.Alt`): the new key for the nodes that have it, the old one for the rest.
  The Nodes page says "Old keys (rotation pending)" for a node not on the controller's
  (or the rotation's new) keys, and "Needs keys (setup mode)" for a node without keys.
  `/api/key` has `rotation`; `release.NodeStatus` reads `keys_retired`.
- `espdns key import` and the other key readers take a SEC1 PEM ("EC PRIVATE KEY") too.

- The keys partition: the two public keys a node trusts now live in flash apart from the
  firmware (`keys`, 0x1b000, two slots; docs/architecture.md, Signed releases and pins), read at boot,
  so an image can carry none. A seed build (below) has them built in as the seed: a
  node whose partition holds no keys record (erased, or bytes an earlier flash left there,
  which it erases) writes them there at its first boot, keeping the same fingerprints, and
  its stored config, zones and lists still load. A damaged keys record is never seeded over. A node flashed before the
  partition gets the range registered at boot when nothing in its partition table is there,
  so it moves over the air. `/status` has `keys_source` (`partition`, `seeded`, `corrupt`,
  `embedded` or `none`), `keys_gen` and `keys_error`; a trial firmware with no usable keys
  rolls back. `firmware/tools/keyspart.py` packs a partition image from two public keys.
- Key-neutral images: a build carries no public key unless built with `KEYS_SEED=<dir>`,
  which builds in that directory's keys as the keys partition's seed (marker
  `espdns:keys-seed=`); every image carries `espdns:keys=partition-v1`. A release's images
  are key-neutral, and `scripts/release/build-firmware.sh` fails one with a seed or a test
  hook. `make -C controller fleet-firmware` seeds the nodes in service with `firmware/keys`
  (`FLEET_KEYS_SEED=`); `make factory`, `boards`, `export-images` and development builds
  are key-neutral (`INSTALL_KEYS_SEED=keys` for a seed). `firmware/keys/` is no longer
  needed to build.
- Keys at install: Install over USB writes the node's keys partition (slot A: the
  controller's release public key and recovery public key, generation 1; slot B erased),
  and prepares nothing until both keys are there ("set up your keys first"). A chip image
  exported before the keys partition is refused: import a current release.
  `internal/keyspart` packs and reads the partition, byte for byte `tools/keyspart.py`.
  `make factory` (a developer's tool) writes `FACTORY_KEYS`'s keys (default
  `firmware/keys`) the same way (`tools/factory.py --keys`).
- Write keys only (keeps settings), in the Install over USB panel: the keys partition alone,
  one part at 0x1b000, for a node in setup mode. + Add node offers it for a node found with
  no keys (`keys_source` `none` or `corrupt`). The page writes it itself with esptool-js
  (vendored at 0.6.0, the version ESP Web Tools 10.4.0 is built with; `make vendor-etj`):
  one compressed write, no erase, read back, then a reset. No ESP Web Tools dialog, so no
  way to erase the board from it.
- Make your keys, the first run's second step (was Import the release key): the release
  key made in the controller (`POST /api/keys/release`, or `espdns key generate`) or
  imported; the recovery key made in the browser with WebCrypto, `recovery.pem` downloaded,
  and only its public half sent (`POST /api/keys/recovery`, kept as `keys/recovery.pub`), or
  a recovery public key file imported; done when both are there and recovery.pem is said to
  be saved offline. The account panel shows both fingerprints and downloads `release.pub`.
- `espdns key import` takes a new fleet's first release key with no node listed and no
  `-pub` (any key but the recovery key); `-recovery-pub` defaults to the data directory's
  `keys/recovery.pub`.
- Setup mode: a node with no keys (`keys_source` `none`, or `corrupt` on a key-neutral
  build) answers DNS on its board's defaults, reports the health reason `no keys`
  (degraded), and `/ota` and `/release` refuse every release (`403`, "this node has no
  keys"); it never takes keys over the network. A trial firmware in setup mode rolls back.
  The test build `make build TEST_NO_KEYS=1` (`-DDNS2_TEST_NO_KEYS=1`) comes up in setup
  mode and rolls back by itself after 60 s. The controller reads an image's keys markers
  (`release.KeysOf`).
- The release supply chain: the project's release public key is `keys/project.pub` at the
  top of the repository (fingerprint `ceccd5865252c2df`), `/keys/project.pub` in the
  controller's image (`--build-context keys=keys`). It signs a release's `SHA256SUMS` and
  nothing else; no node trusts it. `espdns release import` takes a release signed by the
  project's key (`-pub`, default `/keys/project.pub`) or by the controller's own release key,
  says which, and refuses any other. Each imported chip image keeps its provenance
  (`firmware/images/<image>/provenance/`: the signed `SHA256SUMS`, its signature, the
  tarball and who signed it). Before every firmware update (the Nodes page's, a rolling
  push, `espdns rollout`) the controller checks that chain again, down to the app it sends
  being the one in the signed tarball, and only then signs the update with your release key.
  Your own builds with no release behind them (`firmware/builds/`, a chip image you copied
  in) are sent as they are.
- Firmware updates refuse an image without the keys partition marker (a downgrade to an
  image with keys built in), any update to a node in setup mode (reinstall it, or Write keys
  only), and, for a node on firmware from before the keys partition, any image whose keys
  seed isn't that node's keys.
- `espdns release fetch [-version v0.0.3] [-url <forge>]` and **Get firmware** (Install over
  USB, for a board whose chip image isn't there yet, and the account panel's Firmware): the
  newest published release's chip images, `SHA256SUMS` and its signature downloaded from
  GitHub (or a copy elsewhere: `-url`, `settings.json`'s `release_url`, the
  controller's `-release-url`), checked with the project's key and imported, as a job. The
  pages label each chip image "project release v…", "your build v…" or "your build"
  (`GET /api/firmware`; the Update panel too).
- KEYS releases (kind 7 on a node's `/release`): new release and recovery keys for one node,
  signed with its current recovery key (never the release key), for its node ID alone, with
  a seq above its last KEYS one. The node writes the keys slot it isn't using (the next
  generation), reads it back and uses the new keys at once, no reboot; a power cut during
  the write leaves the old slot in use. A planned rotation keeps the old release key as the
  retired key, which only the node's stored config, zones and lists are checked with, until
  they are pushed again; after a compromise there is none, and they fail at the next boot
  until pushed again. A node in setup mode refuses one. `/status` has `keys_retired` and
  `seq.keys`, `/metrics` `espdns_release_seq{kind="keys"}`. `firmware/tools/release.py
  keys-prepare` and `keys-sign` make one and sign it offline; the controller's rotation is
  above (docs/security.md, Key rotation).
- Releases: pushing a version's tag (`v<VERSION>`, checked against `VERSION` and this file)
  builds its files in CI as a draft release on GitHub: the controller's image (to the GitHub
  container registry, `ghcr.io`), every chip image (generic and key-neutral: no address,
  keys or site defaults), and `SHA256SUMS`. No factory images: a new node is installed with
  the controller's Install over USB, which writes its keys and address. `espdns release
  build` refuses a chip image that isn't key-neutral (no keys partition marker, or a keys
  seed built in). CI never has the release key: a person signs `SHA256SUMS`
  with one docker command, the key on standard input, and anyone checks a release, or
  imports its chip images into the controller, with `espdns release verify` and
  `espdns release import` (docs/releasing.md). The controller's image carries the
  project's release public key at `/keys/project.pub`.
- GitHub Actions workflows (`.github/workflows/`): CI on every push and pull request, and
  the release, with the same pinned images, jobs and checks as before. They need no secret:
  each job uses the run's own token, with only the permissions it needs.
- The forward loop's metrics: `/metrics` has the upstream queries in flight per group
  (`fwd_inflight`, `fwd_zone_inflight`) and their peak, the table's size and a group's cap,
  the queries shed at the caps (`fwd_shed_total`), the upstream queries that went
  unanswered (`fwd_expired_total`), each forwarder's timeouts (`upstream_timeouts_total`),
  the TCP retries and the loop's `select()` errors. The Traffic tiles and `espdns metrics`
  show them.
- A health reason, `forwarders slow`: a node whose default forwarders shed queries,
  hold over half their cap for 10 s, or are mostly slower than `upstream_timeout_ms` is
  degraded instead of healthy. The Nodes page explains it.
- A health reason, `overloaded`: a node that dropped at least a tenth of the UDP
  queries it received over the last 30 s (and at least 64 of them), because every worker was
  busy and the queue full, is degraded instead of healthy. The node page explains it.
  `/metrics` has the time the UDP queue was full (`udp_queue_full_seconds_total`) and the
  queries shed past its deadline (`udp_queue_expired_total`), which `espdns metrics` shows.
- Scheduled blocklist refresh: the controller checks the lists it downloads for a new
  version (every day by default; every 6 or 12 hours, weekly or never, on the Blocking page
  or as `settings.json`'s `blocklist_refresh`), with `If-None-Match` and
  `If-Modified-Since`, and sends a list that changed to the nodes as an apply of it alone:
  the size-change check, every node checked, signed, one node at a time, never the last
  healthy node. An unchanged list is only marked checked. A version over the size-change
  limit isn't sent, and is shown on the Blocking page with **Send it anyway**; a list a
  pending change edits waits for that change's apply. The Blocking page shows when the
  lists were last checked, when next, and each list's last update, with **Refresh now**;
  Recent changes lists each update sent or refused. The schedule resumes from its kept
  times after a restart (`blocking/refresh.json`), with jitter.
- Documentation for the public release: a new README, getting started (your own keys and
  firmware, two nodes, one controller), hardware, zones, blocking, operations,
  troubleshooting, architecture, security, and a reference for the CLI, settings, node
  config, the controller's API and a node's endpoints. CONTRIBUTING.md and SECURITY.md.
  Then rewritten shorter, for new users, with Artie the pigeon: getting started is one
  download and the browser setup; Compose from a checkout, the command line and your own
  builds moved to a developer guide (docs/developer.md).
- `compose.yaml` in every release: `controller/compose.user.yaml` with the release's
  controller image by digest, keys and settings in `./data` beside it as your own files
  (the user from `.env`). Releases are full releases, 0.x too, so
  `releases/latest/download/compose.yaml` always finds the newest.

- A chip image for wired S3 boards, `esp32s3-eth`: `esp32s3-octal` with a 32 KB instruction
  cache and the SPI master and lwIP's packet path in IRAM. The Waveshare ESP32-S3-ETH
  (`ws-s3-eth`) runs it now. On that board, measured with lwIP's core lock as well, it
  answered 78.6% of a 3,000 queries/s flood, not 30.5%, with about 29 KB less internal RAM
  free. S3 boards on Wi-Fi (`xiao-s3-sense`) keep `esp32s3-octal` and its RAM. The two are
  sibling images: a node moves between them over the air and keeps its board definition, config, zones and lists, with no USB
  reflash. A firmware rollout, `make ota` and the Nodes page's update move a `ws-s3-eth`
  node from `esp32s3-octal` to its board's new image.

### Changed

- `firmware/keys/` is no longer in the repository: it is yours (git-ignored; `make -C
  firmware keys` makes a pair there), the optional seed of your own builds and `make
  factory`'s keys. `make -C controller fleet-firmware` seeds from it when it holds keys,
  else builds key-neutral; `key-import` and `key-status` pass it only when it is there.
- W5500 nodes: the W5500's driver is our own copy of ESP-IDF 5.5.5's
  (`firmware/components/w5500`, Apache-2.0), changed to use far fewer SPI transactions.
  A wake takes every frame waiting, up to 4 KB per SPI read, instead of 8 or more
  transactions per frame; an answer goes out in 4 transactions instead of about 12, without
  waiting for the chip to report it sent. On the Waveshare ESP32-S3-ETH at 1000 queries/s,
  answered went from 72% to 100% and the median answer time from 95 ms to 5.6 ms; at 3000/s
  the W5500 no longer drops frames. A board whose optional W5500 isn't fitted still falls
  back to Wi-Fi.
- A UDP query that waited 1 s or more in the queue for the workers is dropped, unanswered,
  instead of answered late: its client has asked again or given up, and answering it only
  delays the queries behind it. Those count towards `overloaded`, and 64 of them in
  30 s make the node `overloaded` on their own.
- The node's HTTP server stays at its default priority (5), below the DNS workers (6):
  raised to 6 on the S3 it cost DNS answers (at 1000 queries/s, 80% answered, 6,042
  dropped, against 100% at 5, p50 9 ms). `/health` may still not answer during a heavy
  flood; when it does, `overloaded` is what it reports.
- Tests, test vectors and examples use documentation addresses only. CI now refuses a
  10/8 address in a test too; a test that needs a private address builds it.
- Zone primaries run the standard way: the nodes copy each zone by zone transfer and poll
  its SOA (NOTIFY optional), and you set the primary up once to allow zone transfers from
  your nodes' networks. Adding a node changes nothing on the primary: adoption's step 4
  ("The zone primary") only says, once, when the node's address is in none of those
  networks ("Your zone primary must allow zone transfers from 192.0.2.60: allow its network
  (e.g. 192.0.2.0/24) there once.") and goes on; there is no "made by hand" confirmation
  any more. + Add node shows it, and a new name's config starts with the zones to copy.
- `settings.json`'s `"primary"` is `{"address", "allows", "zones"}`: the primary's IPv4
  address, the networks it allows zone transfers from, and the zones to copy from it. An
  older file's `"kind"`, `"url"` and `"cert_sha256"` are accepted and ignored: the
  controller logs once at startup that they are no longer used, takes an `"url"` on an IP
  address as `"address"`, and drops them the next time the file is written.
- Zones' "Zone primary" row is the primary's address, the networks it allows (naming the
  nodes in none of them, with the network to allow) and the zones copied from it, each
  added or removed as a change to apply. The first run's zone primary step is the same
  fields, or Skip. A zone the nodes can't copy says what to check on the primary.

### Removed

- The zone primary's API integration: its API driver, the API token, the https
  client and certificate pinning. Gone with it: `espdns primary import|status|cert|pin|unpin`;
  adopt's `-primary-kind`, `-primary-url`, `-primary-token-file`, `-primary-token-env` and
  `-primary-done`, and `$ESPDNS_PRIMARY_TOKEN`; `GET /api/primary`,
  `GET`/`POST`/`DELETE /api/primary/certificate` and the `primary` job kind; the adoption
  request's `primary_done`; `POST /api/settings`'s `primary_token` and
  `remove_primary_token`, and `GET /api/settings`'s `primary_kinds`, `primary_token` and
  `primary_state` (it has `primary` instead: the address, networks, zones and the nodes
  outside them).
- Factory images from releases (`espdns-<version>-factory-<board>.bin` and its
  `.manifest.json`; `dist.Factory`): a release knows nobody's keys or addresses, so a new
  node is installed with the controller's Install over USB. `release build`'s `-catalog`
  now only checks that every catalog board's chip image is in the release. Getting
  started no longer makes keys with `tools/keygen.py` and builds the firmware by default
  (both are in its Own build section and appendix).

### Security

- No zone primary token is stored any more. At startup the controller deletes one an older
  version left (`keys/primary.token`, or `keys/technitium.token`) and logs that it removed
  the unused token: revoke that token on your primary. A backup never includes one, and a
  restore skips one an older backup carries, saying so.
- Blocklist URL sources are https from public addresses only, each address checked as it
  is connected to (redirects and DNS rebinding included). A list server inside is fetched
  only with its host on the internal allowlist, `settings.json`'s `internal_sources` (the
  CLI's `-internal`), and from that host only.
- mDNS answers are a hint only: loopback, link-local, multicast and broadcast addresses are
  ignored, at most 64 nodes are listed from mDNS alone, and an answer never renames or
  moves a known node.

## [0.0.1] - 2026-10-07

The first numbered version: everything built so far, from the node firmware and the
controller's first pages to security fixes and the backend of the new GUI.

### Added

- Versions: the repository's `VERSION` is the firmware's version (its app descriptor's, so
  `/status`, the image header and the controller say it, no longer `git describe`) and the
  controller's (`espdns version`, `/api/session`, its log). Firmware updates are ordered by
  version, the build time breaking a tie between two builds of one version.
  `scripts/bump-version.sh` moves to the next version.
- The node: a small OS on ESP32-P4 and ESP32-S3 boards (wired Ethernet, Wi-Fi as a fallback,
  microSD): the services its config enables (hosted zones, secondary zones over AXFR,
  forwarding, a cache, blocking, a query log), board definitions as data, a memory plan from
  board data, health states and LEDs, `/status`, `/metrics` and the query log with a cursor.
  It takes only signed releases (ECDSA P-256, bound to the node, board and a sequence number).
- Blocking: lists compiled by the controller (adblock, hosts, domains, wildcard and RPZ
  sources, allow lists, overrides), placed in the node's memory tiers, with a size-change
  check, a list canary, a soak and a revert to the older list the node keeps.
- The controller (one Go binary with a web UI, run with Docker Compose) and its CLI
  (`espdns`): discovery, adoption, node configs and their editor, the zone editor, any zone
  primary (manual, or Technitium's API as a driver), rolling pushes that never take down the
  last healthy node, a dashboard and the query log, backup, restore and recovery from the
  nodes.
- The backend of the new GUI: zone inventory and zone lookup; pending changes and
  one apply job that sends them; the node settings form, its edits as pending changes;
  firmware update availability, shown as versions; one search across nodes, zones and
  lists; the first run and `settings.json` from the browser; and looking up a node to add
  by its address.
- Forwarding: a table of the upstream queries in flight, with caps per group.
- Hosted zones: a revert to the older bundle the node keeps, from the firmware, the CLI, make,
  a controller job and the Zones page.

### Changed

- Hosted zones are held in canonical order, with a binary search for a name's existence, and
  to their memory limit; an AXFR and its SOA check run within one deadline.
- The test vectors, fixtures and tests use documentation addresses and names only, and CI
  refuses private ones.

### Security

- The controller loads esp-web-tools from its own vendored copy (10.4.0), not from a CDN.
- The controller sends a strict Content Security Policy and security headers on every
  response, with no inline scripts.
- Firmware builds: no key and no network in the build containers, signing in a step of its
  own, and container images and CI actions pinned by digest.
- The node's HTTP server checks the Host header against DNS rebinding, gives each request one
  deadline, applies releases in a worker, and JSON-escapes `/status`.
- Controller sessions are bound to the page's origin, logins back off per client, and a backup
  or a revealed Wi-Fi password asks for the password again.
- The controller's data directory is the user's only (directories 0700, files 0600), and its
  container is hardened: no capabilities, no new privileges, a read-only root, memory and
  process limits.
