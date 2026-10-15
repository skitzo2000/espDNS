# Controller settings

The controller reads three sets of settings:

1. [`settings.json`](#settingsjson) in the data directory: the deployment's nodes, DNS peers
   and zone primary. The controller and the `espdns` CLI both read it.
2. [The controller's flags](#controller-flags), set in `compose.yaml`.
3. [`.env`](#env-variables) next to `compose.yaml`, which Docker Compose reads.

Back to the [reference index](README.md).

## settings.json

Path: `<data>/settings.json` (`/data/settings.json` in the container). Edit it on the
dashboard's Settings page (which writes it whole) or by hand.

- Every field is optional. A missing file means "no settings" (all fields empty).
- Unknown fields are refused, so a misspelt field shows up as an error.
- The file must hold exactly one JSON object.
- The controller rereads it every 10 seconds, so a change applies without a restart. A file
  that no longer parses leaves the node list as it was.
- The file is written with mode `0600`.

### Example

```json
{
  "nodes": ["192.0.2.53", "192.0.2.54"],
  "dns_peers": ["192.0.2.1"],
  "dns_peer_zones": ["home.arpa"],
  "canary": "192.0.2.53",
  "no_dhcp": ["192.0.2.0/24"],
  "primary": { "address": "192.0.2.1", "allows": ["192.0.2.0/24"], "zones": ["home.arpa"] }
}
```

### Fields

| Field | Type | Meaning | Checks |
|-------|------|---------|--------|
| `nodes` | list of strings | Your nodes. The controller polls each one, even if mDNS doesn't find it. An apply changes only these nodes, those in `out_of_service` aside, and the node actions (all but identify) take only these. The CLI counts them (`out_of_service` aside) as peers for the last-healthy-node rule. Each entry is `host` or `host:port` (for an HTTP port other than 80). | Not empty, no spaces, tabs or `/`, no leading or trailing space. No duplicates. A `host:port` must have a port. |
| `out_of_service` | list of strings | Nodes taken out of service: still listed, polled and shown, but an apply, a rollout, a refresh and a config push skip them, and they don't count for the last-healthy-node rule. A key rotation isn't prepared while one is here. The node page's **Take out of service** and **Return to service** set it (returning brings the node up to date first: [operations.md](../operations.md#taking-a-node-out-of-service)). Empty: every node is in service. | Each must be one of `nodes`. No duplicates. |
| `dns_peers` | list of strings | Other resolvers your clients use besides the nodes (for example the zone primary). Each counts as one answering peer while it resolves a forwarded name and answers its zones' SOA authoritatively. Entry is `host` or `host:port` (for a DNS port other than 53). | Same as `nodes`. |
| `dns_peer_zones` | list of strings | The zones the DNS peers must answer for. Empty: the secondary zones of the node about to change. | Not empty, no spaces, tabs or `/`. No duplicates. |
| `canary` | string | The node an apply changes first, unless you choose another. Empty: the first node changed. | Must be one of `nodes`. |
| `no_dhcp` | list of strings | IPv4 networks with no DHCP server, each as its network address with prefix length (`192.0.2.0/24`). A node on one of these is never given a DHCP config by adoption. Empty: none. | Must parse as an IPv4 prefix. Must be the network address itself (`192.0.2.0/24`, not `192.0.2.5/24`). No duplicates. |
| `primary` | object | The zone primary. See [primary](#primary). Absent: none set up. | See below. |
| `release_url` | string | The repository the controller asks for the newest firmware release, and Get firmware, firmware updates and `espdns release fetch` download the project's releases from: a GitHub repository, such as a fork's, `https://github.com/<owner>/espdns`. Empty or absent: `https://github.com/skitzo2000/espdns`. A release is taken only if the project's key signed it, wherever it comes from. See [Releasing](../releasing.md#using-a-release). | An `https` URL with a host and `/<owner>/<repo>`. |
| `internal_sources` | list of strings | The internal allowlist: hosts (a name or an IP address, no scheme or port) a blocklist URL source may be fetched from over plain http or at an inside (private, loopback, link-local) address. Such a fetch goes to that host only. Empty: URL sources are https from public addresses only. See [Blocking](../blocking.md#lists-hosted-inside-your-network). | Each must be a host name or an IP address. |
| `blocklist_refresh` | string | How often the controller checks the blocklists it downloads (a list's URL sources) for a new version, and sends a changed one to the nodes: `"off"`, or a time such as `"6h"`, `"12h"`, `"24h"`, `"168h"`. Empty or absent: every day (`"24h"`). The Blocking page's **Check for new versions** sets it. See [Blocking](../blocking.md#keep-the-lists-up-to-date). | `"off"`, or a Go duration from `1h` to `720h` (30 days). |

A host given by name in `nodes` must be a name the node answers to: the `name` in its node
config, or its `.local` mDNS name. A node refuses any other `Host` (HTTP 421). An IP address
always works.

### primary

```json
{ "address": "192.0.2.1", "allows": ["192.0.2.0/24"], "zones": ["home.arpa"] }
```

The zone primary the nodes copy their secondary zones from. Any standards-compliant primary
works (BIND, Knot, PowerDNS, Windows DNS, Technitium and others): each node copies a zone by
AXFR/IXFR, checks its SOA for changes, and takes the primary's NOTIFYs if it sends them.

Set the primary up once to allow zone transfers (and, if you like, NOTIFY) from your nodes'
networks; adding a node then changes nothing there. The controller never changes the
primary. It only sends it DNS queries (a zone lookup's SOA query, or an AXFR when you open a
copied zone).

| Field | Meaning | Checks |
|-------|---------|--------|
| `address` | The primary's address, as a node config's `secondary.primary` names it. | An IPv4 address. |
| `allows` | The networks the primary allows zone transfers from, as you set it up. When a node's address is in none of them, adoption says once what to allow there; it never stops on it. The Zones page names the nodes outside them. | Each an IPv4 network with its prefix length, as its network address (`192.0.2.0/24`). No duplicates. |
| `zones` | The zones to copy from the primary. A new node's config starts with them; the node configs say what each node copies. | Each a zone name in lower case, without the final dot. No duplicates. |

All three are optional. Edit them on the Zones page (Zone primary); a zone copied from a
primary with + Add zone, there or in the setup, fills in its address and the zone.

An older controller's `settings.json` may have `"kind"`, `"url"` and `"cert_sha256"` here,
from when it drove a primary's API. They're accepted and ignored: the controller logs once at
startup that they're unused, takes the IP address in `"url"` (if it is one) as `address` when
none is given, and drops them the next time it writes the file.

## Controller flags

Flags of the controller binary (`/espdns-controller` in the image). The image's entrypoint
already sets `-data /data -catalog /catalog`; `compose.yaml` sets `-listen` from
`ESPDNS_LISTEN`, and passes `ESPDNS_URL` and `ESPDNS_PROXY` through as environment variables,
which `-url` and `-proxy` default to.

| Flag | Default | Meaning |
|------|---------|---------|
| `-listen` | `127.0.0.1:8480` (`ESPDNS_LISTEN`) | Address for the dashboard and API. A loopback address (`127.0.0.1`, `[::1]` or `localhost`) with any port. Any other address (`0.0.0.0:8480`) only with `-url` and `-proxy`; else the controller refuses to start. |
| `-url` | `ESPDNS_URL`, else none | The controller's address at an HTTPS reverse proxy: `https://name` or `https://name:port`, nothing after it. Empty: localhost only. See [security.md, Behind a reverse proxy](../security.md#behind-a-reverse-proxy). |
| `-proxy` | `ESPDNS_PROXY`, else none | The reverse proxy's source addresses or networks (`192.0.2.10`, `192.0.2.0/28`), comma-separated: the proxy's own, never your whole LAN. Needs `-url`. |
| `-data` | `data` (image: `/data`) | The data directory. |
| `-catalog` | `../boards` (image: `/catalog`) | The shipped board catalog. |
| `-release-url` | `https://github.com/skitzo2000/espdns` | The repository the controller checks for new firmware releases and downloads them from, when `settings.json` has no `release_url`. |
| `-project-pub` | `/keys/project.pub` | The project's release public key: a release whose `SHA256SUMS` it signed (or your release key signed) is imported. No node trusts it. |
| `-browse-mdns` | `true` | Browse mDNS for nodes besides `settings.json`'s. `false`: poll only the listed nodes. |
| `-check-mdns` | `3s` | How long a `check` job browses mDNS for extra nodes. `0`: don't. |
| `-healthcheck` | `false` | Only check that a controller answers on `-listen`, then exit 0 (yes) or 1 (no). The container's health check uses it. |

## .env variables

`compose.yaml` reads `.env` from the `controller/` directory. Copy `.env.example` to `.env`
and edit it.

| Variable | Default | Meaning |
|----------|---------|---------|
| `ESPDNS_UID` | none (required) | The user ID the controller runs as. Use the owner of the data directory (`id -u`). Compose refuses to start without it. |
| `ESPDNS_GID` | none (required) | The group ID (`id -g`). Required. |
| `ESPDNS_LISTEN` | `127.0.0.1:8480` | The dashboard's address. Loopback only, unless `ESPDNS_URL` and `ESPDNS_PROXY` are set. |
| `ESPDNS_URL` | empty | The controller's `https://` address at your reverse proxy, such as `https://espdns.example.net`. Empty: localhost only. |
| `ESPDNS_PROXY` | empty | The reverse proxy's source addresses or networks, comma-separated: the proxy's own, never your whole LAN. Needs `ESPDNS_URL`. |
| `ESPDNS_DATA` | `./data` | The data directory on the host, mounted at `/data`. It must exist and be owned by `ESPDNS_UID`. Compose never creates it. |
| `ESPDNS_MEM_LIMIT` | `2g` | The container's memory limit. Compiling a blocklist needs about 220 bytes per domain at its peak. |
| `ESPDNS_GOMEMLIMIT` | `1800MiB` | The Go runtime's soft memory limit. Keep it about nine tenths of `ESPDNS_MEM_LIMIT`. |
| `ESPDNS_PIDS_LIMIT` | `256` | The container's process and thread limit. |
| `ESPDNS_IMAGE` | `espdns-controller` | The image to run. A release's published image, `ghcr.io/skitzo2000/espdns-controller:<version>` (or the line of its `espdns-<version>-controller-image.txt`, by digest), runs with `docker compose pull` and `docker compose up -d --no-build`. `docker compose up -d --build` builds from the checkout and tags the image with this name. |
| `ESPDNS_CONTAINER` | `espdns-controller` | The container name. |

## The data directory

Everything the controller keeps lives here. Directories are mode `0700` and files `0600`,
whatever the umask; at start the controller tightens anything it finds open to group or
others.

| Path | What it is |
|------|------------|
| `settings.json` | [Deployment settings](#settingsjson). |
| `auth.json` | The login: the user name and an argon2id hash of the password. |
| `keys/release.pem` | The release signing key (`espdns key import` or `key generate`, or the first run's Make your keys). |
| `keys/recovery.pub` | The recovery key's public half, the 65-byte point (the first run's Make your keys). The private half is never here. |
| `keys/release.next.pem`, `keys/release.prev.pem` | During a key rotation, its new release key; after one, the release key it replaced. |
| `keys/rotation/<id>/` | A key rotation: each node's KEYS release, unsigned and signed, and `plan.json`. |
| `configs/<name>.json` | [Node configs](node-config.md). |
| `zones/<zone>.zone` | Hosted zones (RFC 1035 master files). |
| `blocking/lists.json` | The blocklist definitions: what each list is compiled from. |
| `blocking/sources/` | Local list sources, allow lists and overrides. |
| `lists/` | Compiled blocklist and overrides files. |
| `boards/` | Your own board definitions. |
| `firmware/images/<image>/` | Chip images (`espdns release import` or `fetch`, Get firmware), each with its `provenance/`: the signed `SHA256SUMS`, its signature, the image's tarball and who signed it, checked again before every update. |
| `firmware/latest` | The newest firmware release's version, as the controller last heard it (at start, every 24 hours, and when a new node shows up). Nothing is downloaded with it. |
| `firmware/builds/<board>/` | Per-board firmware builds for rollouts. |
| `changes/` | Pending changes, waiting for one apply. |
| `fleet.lock` | The fleet lock: one fleet change at a time. |
| `log/actions.jsonl` | The action log: every fleet change and who made it. |
| `log/jobs/<id>.json` | Each job's record and progress. |
