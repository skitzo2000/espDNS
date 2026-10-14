# Reference

Exact lists for espDNS 0.0.x (alpha), written from the code. The guides tell the story; the
facts roost here. If a page and the code disagree, the code wins: please open an issue.

| Page | What it covers |
|------|----------------|
| [Controller HTTP API](api.md) | Every route the controller serves: method, path, login needed, body, reply. Also the job kinds you start through the API. |
| [espdns CLI](cli.md) | Every `espdns` command and flag, run through `docker compose`. |
| [Controller settings](settings.md) | `settings.json` (every field and its checks), the controller's own flags, and the `.env` variables `compose.yaml` reads. |
| [Node config](node-config.md) | The node config JSON a node runs: every key, its range, its default, and whether a change applies live or at a reboot. |
| [Node endpoints](node-endpoints.md) | What a node answers over HTTP: `/health`, `/status` (every field), `/metrics` (every metric), `/querylog`, `/zone`. |

## Words used on these pages

- **Node**: an ESP32 board running the espDNS firmware and answering DNS.
- **Controller**: the management service in the `espdns-controller` container. It serves the
  dashboard and the API on `http://127.0.0.1:8480`. Nodes keep answering DNS when it's off.
- **Data directory**: where the controller keeps all its state: `/data` in the container,
  mounted from the host (`ESPDNS_DATA` in `.env`, `./data` by default).
- **Release**: a signed change sent to a node (firmware, config, zones, blocklist, overrides
  or a control command). Nodes take only releases signed with a key they trust.
- **Zone primary**: the DNS server the nodes copy their secondary zones from, over standard
  AXFR/IXFR and NOTIFY. Any standards-compliant primary works (BIND, Knot, PowerDNS,
  Windows DNS, Technitium and others).

## Where to go next

- How it fits together: [Architecture](../architecture.md).
- Setting it up: [Getting started](../getting-started.md); Compose and own builds: [Developer guide](../developer.md).
- How a release is built, signed and checked: [releasing.md](../releasing.md).
- What changed in each version: [CHANGELOG.md](../../CHANGELOG.md).
