# espDNS architecture

How espDNS works: what a node does, what the controller does, and how a change gets from one
to the other safely. This is the code as it is today, in the 0.0.x alpha; what isn't built
yet is listed at the end.

Security model: [security.md](security.md). Making and checking a release:
[releasing.md](releasing.md).

## The idea

espDNS is reliable internal DNS on cheap, low-power ESP32 boards. A basic setup is two
nodes and one Docker container:

- Each node is an ESP32 board with Ethernet and a microSD card. It answers DNS.
- The controller is a Docker container. It sets nodes up, signs changes and pushes them.
- Your router hands out both nodes as the clients' DNS servers. If one is down, clients use
  the other.

```
          router: clients' DNS = node A, node B
                          │
   ┌──────────── controller (Docker, can be off) ────────────┐
   │ web UI · CLI · release signer · blocklist compiler       │
   │ adoption · config and zone editors · rolling push        │
   └──────┬────────────────────────────────┬──────────────────┘
          │  signed releases, only when you change something
   ┌──────▼──────┐                  ┌──────▼──────┐
   │   node A    │                  │   node B    │   each runs alone from flash + SD
   └──────┬──────┘                  └──────┬──────┘
          └── zone transfers from your primary (optional, per zone) ──┘
```

Most resolvers move to the second server after a few seconds, so while one node reboots some
clients see a short delay, not a failure. Add more nodes the same way; they flock together.

## Goals

In priority order. When two conflict, the higher one wins.

1. Always answering. At least one node answers at all times, including during updates.
   Nothing optional (blocking, logs, zone checks, the clock, the SD card) may delay or stop
   a node answering. A failure there makes the node *degraded*, not silent.
2. Easy to stand up. Two boards and one container. Flash from the browser, adopt from
   the web UI, hand both nodes to your router. No toolchain, no hand-edited files.
3. Efficient. A node runs only the services its config enables; one that's off costs
   nothing. A node draws about half a watt to a watt.
4. Fast cold start. DNS answered within 15 seconds of power-on, with no controller and
   no zone primary reachable.
5. Optional blocking. Adlists, allow and deny rules, overrides, pause, a query log and a
   dashboard.
6. Cheap. Roughly US$20 to $40 per node.

Four rules keep those goals honest:

- Nodes never depend on the controller. It builds, signs and pushes, and you can switch
  it off any time. A node never contacts it; at run time a node talks only to DNS clients,
  its forwarders, its zone primary (if any) and NTP servers.
- Answering comes first. The DNS listeners open before anything optional starts.
- Never take down the last healthy node. The controller changes one node at a time and
  won't start a change that needs a reboot while another node is unhealthy, unless you
  force it.
- Make faults obvious. A degraded node says so in `/health`, in the controller and on
  its LED, so you can find the bad board and swap it.

## Scope

