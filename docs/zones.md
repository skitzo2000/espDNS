# Zones and forwarding

How espDNS nodes answer for names: the zones they hold, the domains they send to other
servers, and the default forwarders for everything else. Blocklists are in
[blocking.md](blocking.md); the big picture is in [Architecture](architecture.md). espDNS is
alpha (0.0.x).

## How a node answers a query

A node looks at each query in this order:

1. A zone it holds, hosted or secondary. The node answers with authority, and these
   names are never blocked.
2. Blocking, if the service is on ([blocking.md](blocking.md)).
3. A forward zone: a domain sent to its own forwarder.
4. The cache, then the default forwarders, for everything else.

Each zone name has exactly one kind; the controller refuses a name that's two at once, such
as a hosted zone that's also a secondary zone on the same node.

| Kind | Where the records live | How nodes get them |
|---|---|---|
| Hosted | A zone file in the controller | The controller pushes a signed bundle to every node |
| Secondary | Your own primary DNS server | Each node copies the zone by zone transfer (AXFR) |
| Forward | Another DNS server | The node forwards queries for the domain to it |

## Before you start

Commands here run from `controller/`. The data directory is the one `ESPDNS_DATA` names in
`controller/.env` (`/data` inside the container). Commands that change a node sign with
`-key /data/keys/release.pem`, the key from the first run's Make your keys
([Getting started](getting-started.md#run-the-setup)); other ways to
give it are in the [CLI reference](reference/cli.md#secrets).

You can do most tasks from the dashboard at http://127.0.0.1:8480. You need to be logged in
to read or change zones.

## Hosted zones

In a hosted zone espDNS is the primary. You edit the records in the controller, which pushes
them to every node as one signed bundle, applied live with no reboot.

### What a hosted zone can hold

- Record types: A, AAAA, CNAME, MX, TXT, SRV, NS, PTR, CAA and SOA.
- No DNSSEC. Hosted zones are unsigned.
- Standard master file syntax (RFC 1035): `$ORIGIN`, `$TTL`, relative and absolute names.

Each zone is one file in the data directory, `zones/<zone>.zone`: `zones/home.arpa.zone`
holds `home.arpa`. File names use lowercase letters, digits, `.`, `_` and `-`.

Each node has a memory limit for hosted zones, from its board, and the controller checks
every zone set against it before pushing. Hosted zones live on the node's SD card, so a node
without a working card can't serve them. `"hosted": {"enabled": false}` in a node config
turns the service off.

### Create and edit a zone

1. Open **Zones** in the dashboard.
2. Under **New zone**, type the zone name (for example `home.arpa`) and click **Create**.
   It starts from a template: an SOA record, plus an NS record and an address record for
   each node in `settings.json`.
3. Edit the zone as text.
4. Click **Check**. The controller runs the same checks the node runs, plus each node's
   memory limit, firmware, and any clash with its secondary or forward zones.
5. Click **Bump serial** to raise the SOA serial. A date serial (`YYYYMMDDnn`) stays a date
   serial. The page warns you if the serial isn't higher than the saved file's or what a
   node serves.
6. Click **Save**, check the diff and confirm. A zone that fails the checks isn't saved.

Each save keeps the previous version in `zones/.history/`. **Delete** keeps the file there
too, and **Restore** opens an old version as a new zone. Nodes keep serving a deleted zone
until you push the zone set without it.

Saving doesn't change any node: the change waits in **changes to apply** at the top of every
page.

### Push zones to the nodes

Press **N changes to apply** at the top, then **Apply**. The pushed set **replaces** every
hosted zone on each node: everything the nodes serve now, plus your changes.

Apply checks every node first, changing nothing. Then it changes one node at a time,
starting with the one chosen under **How it rolls out**, and checks each answers DNS and
watches it before moving on.

From the command line, check zone files without touching any node:

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    zones -check -zone /data/zones/home.arpa.zone
```

Roll out the zone set (dry run first, then the same command without `-dry-run`):

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    rollout -kind zones -all -data /data -zone /data/zones/home.arpa.zone -dry-run -key /data/keys/release.pem
```

Repeat `-zone` for each zone in the set. `-empty` instead of `-zone` removes every hosted
zone from the nodes.

### Revert hosted zones

Each node keeps the previous zone bundle. Click **Revert zones** next to a node on the Zones
page and it switches back live, staying there until you push a newer one. If there's nothing
older, the node refuses and the page shows why.

From the command line:

```sh
docker compose run --rm -T --entrypoint /espdns espdns-controller \
    revert -host 192.0.2.53 -kind zones -data /data -key /data/keys/release.pem
```

## Secondary zones

A secondary zone lives on your own primary DNS server, and each node copies it by zone
transfer (AXFR) and answers with authority. Copies are kept on the SD card (in memory only,
on a node without one).

Any standards-compliant primary works: BIND, Knot, PowerDNS, Windows DNS, Technitium and
others. The nodes are plain secondaries: they check the SOA serial on a timer, copy the zone
again when it changes, and take NOTIFY if the primary sends it. The controller never logs in
to the primary and needs no credential for it.

### Set up the zone primary

Open **Zones** and expand **Zone primary**:

- **Address**: the primary's IPv4 address. The nodes copy each zone from it.
- **Networks it allows zone transfers from**: the networks you allowed on the primary (for
  example `192.0.2.0/24`). The hint names any node outside them, and the network to allow.
- **Zones to copy**: add or remove a name, then **Save**. Each change goes to every node's
  config and waits in **changes to apply**. Hosted zones are separate: add those with
  **+ Add zone**.

The first run has the same step, which you can skip. It's kept in `settings.json` in the
data directory:

```json
"primary": {
  "address": "192.0.2.254",
  "allows": ["192.0.2.0/24"],
  "zones": ["example.com", "home.arpa"]
}
```

A node added later starts with these zones. An older `settings.json` with `"kind"`, `"url"`
or `"cert_sha256"` under `"primary"` still loads; those fields are ignored, and the
controller says so once at start.

### On your primary: allow zone transfers from your nodes' networks

Do this once, on the primary itself:

1. Allow zone transfers (AXFR/IXFR) of each zone to the networks your nodes are on, for
   example `192.0.2.0/24`.
2. Optionally, send NOTIFY for the zone to the nodes, so a change reaches them at once.
   Without it, each node finds the change at its next SOA check (every 60 s by default,
   `soa_poll_s`).
3. Add the same networks under **Networks it allows zone transfers from** on Zones.

After that, adding a node changes nothing on the primary. If a new node's address is outside
those networks, adoption says once: "Your zone primary must allow zone transfers from
192.0.2.60: allow its network (e.g. 192.0.2.0/24) there once." It adds the node anyway.

> **Artie's tip:** allow the whole network, not each node's address. Then new birds join the
> flock without a trip to the primary.

### Secondary zones in a node config

The zones to copy end up in each node's config, under `"secondary"`:

```json
"secondary": {
  "primary": "192.0.2.254",
  "zones": ["example.com", "home.arpa"],
  "soa_poll_s": 60,
  "retry_s": 30
}
```

- `primary`: the IPv4 address of your primary. One primary per node.
- `zones`: up to 32 zone names.
- `soa_poll_s`: how often the node checks the SOA serial, in seconds (10 to 86400).
- `retry_s`: how long to wait after a failed check or transfer, in seconds (5 to 3600).

**Zone primary** and **+ Add zone** on the Zones page edit these for you, and a node's own
page can edit its config.

### When the nodes can't copy a zone

A zone the nodes can't copy shows **Can't copy it** on Zones. Its page says what to check:
that the primary allows zone transfers from your nodes' networks, and that it serves the
zone. The nodes keep answering from the copy they have until it expires.

### When a copy expires

If a node goes longer than the zone's SOA expire time without a good check against the
primary, its copy expires. The node stops answering from it and reports degraded, reason
`zone expired`. Check that the primary is up and still allows transfers from the node's
network.

## Forward zones

A forward zone sends every query for one domain to a server you choose: a partner network,
say, or an ACME challenge domain. Set them in each node's config:

```json
"forward_zones": [
  { "zone": "partner.example", "forwarder": "198.51.100.53" },
  { "zone": "_acme-challenge.example.com", "forwarder": "198.51.100.54" }
]
```

- Each entry has one zone and one forwarder (an IPv4 address).
- Up to 32 forward zones per node.
- A zone can't be both a forward zone and a secondary zone.
- `"forward_zones": []` means none.

Blocking is checked first, so a blocked name inside a forward zone is still blocked.

## Default forwarders

Every name that isn't in a zone, blocked or cached goes to the default forwarders, set in
the node config:

```json
"forwarders": ["9.9.9.9", "149.112.112.112"],
"upstream_timeout_ms": 1500
```

- Up to 4 IPv4 addresses, asked in order.
- Left out, the node uses its board's default, else the firmware's: Quad9 (`9.9.9.9` and
  `149.112.112.112`).
- `"forwarders": []` turns forwarding off. The node then answers only its own zones.
- `upstream_timeout_ms` is the time for one try, from 100 to 10000 ms.

Forwarders are plain DNS: UDP, with TCP for answers that don't fit.

## Live changes and reboots

These apply live: the default forwarders, `upstream_timeout_ms`, `soa_poll_s` and `retry_s`.
A change to the list of secondary zones, the secondary primary, or the forward zones needs a
reboot.

The **Configs** page shows which is which before you push. A rolling push reboots one node
at a time, and only while another node is answering.

## The zone inventory and lookup

The controller keeps an inventory of every zone the nodes answer for: hosted zones (serial
and record count), secondary zones (their primary and the newest copy's serial) and forward
zones (their forwarders). Each has a fleet-wide state: `ok`, `partial`, `waiting`
(configured but on no node yet), `expired`, `error` (the zone file fails its checks),
`no_file` or `unknown`.

It can also look up where a zone lives: the fleet's own zones first, then each configured
primary's SOA over DNS (UDP port 53, no recursion), so it works with any primary. The answer
is one of:

- `zone`: one of the fleet's zones;
- `primary`: a zone on one of your primaries, with its serial;
- `within`: a name inside one of the fleet's zones;
- `primary_within`: a name inside a zone a primary serves;
- `none`: nowhere yet. Host it here, or forward it.

Both are controller APIs today (`GET /api/zone-inventory` and
`GET /api/zone-lookup?name=<zone>`) and need a login. The Zones page doesn't show them yet.

## Quick reference

| Task | Where |
|---|---|
| Create, edit, check, save a hosted zone | Zones page |
| Push hosted zones | **Apply** (changes to apply); or `rollout -kind zones` |
| Revert hosted zones on one node | Zones page, **Revert zones**; or `revert -kind zones` |
| Add secondary zones, forward zones | Zones page, **+ Add zone**, then **Apply** |
| Set the zone primary, its networks and zones to copy | Zones page, **Zone primary**; or `settings.json`, `"primary"` |
| Let the nodes copy zones | On your primary: allow zone transfers from your nodes' networks, once |
