# Troubleshooting

Start with [First look](#first-look), find the node's state and reasons, then jump to the
fix. Safety rules, updates and backups are in [Operations](operations.md). Run every command
from your clone's `controller/` directory ([Operations](operations.md#before-you-start)).

## First look

In the web page at `http://127.0.0.1:8480`, **Nodes** gives each node a dot: green while it
answers and all is well, amber when something needs doing (an update, a reason to read, a
node not added yet), red when it isn't working, is offline or is out of service. A short
flag sits beside a dot that isn't green; hover over the dot for the state and reasons.
Click a node for the details and its raw `/status`.

On the command line, every node in `settings.json`:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller status -all -mdns 0 -data /data
```

One node, by address, as JSON with everything it reports:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller status -host 192.0.2.54 -json -data /data
```

Each node's first line is its state, its reasons in parentheses, and `reboot pending` if
one is waiting. To find nodes, adopted or not:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller discover -data /data
```

The controller's log:

```sh
docker compose logs -f
```

The action log, `log/actions.jsonl` in the data directory, has a `start` and an `end`
line per change. A `start` with no `end` is a change that died part way.

## Health states

Every node reports one state, in `/health` and `/status`.

| State | What it means | Answers DNS? | `/health` code |
| --- | --- | --- | --- |
| `booting` | The node is starting. Its DNS listeners are not open yet. | No | 503 |
| `healthy` | Everything its config turns on is running. | Yes | 200 |
| `degraded` | It answers, but something it should run has failed. | Yes | 200 |
| `no network` | The link is down, or the node has no address. | No | 503 |
| `fault` | Its DNS listeners failed to open, or stalled. | No | 503 |
| `updating` | It is receiving a release. | Yes | 200 |

`GET http://<node>/health` needs no login, so a monitor only needs the code. Point it at the
node's address: a `Host` that isn't the address, the mDNS name (`<hostname>.local`) or the
config's name gets `421`. Only services the config turns on count; an unsynced clock isn't
a reason.

## Health reasons

A node lists every reason that holds. Most make it `degraded` and two make it `fault`;
the rest are just for your information.

### Fault reasons

These stop DNS.

**`listeners failed`.** A DNS listener couldn't open its port.
Fix: reboot. If it comes back, check the config and that the firmware is current, then
report a bug.

**`listeners stalled`.** The listeners stalled and the node rebooted for it three times in
a row, so now it stays up for you to read.
Fix: save its `/status` (the JSON command above), reboot or power-cycle it, update the
firmware if there's a newer one, and report a bug with the saved `/status`.

### No network

These come with the `no network` state.

**`no link`.** Fix: check the cable, switch port and power; with a Wi-Fi fallback, the
Wi-Fi settings in its config.

**`no address`.** The link is up but there's no IP address.
Fix: on DHCP (board definition or config says `dhcp`), the network needs a DHCP server; if
it has none, reinstall from **Install over USB** (+ Add node) with a static address. On a
static address, check the address, prefix and gateway in the config.

### Degraded reasons

The node still answers, but something it should run has failed.

**`sd card`.** A service that uses the card is on (secondary zones, hosted zones or
blocking), but the card is missing, failed to mount or timed out. Secondary zones are kept
in memory only; hosted zones and blocking are off.
Fix: reseat or replace the card, then reboot.

**`blocking`.** A blocklist or the overrides failed to load, or the card had read errors.
Fix: send the node back to its older list:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  revert -host 192.0.2.54 -kind blocklist -data /data -key /data/keys/release.pem
```

`-kind overrides` reverts the overrides; the **Blocklists** page has the same
([Revert](blocking.md#revert)). Then build and push the list again, and replace a card with
read errors.

**`zone expired`** (preceded by **`zone refresh failing`**, after three failed refreshes
in a row). A secondary zone passed its SOA expire time without a refresh from the primary.
Fix: check that the primary is up and reachable, that its address in the node's config is
right, and that it allows zone transfers from this node's network (the zone's page on
**Zones** names any node outside them;
[On your primary](zones.md#on-your-primary-allow-zone-transfers-from-your-nodes-networks)).

**`forwarders failing`.** The default forwarders missed five queries in a row over at least
30 seconds (forward zones don't count).
Fix: check the node can reach them, and the forwarders in its config.

**`forwarders slow`.** The default forwarders can't keep up. In the last 30 seconds: a
query was turned away (SERVFAIL at once) because they had their full share of the upstream
query table; or they held over half that share for 10 seconds; or over half of at least
five queries took longer than `upstream_timeout_ms` or got no answer. Forward zones don't
count. `espdns metrics -host <node>` shows queries in flight, turned away and timed out.
Fix: check the forwarders' reachability and speed, or use faster ones.

**`overloaded`.** In the last 30 seconds the node dropped at least a tenth of its UDP
queries, and at least 64 (every worker busy and the queue full, or queued over a second);
or 64 waited that long. It still answers what it can, and clears 30 seconds after the load
drops. `espdns metrics -host <node>` shows `dropped`, queue-full time and queries shed.
Fix: spread clients over more nodes, or find the one flooding it. Under a heavy flood
`/health` may time out too, so it may be `overloaded` rather than down.

**`forwarder task stalled`.** The forwarding task missed its watchdog: forwarded queries go
unanswered (cache and the node's own zones still work), and the node won't reboot itself.
Fix: reboot it under the safety rules, and report a bug if it comes back:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  reboot -host 192.0.2.54 -mdns 0 -data /data -key /data/keys/release.pem
```

**`low memory`.** Under 16 KB of internal RAM free.
Fix: turn off services it doesn't need or use a smaller blocklist, then reboot.

**`no board definition`.** It runs on defaults: no Ethernet, no SD card, maybe Wi-Fi.
Fix: flash it again with **Install over USB** (+ Add node).

**`config`.** The stored config was refused, unreadable or failed its trial (a memory plan
that doesn't fit the board too), so the node runs its older config or firmware defaults.
The error is `config.error` in `/status` (the `config:` line of `status -host <node>`).
Fix: correct it on the **Configs** page, check it, and push it again.

**`hosted zones`.** A bundle failed to load, or a zone in it is also a secondary or forward
zone in the node's config (a zone has one source).
Fix: remove the zone from one of the two, or go back to the older bundle the node keeps:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  revert -host 192.0.2.54 -kind zones -data /data -key /data/keys/release.pem
```

**`service failed`.** A service failed to start three times in a row, or missed its
watchdog. The node's details (`/status` `services`) say which.
Fix: check that service's setting in the config, then reboot.

**`older copy`.** At boot the newest blocklist, overrides or hosted zones bundle was
corrupt, unreadable or too big, so the node runs its older copy (`/status` `fallback` says
why). A revert you asked for doesn't show this.
Fix: push it again (smaller, if it was too big); replace a failing card.

**`no keys`.** Setup mode: the keys partition is erased or damaged and the firmware has no
keys built in. It answers DNS on board defaults and refuses every update and setting.
Fix: see ["this node has no keys"](#this-node-has-no-keys).

### For your information

These don't make a node degraded.

**`config on trial`.** A config with a new address or Wi-Fi network. It reverts unless a
DNS query reaches it within 90 seconds; the rollout or adoption that sent it confirms it.

**`reboot pending`.** A release applies at the next reboot; it serves meanwhile. Reboot it
under the safety rules (**Reboot (pending)** in the node's details), or:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  reboot -host 192.0.2.54 -if-pending -mdns 0 -data /data -key /data/keys/release.pem
```

**`service restarting`.** A service is being restarted; three failures in a row make it
`service failed`.

## LED patterns

One status LED per board, if its board definition (or an add-on LED in the config) has
one. The patterns work in a single colour; RGB adds colour. The Nodes page shows the state
as its dot ([First look](#first-look)).

The two ESP32-P4 boards and the XIAO have no LED the node can use: add one
([A status light](hardware.md#a-status-light)).

| Pattern | RGB colour | State | What to do |
| --- | --- | --- | --- |
| Fast blink, 5 a second | Blue | `booting` | Wait. Booting ends once the node answers, or after 15 seconds without a network. |
| One short blip every 5 seconds | Green | `healthy` | Nothing. |
| Two blinks every 2 seconds | Amber | `degraded` | Read its reasons: [Degraded reasons](#degraded-reasons). |
| Slow blink, once a second | Red | `no network` | [No network](#no-network). |
| On solid | Red | `fault` | [Fault reasons](#fault-reasons). |
| Three quick blinks every second | Blue | `updating` | Wait for the release to finish. Do not power it off. |
| Very fast flicker | White | Identify | Someone asked the node to identify itself. It stops on its own (30 seconds by default). |

Identify overrides every other pattern while it runs. Start it from the node's page
(**Blink its light**), or:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  identify -host 192.0.2.54 -for 30s -data /data -key /data/keys/release.pem
```

> **Artie's tip:** with a shelf of identical boards, Blink its light is the quickest way to
> find which bird is which.

## Common problems

### A node is not answering

1. Look at its LED. Solid red, slow red or fast blue means no DNS: see
   [LED patterns](#led-patterns).
2. Read its status with `status -host <address>` ([First look](#first-look)). If that
   can't reach it at all, check the power, the cable, and that the controller's host can
   reach the node's network. The controller uses host networking, so it reaches what the host
   reaches.
3. Answers `/status` but not DNS? Look up its state and reasons in
   [Health reasons](#health-reasons).
4. A silent node shows its last status as stale, with when it was last seen, and offline
   after 30 seconds.
5. Found over mDNS but not on the Nodes page? It isn't in `settings.json`. Add it
   (adopt it first if it's new).

### The controller does not start

Read `docker compose logs`.

| You see | Cause and fix |
| --- | --- |
| `required variable ESPDNS_UID is missing a value` | No `.env`. Copy `.env.example` to `.env` and set `ESPDNS_UID` and `ESPDNS_GID` to your user's ids. |
| It can't write to the data directory | The directory must exist and be owned by `ESPDNS_UID`. Docker creates a missing one as root, which the controller can't use. |
| `settings: ...` | `settings.json` doesn't parse, or has a field it doesn't know. The message names the problem; fix the file. |
| A refused `ESPDNS_LISTEN` | The dashboard listens on localhost only. Any other address is refused. |

### No update is offered

A node's dot on Nodes, hovered, says why:

- `No release known yet`: the controller hasn't heard from GitHub (or your `release_url`)
  since it started. It asks at start, every 24 hours and when a new node shows up. Check
  the host can reach GitHub; the controller's log has a `firmware:` line when a check fails.
- `Its firmware speaks node protocol ...`: the node and this controller don't fit together.
  Update the controller first.
- `It runs "...", not a version`: firmware from before version numbers. Update it with a
  rollout ([Update from the command line](operations.md#update-from-the-command-line)).

### "the fleet is locked by ..."

Another change (a controller job or a CLI command) holds the fleet lock; the message and the
top of the web page say which, who and since when. Wait for it, then try again.

### "this is the only node clients use"

Every change keeps one node or DNS peer answering, so a lone node can't be changed safely.
Fix: add a second (adopted) node, or a resolver your clients also use to `"dns_peers"`.

### "another node is unhealthy" or "no other node or DNS peer is answering"

Nothing was pushed to that node.
Fix: fix the other node or DNS peer, then run the dry run and the change again (nodes
already done are skipped).

### Adoption is refused

+ Add node and `espdns adopt` say why. The common reasons:

- The address is in use (by a node, a web server, a DNS server, or any host that
  refuses connections or answers ARP). Choose a free address.
- The address is a node's or DNS peer's in `settings.json`. Choose another.
- DHCP on a network with no DHCP server: the network is in `settings.json`'s
  `"no_dhcp"`. Give the node a static address.
- A config on DHCP without a reservation. Reserve the node's address for its MAC in
  your DHCP server, then adopt with the reservation ticked (`-reserved` on the CLI).
- The dry run is too old, or something changed. Adoption needs a passing dry run of the
  same request within 15 minutes. Run it again.
- The config fails its checks for this node (say, its memory plan doesn't fit the
  board). Fix it on **Configs**.
- Its firmware takes no node configs. Update its firmware first.
- The node was adopted before (it took a config, or one was pushed to its ID): the web
  page won't adopt it. If you know which node it is (say, it was wiped), use the CLI, dry
  run first:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller \
    adopt -host 192.0.2.60 -config node3.json -mdns 0 -data /data \
    -key /data/keys/release.pem -dry-run
  ```

  Then run it again without `-dry-run`, with `-add-to-settings`. `node3.json` is a file in
  the data directory's `configs/`.
- Another node has the same ID, or another node is pinned to that address: see
  [Pinned to another node](#pinned-to-another-node).

### "Your zone primary must allow zone transfers from ..."

The adoption worked, but the node's address is outside the networks you said your
primary allows transfers from (**Zones**, **Zone primary**), so it can't copy secondary
zones yet.
Fix: allow transfers from that network on the primary, then add it under **Networks it
allows zone transfers from** on Zones.

### "zone primary API token" at startup

Older controllers kept a zone primary API token in `keys/primary.token`. It's unused now,
so the controller deletes it at start and logs
`removed keys/primary.token, the unused zone primary API token: revoke that token on your
zone primary`.
Fix: revoke the token on the primary. An older `settings.json` with `"kind"`, `"url"` or
`"cert_sha256"` under `"primary"` still loads; the log says once that those fields are
ignored.

### "this node has no keys"

A push was refused with `this node has no keys: install it with the controller's Install
over USB (or write keys over USB)`. The node is in setup mode (health reason `no keys`).
`status -host <node>` shows why on its `keys_source` and `keys_error`:

- `none`: the keys partition holds no keys record, and the firmware has none built in
  (a key-neutral image on a node that never had keys written).
- `corrupt`: the keys partition holds a damaged keys record. It's never written over
  from the network, whatever the firmware.

Nothing over the network gives a node keys. That's on purpose: otherwise anyone could give
it theirs. (A push alone never leaves a node here: firmware that comes up keyless after an
update rolls back by itself.)

Fix: connect it over USB and reinstall it from Install over USB, or use **Write keys only
(keeps settings)** in the same panel.

### "stale release"

The node refused a release with `stale release (seq not above the last one applied)`.

A node takes only a sequence number (*seq*) above the last it applied for that kind of
release. The controller uses the current time in milliseconds, or one above the last seq it
signed for that node if higher. So the node has a higher seq than the controller expects:

- Another controller, with its own data directory, signed for this node. Run one
  controller per fleet.
- The controller's clock is behind. The node took a seq from a time the controller
  hasn't reached yet. Set the host's clock right (use NTP).

Compare the `seq:` line of `status -host <node>` (milliseconds since 1970) with the current
time.

`seq too far ahead of this node's clock` means the controller's clock is over 24 hours ahead
of the node's. The controller also won't sign when its last recorded seq is over 24 hours
past its own clock. Either way, set the host's clock right.

A node marked stale on the Nodes page is unrelated: see
[A node is not answering](#a-node-is-not-answering).

### Pinned to another node

The controller signs releases only for the node ID pinned to an address
([Pinned nodes](operations.md#pinned-nodes)). These refusals come from that rule:

- `<address> answers as node "<id>", but node <other id> is pinned there: not signed`.
  Something else answers there. Compare `discover`'s `NODE` column with `pin -data /data`
  (the pins).
  - Replaced the node's board? Pin the new board to the address. It must answer there:

    ```sh
    docker compose run --rm --entrypoint /espdns espdns-controller \
      pin -data /data -host 192.0.2.54 -node <the new board's ID>
    ```

  - Not your node? Don't pin it. Take the device off that address.

- `<address> is not pinned to a node`. Nothing is pinned there. Adopt the node or, for
  a node already in service, pin it by hand as above.

- `node <id> is pinned to <address>: releases for it are signed there alone`. That node
  now answers somewhere else. If you moved it on purpose, pin it at its new address. The
  pin moves, and its seqs move with it.

### A rollout stopped

See [When a rollout stops](operations.md#when-a-rollout-stops).