- Your router owns the network. It hands out DNS servers, sends IPv6 router adverts,
  provides time and routes traffic. espDNS doesn't run DHCP, and it doesn't stop clients
  using other resolvers (IPv6 DNS from router adverts, a browser's DNS over HTTPS). Setting
  up the router is part of setup.
- Nodes forward. They don't resolve from the root servers.
- Not supported yet: one shared IP address for several nodes, and Pi-hole regex rules.

## The node

The firmware is a small operating system on ESP-IDF and FreeRTOS.

- One image per chip, not per board. A small data file in flash (the board definition)
  says how the board is wired. The image holds no board details, site settings or secrets.
- The DNS listeners and cache (always on), forwarding, forward zones, secondary zones,
  hosted zones, blocking and the query log each start and stop on their own. One that's
  off has no task, stack, buffers or timers.
- Live where possible. Turning forwarding, hosted zones, blocking or the query log on or
  off applies at once. Changes that need a new memory layout or address wait for a reboot,
  which the controller schedules.
- Watchdogs. If the UDP receive or TCP accept task stalls (or every UDP worker at once),
  the node reboots, at most 3 times in a row; after that it stays up in *fault* rather than
  boot-looping. An optional service that fails is retried after 5 s, 10 s, 20 s and so on up
  to 5 minutes, and after 3 failures in a row the node is *degraded*. A stalled forward loop
  also makes it *degraded*; it keeps answering from cache and local zones. The node never
  reboots for anything else.
- Idle means idle. Services wait on events rather than poll. Where it's safe, the CPU
  clock drops when idle; the board sets the default and the node config can switch it live.

## Answering a query

For each query the node checks, in order:

1. Local zones. A secondary or hosted zone; the longest matching name wins. Never
   blocked.
2. Blocking, if it's on. A blocked name gets `0.0.0.0` for A, `::` for AAAA and an empty
   answer for other types, with a 10-second TTL (or NXDOMAIN, if chosen). Never cached.
3. Forward zones: a zone whose queries go to its own forwarder.
4. The cache, then the default forwarders. With blocking on, CNAME targets in a
   forwarded answer are checked too, which catches trackers hiding behind first-party names.

A node with no default forwarders answers such a query REFUSED, so the client asks its
next server at once.

## Forwarding: the pending table and the forward loop

No DNS worker ever waits on a forwarder, so a slow or dead one can't tie up the node.
Queries that miss the cache park in the pending table, one *flight* per question, and
every query for the same question shares that flight's answer (or SERVFAIL). One task, the
forward loop, drives all the flights, each with its own random source port and query ID.

The table has `memory.fwd_pending` slots (32 by default). The
default forwarders and each forward zone may use at most half of them, so one dead forwarder
can't starve the rest. Each flight tries every forwarder twice, each try bounded by
`upstream_timeout_ms`, plus one TCP retry for a truncated answer.

## Zones

Each zone has exactly one source:

- Secondary. Copied from your zone primary over AXFR, saved to SD, refreshed on NOTIFY
  and SOA checks. Any standards-compliant primary works (BIND, Knot, PowerDNS, Windows DNS,
  Technitium and others).
- Hosted. espDNS is the primary. The controller keeps RFC 1035 zone files and sends a
  node's hosted zones as one signed bundle, swapped in live. At most 32 per node.
- Forward. Queries under the zone go to a named forwarder.

One setup can mix all three, but a zone can't be two kinds on one node. Secondary zones
saved on SD answer at boot, before any check with the primary. How-to: [zones.md](zones.md).

## Blocking

An optional service; off, none of it runs or uses memory. How-to: [blocking.md](blocking.md).

- The controller compiles; nodes only receive. It downloads your lists, removes
  duplicates, writes one compact file (about 3 to 3.6 bytes per domain), signs it and pushes
  it. Every node gets the same bytes and never fetches lists itself.
- Overrides are checked first. Then the most specific match wins; on a tie, allow beats
  block.
- Two slots on SD, so the previous list is always kept for a revert. If two copies fit
  the board's memory share, a new list swaps in live; if not, it loads at the next reboot. A
  list too big for RAM is looked up from SD, one sector per likely match.
- Checks before a push. The compiler refuses a build whose size changes by more than a
  set percentage (20% by default) or that blocks a must-resolve name, unless you accept that
  build once.

## Storage

| What | Where on the node |
|---|---|
| Firmware | Two OTA app slots in flash, with automatic rollback |
| Board definition | The `board` flash partition (4 KB), written when the board is flashed |
| The keys it trusts | The `keys` flash partition (8 KB at 0x1b000): two slots, each with a CRC and a generation; written over USB, or by a KEYS release |
| Node config | The `config` flash partition, two 16 KB slots |
| Wi-Fi network from setup | NVS (flash), set over USB during flashing |
| Release sequence numbers | NVS |
| Secondary zones | SD card |
| Hosted zones | SD card, two slots |
| Blocklist and overrides | SD card, two slots each |
| Query log | RAM only |

With two slots, the node boots the valid copy with the highest sequence number. If it had to
fall back to an older one, it reports *degraded*, reason `older copy`.

If the SD card fails or is too slow at boot, the node keeps answering from its config
and forwarders. Secondary zones are transferred again and kept in RAM; hosted zones and
blocking are unavailable. It reports *degraded*, reason `sd card`.

## Memory plan from board data

Nothing on the node looks at free memory to pick a size. Each board definition lists fixed
sizes in its `memory` section, found by testing that board:

`internal_kb`, `cache_kb`, `cache_entries`, `blocklist_kb`, `blocklist_index_kb`,
`hosted_zones_kb`, `secondary_zones_kb`, `querylog_kb` and `fwd_pending`, plus `psram_mb`.

Anything left out comes from the chip image's default; the full list is in
[`boards/README.md`](../boards/README.md). At boot, and for every config pushed, the node
plans memory for the services the config enables:

| Service | Internal RAM | Data (PSRAM) |
|---|---|---|
| dns | Listener, worker and forward-loop stacks | The cache, worker buffers, the UDP receive pool, the forward loop's TCP buffer, and the pending table |
| secondary | The zone task's stack | `secondary_zones_kb` and the transfer buffers |
| hosted | The load task's stack | Three times `hosted_zones_kb` (the zones, a new bundle being checked, its payload) |
| blocking | The load task's stack, `blocklist_index_kb` | `blocklist_kb` (the list and overrides, two copies during a live swap) |
| querylog | None | `querylog_kb`, 160 bytes per entry |

A config whose plan doesn't fit is refused; it never half-starts. At boot the node falls back
to the previous config (or firmware defaults) and reports *degraded*, reason `config`.

The controller runs the same plan before it pushes. To check a config against a catalog
board yourself, from `controller/`:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  config -check -file /data/configs/node1.json -board ws-s3-eth -data /data
```

## Boards and images

Only the chip, PSRAM type, P4 silicon revision and flash layout are fixed at compile time.
Everything else about a board is data.

| Chip image | For |
|---|---|
| `esp32p4-rev1` | ESP32-P4 silicon v1.x (wired only) |
| `esp32p4` | ESP32-P4 v3 and later |
| `esp32s3-octal` | ESP32-S3 with octal PSRAM |
| `esp32s3-eth` | ESP32-S3 with octal PSRAM, wired: `esp32s3-octal`'s sibling, faster for less internal RAM |
| `esp32s3-quad` | ESP32-S3 with 8 MB of quad PSRAM |

Each image carries every driver its chip can use (built-in Ethernet with IP101, LAN87xx,
RTL8201 and DP83848 PHYs, W5500 SPI Ethernet, SD over SDMMC or SPI, Wi-Fi, LEDs). Unused
drivers cost flash, not RAM.

The board catalog is JSON files in [`boards/`](../boards/), each marked *tested*,
*community* or *untested*. Tested on hardware today:

- Guition JC-ESP32P4-M3-DEV (ESP32-P4, built-in Ethernet with an IP101 PHY, SDMMC)
- Waveshare ESP32-P4-ETH (ESP32-P4, built-in Ethernet with an IP101 PHY, optional PoE, SDMMC)
- Waveshare ESP32-S3-ETH (W5500 Ethernet, optional PoE, SD over SPI)
- Seeed XIAO ESP32S3 Sense (Wi-Fi, or wired with an added W5500 module)

**Install over USB** (on Nodes, and Reinstall over USB on a node's page) flashes a node from
the browser; **+ Add node** then adds it to your nodes. The controller
assembles the image from a chip image plus a `board` partition and a `keys` partition (your
release and recovery public keys) in milliseconds; it doesn't compile anything. For a Wi-Fi
board it then sends the network name and password over USB. **Write keys only** writes just
the `keys` partition, for a node with no keys.

Chip images are key-neutral: no board, address or keys built in, the same for everyone.
The controller gets them from the project's release (Get firmware, or when you apply an
update) or from your own build signed with your release key.

Settings come in layers. A value comes from the first of:

1. the node config (signed, pushed by the controller);
2. the board definition (the catalog's values, plus the address from Install over USB);
3. the firmware's default.

The board definition only changes by flashing over USB.

Wired is the supported way. Wi-Fi is a best-effort fallback for boards without Ethernet.
Keep at least one node wired; the controller warns when only Wi-Fi nodes are answering.

## Boot

DNS is answered within 15 seconds of power-on. Only these come before the listeners open:
the bootloader and image check, PSRAM start-up, the Ethernet link, the address, and mounting
the SD card (in parallel, 1.5-second limit).

Everything else follows: blocking loads in the background (answers in that window aren't
blocked), zone checks run while saved zones already answer, and the clock is set over SNTP
from the configured NTP servers, else the default gateway, else `pool.ntp.org`. An unsynced
clock isn't a fault.

`/status` reports the time to listeners open as `boot_ms`. New firmware that misses 15
seconds fails its health check and rolls itself back. (On DHCP the limit is reported but not
enforced.)

## Addresses and adoption

A node never asks DHCP for an address unless a setting says `"dhcp"`. Its address comes from
its node config (`network`) once adopted, else its board definition (`network`), written
when flashed. With neither, it says so on its LED and serial console, and sends nothing.

Install over USB gives the node a static address (prefix length and gateway), or `dhcp` on a
network with a DHCP server; it refuses to make an image with no address. A release has no
factory images: a new node is always installed from the controller.

Adoption, from the web UI or the CLI:

1. **Discovery.** Nodes advertise `_espdns._tcp` over mDNS. The controller lists unadopted
   nodes on the Nodes page with their address; hover for the board, firmware and MAC
   address. The node's setup panel has the MAC with a copy button, for a DHCP reservation.
   *Identify* blinks a node's LED so you can match it to the board in your
   hand.
2. **Address.** Keep the current one or give a static one; the controller first checks that
   nothing else answers there. A network in `settings.json`'s `"no_dhcp"` never gets a DHCP
   config.
3. **Zone primary.** Nothing changes on the primary: you allow zone transfers from your
   nodes' networks once and list them in `settings.json`'s `"primary"` `"allows"`. If a node
   with secondary zones is outside them, the controller says once what to allow.
4. **Config.** The controller pushes the node's config. A new address waits for a reboot,
   which the controller then does.
5. **Confirm.** A config that changes the address or Wi-Fi network boots *on trial*. It's
   kept once a DNS query reaches the new address; if none arrives within 90 seconds, the node
   homes back to its previous config.
6. **Pin.** The controller records the node's ID against its address, with its node key
   (see [pins](#signed-releases-and-pins)).

Once two nodes are adopted, give both addresses to your router as the clients' DNS servers.

## Health

Every node reports one state in `/health` and `/status`:

| State | Meaning | Answering? |
|---|---|---|
| booting | Before the listeners open | No |
| healthy | Everything its config enables is running | Yes |
| degraded | Answering, but something enabled failed | Yes |
| no network | Link down or no address | No |
| fault | The listeners failed, or stalled 3 times in a row | No |
| updating | Receiving a release | Yes |

`/health` returns 200 while the node answers and 503 otherwise, so a monitor only needs
the status code. It also lists every reason that holds: `sd card`, `blocking`,
`zone expired`, `zone refresh failing`, `forwarders failing`, `forwarders slow`,
`overloaded`, `forwarder task stalled`, `config`, `hosted zones`, `service failed`,
`older copy` and `no keys`. `reboot pending`, `config on trial` and `service restarting` are
listed but don't make a node degraded.

LED patterns (single-colour, or colour on RGB): a short blip every 5 seconds when
healthy, two blinks when degraded, a slow blink with no network, solid for a fault, a fast
flicker to identify. A board without a usable LED can name an add-on LED on a spare pin in
its node config.

## Observability

- `GET /metrics`: Prometheus text. Query counts, answer times, per-forwarder figures,
  cache and blocking counters, zone state, memory per service, health.
- `GET /querylog?cursor=N&limit=M`: the optional query log, a ring of recent queries in
  memory (160 bytes each). `querylog.client` in the node config keeps each client's address
  `full`, as its `/24` (`subnet`), or not at all (`hidden`).
- The controller's **Query log** page polls each node every 10 seconds and keeps one hour of
  history and the newest 20,000 log entries, in memory only.

`/health` and `/metrics` are open to anyone who can reach the node, so your own monitoring
can watch nodes with the controller off. Who can read `/status` and `/querylog`:
[security.md](security.md).

## Signed releases and pins

Every change to a node is a signed release: firmware, config, hosted zones, blocklist,
overrides, and control commands (pause, identify, flush cache, reboot, revert). Think of a
carrier pigeon with a sealed letter: the node checks the seal before it reads a word. The
format is in `firmware/main/release.h`:

| Field | Purpose |
|---|---|
| Kind | What the payload is |
| Key slot | 0 = release key, 1 = offline recovery key |
| Target node | The node's ID (its eFuse base MAC), so a release can't be used on another node |
| Chip image | The image name, so a release can't be used on the wrong hardware |
| Sequence number | Must be above the last one the node took for that kind |
| Payload length and SHA-256 | Checked while the payload streams in |
| Signature | ECDSA P-256 over the 128-byte manifest |

- Keys live in the keys partition, not the firmware. A node trusts two public keys, the
  release key and the recovery key. Install over USB writes them.
- No keys, no changes. A node with no keys (or a damaged keys record) is in setup mode:
  it answers DNS on its board's defaults, reports `no keys`, and refuses every
  release until keys are written over USB.
- Key rotation is a KEYS release signed with the recovery key only, for one node by ID
  ([security.md](security.md#key-rotation)).
- Sequence numbers are milliseconds since 1970 (or one more than the last), kept per
  kind in NVS, so replays and downgrades are refused, even after a reboot. To roll back, the
  controller signs the older payload again with a new number. Neither side accepts a number
  more than 24 hours past its own clock.
- Pins. Anything answering on a node's address could claim to be it, so the controller
  keeps its own record (`pins/<node-id>.json` in the data directory): each node's ID, pinned
  to its address at adoption, the last sequence number signed per kind, and the node key
  its `/status` gave then, which a config's Wi-Fi password is sealed to
  ([security.md](security.md#the-wi-fi-password)). It signs only
  for the pinned ID, and nothing for an unpinned address except an *identify* during
  adoption. To pin a replaced board, or a node adopted before pins, from `controller/`:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller pin -data /data -host <address> -node <node ID>
  ```

Firmware updates are safe to fail. A new image boots on trial: it must get its address,
open its listeners and answer within its limits, or the bootloader rolls back.

A node never reboots itself to apply a release. One that can only apply at boot is stored
and checked, and the node reports `reboot pending` with the reason (for example
`config: address`, `blocklist: size`, `firmware`). The controller picks when each node
reboots, so it never takes two down at once.

| Kind | Where it lives | Applied |
|---|---|---|
| Firmware | OTA app slots | At a reboot, now or when the controller schedules it |
| Config | `config` partition, two slots | Live where the setting allows, else at the next reboot |
| Hosted zones | SD, two slots | Live |
| Blocklist | SD, two slots | Live if two copies fit, else at the next reboot |
| Overrides | SD | Live |
| Control | Not stored | Live |

## Rolling updates

Every push, from the web UI or the CLI, goes through one rolling push:

1. Check first. Before touching any node: services, the memory plan for a config, zone
   conflicts for hosted zones.
2. One node at a time. Before each change, and again just before a reboot, another node
   (or a DNS peer) must be answering, and for a change that may reboot, every other one must
   be healthy. `-force` goes ahead with just one other node answering.
3. Wait for it. The node must come back on the new build or sequence number, healthy and
   passing DNS checks, before the next starts.
4. Canary and soak for blocklists. A new list goes to one node first, gets real test
   queries, is watched for the soak time (one minute by default) and checked again before the
   next node.
5. Revert on failure. If a node fails a new list, every node that took it goes back,
   the failed node first. Nodes not yet changed aren't touched.

If clients also use another resolver, list it as a DNS peer in `settings.json`
(`"dns_peers"`). While it passes its DNS checks it counts as one healthy node, so a single
espDNS node can be rebooted safely.

Every change takes the fleet lock (`fleet.lock` in the data
directory). A second change fails at once and names who holds it.

## The controller

One Go binary with its web UI built in, plus the `espdns` CLI, in one Docker image. It
doesn't compile firmware. Start it with one `docker run`
([Getting started](getting-started.md#start-the-controller)), or with Docker Compose
([Developer guide](developer.md#docker-compose)).

The dashboard is at http://127.0.0.1:8480. The controller listens on localhost only and
uses host networking to find nodes over mDNS and reach them.

- Node list. Nodes in `settings.json`, ones found over mDNS, and any you look up by
  address, each polled every 10 seconds. An mDNS answer is only a hint: it never renames or
  moves a known node.
- Firmware updates. The controller asks GitHub (or your `release_url`) for the newest
  firmware release when it starts, every 24 hours, and when a new node shows up, and keeps
  only its version. A node on an older version is offered the update, whatever its chip;
  the chip image is downloaded and checked when the update is applied. Each node's `/status`
  says the protocol it speaks to the controller (`"proto": 1`), and a node on one the
  controller doesn't speak is offered no update.
- Jobs and pending changes. Edits collect as pending changes and go out in one apply
  job. Jobs run one at a time with live progress, and every change goes in the action log.
- Zone primaries. The controller keeps only the primary's address, the networks it allows
  and the zones to copy. It never changes the primary and holds no token for it.
- CLI. The same operations as the web UI, from the same image:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller status -all -data /data
  ```

You can rebuild the controller. `espdns backup` writes one encrypted file (age format, to a
passphrase or an age key); `espdns restore` puts it back into an empty data directory with
the controller stopped. Without a backup, `espdns recover` reads the nodes' `/status` (with
the release key imported) and rebuilds `settings.json` and the pins. Nodes don't return
their configs or zone contents; those come from your own copies.

The release key is `keys/release.pem` in the data directory. The recovery key's private
half never reaches the controller: the first run makes it in your browser and downloads it.
Which keys a node trusts changes only with a rotation you sign offline with the recovery
key. Details: [security.md](security.md).

Chip images come in only as a signed release, the project's or your own, and their
provenance is checked again before every update. The controller then signs the update with
your release key; the project's key signs nothing a node takes
([Releasing](releasing.md#using-a-release)).

## The data directory

Everything the controller keeps is in one directory: `/data` in the container,
`controller/data` on the host by default (`ESPDNS_DATA` in `.env`). It's all JSON or plain
files, no database. Directories are mode 0700, files 0600.

| Path | What |
|---|---|
| `settings.json` | The deployment: nodes, DNS peers, canary, networks with no DHCP server (`no_dhcp`), the zone primary, internal list servers. Example: [`configs/example-settings.json`](../configs/example-settings.json) |
| `auth.json` | The login: user name and an argon2id hash of the password |
| `keys/release.pem` | The release signing key |
| `keys/recovery.pub` | The recovery key's public half (the private half stays offline) |
| `keys/rotation/<id>/` | A key rotation: each node's KEYS release, unsigned and signed, and `plan.json`; during one, the new release key in `keys/release.next.pem`, after it the old one in `keys/release.prev.pem` |
| `configs/<name>.json` | Node configs, one file each. Example: [`configs/example-node.json`](../configs/example-node.json). Older versions in `configs/.history/`; the last pushed to each node in `configs/.pushed/` |
| `zones/<zone>.zone` | Hosted zone files. History in `zones/.history/`; the last pushed set per node in `zones/.pushed/` |
| `blocking/` | List definitions (`lists.json`) and local sources, allow lists and overrides |
| `lists/<name>.bin` | Compiled blocklist and overrides files |
| `boards/` | Your own board definitions, saved from Install over USB |
| `firmware/images/` | Imported chip images, for Install over USB and firmware updates, each with its `provenance/` (the signed `SHA256SUMS`, its signature, the tarball, who signed it) |
| `firmware/latest` | The newest firmware release's version, as the controller last heard it |
| `pins/<node-id>.json` | Each node's pinned ID, last signed sequence numbers and node key |
| `changes/` | Pending changes not yet applied |
| `log/actions.jsonl` | The action log: every fleet change, by the CLI or the web UI |
| `log/jobs/` | Each job's record and progress log |
| `fleet.lock` | The fleet lock |
| `recovered/` | What `espdns recover` read from the nodes |

> **Artie's tip:** back this directory up. It's the whole controller, and the nodes can't hand
> it back to you.

## Not built yet

- Signed reads: `/status` and `/querylog` protected by a request signed by the controller.
- Encrypted transport between the controller and the nodes, and DNS over TLS to forwarders.
- An endpoint for a node to return its config or hosted zones (for recovery). A secondary
  zone's records can already be read from a node with
  [`GET /zone`](reference/node-endpoints.md#zone).
- Saving a secondary zone's last refresh time across reboots (today a zone's expiry timer
  starts again at each boot).
- One shared IP for several nodes, and per-client blocking groups.
- Wi-Fi through the companion Wi-Fi chip on ESP32-P4 boards.
