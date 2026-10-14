# Security policy

## Reporting a vulnerability

Please report security problems privately. Do not open a public issue.

Use GitHub's private vulnerability reporting: on
[github.com/skitzo2000/espdns](https://github.com/skitzo2000/espdns), open the **Security**
tab and choose **Report a vulnerability**
([direct link](https://github.com/skitzo2000/espdns/security/advisories/new)). Only you and
the maintainers see the report.

Please include:

- the version (`docker compose run --rm -T --entrypoint /espdns espdns-controller version`
  from `controller/`, or `"version"` in a node's `/status`);
- the part affected: the node firmware, the controller, the CLI or a release;
- the steps to reproduce it, and what an attacker gains;
- whether the attacker needs to be on the local network, logged in to the controller, or
  on the controller's machine.

Leave out your own addresses, zone names and secrets. We'll confirm we got your report,
keep you updated while we fix it, and credit you when the fix is released, unless you ask
us not to. Please give us a reasonable time to release a fix before you publish details.

## Supported versions

espDNS is alpha software. Only the newest 0.0.x release gets security fixes. There are no
backports, so update to the newest release.

| Version | Security fixes |
|---|---|
| Newest 0.0.x | Yes |
| Older 0.0.x | No |

## Security model in brief

The full model is in [docs/security.md](docs/security.md).

### Nodes

- A node takes only signed releases (firmware, config, zones, blocklists, overrides,
  control commands), ECDSA P-256, each naming the node (its chip ID), the chip image and a
  sequence number. Another node's or chip's release, a bad signature, or a sequence number
  not above the last one applied is refused, so replays and downgrades fail, even after a
  reboot.
- A node trusts two public keys, both yours, in its keys partition: a release key and an
  offline recovery key, written by the controller's Install over USB. Firmware images hold
  no key and no secret. Over the network, only a release signed with the recovery key can
  change a node's keys (a key rotation).
- A node accepts releases, serves its query log and recurses only for private source
  addresses.
- A node's web server answers only requests whose `Host` names the node, which stops DNS
  rebinding from a browser on your network.
- A node never contacts the controller. The controller always connects to the node.

### The controller

- It listens on localhost only (plain HTTP). Use it from the machine it runs on.
- One login, the password kept as an argon2id hash. Failed logins are slowed down. Backups
  and revealing a Wi-Fi password ask for the password again.
- Every page sends a strict Content-Security-Policy and other security headers.
- It runs in Docker as your user, with no Linux capabilities and a read-only root
  filesystem. Its data directory (settings, the login, the release key) is readable by
  your user only.
- It signs each release for the node it pinned at adoption, with its own record of
  sequence numbers, not what the node's address claims.
- Backups are encrypted with a passphrase.

### Known gaps (alpha)

- Releases travel to the nodes over plain HTTP: signed, not encrypted.
- A node's `/status`, `/health` and `/metrics` are open to anyone who can reach it.
  `/metrics` holds no names clients looked up; `/status` does describe the node.
- A node answers DNS from any address (recursion only for private ones), and any private
  address can read its query log. Put the nodes on a network you trust.
