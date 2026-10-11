# Operating espDNS

Day-to-day work on a running flock: updates, the safety rules, nodes out of service, pinned
nodes, keys, backup, and getting a fleet back when the controller's data is gone. It assumes
a setup from [Getting started](getting-started.md); when something breaks, see
[Troubleshooting](troubleshooting.md).

espDNS is alpha (0.0.x): any version may change anything. Read the
[CHANGELOG](../CHANGELOG.md) before you update.

## Before you start

Run every command here from your clone's `controller/` directory, where `compose.yaml` and
`.env` live. The `espdns` CLI ships in the controller's image; run it on the controller's
data directory:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller <command> -data /data [flags]
```

- Without `-T`, the command gets a terminal. Use that when it asks for a password.
- With `-T`, it reads standard input from a pipe or file. Use that to pipe a secret in.
  Secrets are never command-line arguments.

Every command prints its flags with `-h`:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller rollout -h
```

## The safety rules

Every change to a node follows these, from the web page or the CLI. The page loosens none of
them.

- **One change at a time.** Every change takes the *fleet lock*. A second one stops at once
  with `the fleet is locked by <who> (<what>) ... since <time>`: wait and try again. The lock
  goes when its holder exits, even after a crash, so there's never a stale lock to clear.
- **One node at a time.** Change one node, check it, watch it for a while (the *soak*), then
  move on.
- **The canary goes first.** Set a default with `"canary"` in `settings.json`, or pick one
  per change. If the canary fails, nobody else is touched.
- **Never the last healthy node.** Before each node, another node or a *DNS peer* (a resolver
  your clients also use, such as your zone primary: `"dns_peers"` in `settings.json`) must be
  answering. A node that isn't adopted yet doesn't count, nor does one
  [out of service](#taking-a-node-out-of-service).
- **Nobody else unwell.** If another node or DNS peer is unhealthy, a change that may reboot
  a node (firmware, a config, a blocklist) doesn't start.
- **Dry run first.** Apply and adding a node run their own. On the CLI, use `-dry-run` first.

So you need **at least two nodes, or one node and a DNS peer**, in `settings.json`. A lone
node with no DNS peer has every change refused: `this is the only node clients use: they get
no DNS while it reboots or if the change breaks it`.

Every change is recorded, with who started it and how it ended, in `log/actions.jsonl` in the
data directory.

## Updating the controller

Back up first ([Backup](#backup)). Then set `ESPDNS_IMAGE` in `.env` to the new release's
image (`ghcr.io/skitzo2000/espdns-controller:<version>`, or the line in the release's
`espdns-<version>-controller-image.txt`, pinned by digest), and:

```sh
docker compose pull
docker compose up -d --no-build
```

Built from your own checkout instead (no `ESPDNS_IMAGE` in `.env`):

```sh
git pull
docker compose up -d --build
```

The image carries the project's release public key (`keys/project.pub`), which only checks
the project's releases ([Releasing](releasing.md#using-a-release)). Your nodes trust only
your keys, which stay in the data directory.

Check it came up and which version it runs:

```sh
docker compose ps
docker compose logs --tail 20
docker compose run --rm -T --entrypoint /espdns espdns-controller version
```

An update doesn't touch the data directory, and the nodes keep answering while the
controller is down.

## Updating node firmware

### Versions

The firmware and the controller share one version number. Each node gets a state from
comparing its version with the newest firmware the controller holds for it:

| State | Meaning |
| --- | --- |
| `available` | A newer build is there for this node. |
| `current` | The node runs the newest build there. |
| `newer` | The node runs a newer build than any the controller has. |
| `unordered` | The node runs firmware from before version numbers. Update it with a rollout. |
| `no_build` | The controller has no build for this node's chip image. |
| `unknown` | The controller has not read the node yet. |

Two builds of the same version are ordered by build time.

### Get the firmware into the controller

The controller loads chip images only as a **signed release**: the project's (checked with
`/keys/project.pub`) or your own (signed with your release key). Either way, your nodes only
ever see your key: the controller signs every update with it, after checking again that the
release still vouches for the app it sends.

- **The project's release:** **Get firmware** in the account menu's Firmware (or in Install
  over USB, for a board whose chip image isn't there yet), or:

  ```sh
  docker compose run --rm -T --entrypoint /espdns espdns-controller release fetch -data /data
  ```

  It downloads the newest release (`-version v0.0.2` for another), checks the project's
  signature and every file, and imports it as "project release v0.0.2".
- **Your own build:** build, package, sign and import as in
  [Build your own firmware](developer.md#build-your-own-firmware). Remove
  `build/release/dist` first: `release build` only writes into a missing or empty directory.

The import is the same whoever built it. For a release in `../build/release/dist`:

```sh
docker compose run --rm -T -v "$PWD/../build/release:/release:ro" --entrypoint /espdns \
  espdns-controller release import -dir /release/dist -data /data
```

It checks the signature (the project's key or yours, nothing else) and every file, and
imports nothing if any check fails. It takes the fleet lock, so it waits its turn. Chip
images land in `firmware/images/` in the data directory. Builds in `firmware/builds/` and
images you copy in by hand have no release behind them, and are sent as they are.

Every firmware update is refused for an image that doesn't read the keys partition (an older
build with keys built in), for a node in setup mode (reinstall it, or Write keys only over
USB), and for a node on pre-keys-partition firmware when the image's keys seed isn't that
node's keys.

**A node on firmware from before the keys partition** (`/status` has no `keys_source`)
trusts keys built into it, so a key-neutral image is refused for it. Give it, once, your own
build with its current public keys as the partition's seed: it writes them into its keys
partition on first boot (`keys_source` `seeded`, same fingerprints), keeps everything it
stores, and takes key-neutral releases from then on. Or reinstall it over USB, which writes
its keys and erases its settings.

### Update from the web page (the Apply flow)

Edits and firmware updates queue as *pending changes*. Nothing reaches a node until you
apply.

1. Open the controller, `http://127.0.0.1:8480`. **Nodes** says when an update is there,
   e.g. "Update available: 0.0.2 → 0.0.3".
2. Click **Update** (or **Update all**), check the from and to versions, and confirm.
3. Open the orange chip in the top bar ("2 changes to apply"). It lists each change, what it
   does and which nodes restart. You can discard one here.
4. Open **How it rolls out**. Pick the canary and how long to watch each node: 1, 5 or 15
   minutes.
5. Click **Apply** and follow it in the panel.

Apply checks that each file is still the version you edited and every node answers, saves
the files (keeping the old versions), compiles any blocklist, dry-runs every rollout, then
pushes: node configs, hosted zones, overrides, blocklists, then firmware, one node at a time,
canary first.

Each node boots new firmware on trial. If it isn't up and answering within 90 seconds, it
rolls back on its own.

**Stop after this node** stops at the next safe point. If an apply stops part way, the
changes stay pending: click Apply again and nodes that already have a change are skipped.

Apply needs the release key in the controller; without it, it's refused.

### Update from the command line

The same rollout, signed with the controller's copy of the key, `/data/keys/release.pem`.
Give one `-image` per chip image your nodes run (the `IMAGE` column of `espdns discover`).
Dry run first:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  rollout -kind firmware -all -mdns 0 -data /data -key /data/keys/release.pem \
  -canary 192.0.2.53 \
  -image /data/firmware/images/esp32p4-rev1 -image /data/firmware/images/esp32s3-eth \
  -image /data/firmware/images/esp32s3-octal \
  -dry-run
```

It stops at the first problem (an unreachable node, a chip image with no `-image`, a rule
that fails) and pushes nothing. If it passes, run it again without `-dry-run`.

- `-all -mdns 0`: every node in `settings.json`, and only those. `-host a,b` names them
  instead, in order.
- `-canary`: changed first. Without it, `settings.json`'s `"canary"` if it's one of the
  nodes, else the first.
- `-soak 5m` watches each node longer. The default is one minute.
- A node already on the build is skipped (`already had it`).
- It ends with `done: ...`, or `stopped at <node>` plus the nodes it didn't touch.

Check the result:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller status -all -mdns 0 -data /data
```

Every node should be `healthy`, on the new firmware, with no `reboot pending`. Ctrl-C stops a
CLI rollout at once, leaving the current node as below.

### When a rollout stops

At most one node is changed or rolled back; the rest still answer. Look at every node first
(the Nodes page, or `status -all` as above). Then:

- **`not started: no other node or DNS peer is answering`** or **`another node is
  unhealthy`**: nothing was pushed to that node. Fix the other node or DNS peer, then
  dry-run and roll out again. Nodes already updated are skipped.
- **The node rolled back** (it's on its old firmware): it should be healthy. Don't push the
  same build again until you know why it failed.
- **The node took the update but isn't healthy yet**: wait and look again. A node on trial
  rolls itself back within 90 seconds if it can't come up.
- **A node shows `reboot pending`**: the rollout was cut off between push and reboot. Reboot
  it under the same rules from its details on the Nodes page (**Reboot (pending)**), or:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller \
    reboot -host 192.0.2.54 -if-pending -mdns 0 -data /data -key /data/keys/release.pem
  ```

- **A blocklist was sent back** (`reverted (back on the copy each had): ...`): it failed its
  checks, so every node that took it went back to its previous list. Fix the list and start
  again from the dry run.
- **A node didn't answer**: if it's in the `not touched` list, it was never changed. Find out
  why before you go on.

> **Artie's tip:** a node that came home on its old firmware is doing its job. Find out why
> before you send the same build again.

## Taking a node out of service

Every change waits for every node to answer. So a node that's off, broken or away holds up
the rest of the flock. Take it out of service, and changes go on without it.

On its node page:

- **Take out of service.** It stays on Nodes, muted, with its last status. The controller
  sends it nothing: an apply, a rollout, a blocklist refresh and a config push skip it and
  say so. It doesn't count for the safety rules either, up or down. Works while the node is
  offline. If it's up, it still answers DNS.
- **Return to service.** The node must answer and trust the controller's release key. The
  controller then sends it, on its own, what it missed: its settings, the hosted zones, the
  allowed and blocked sites, its blocklist, and newer firmware if it's behind. Each gets a
  dry run first, as an apply does. Only then does it count again. If it doesn't answer, it
  stays out, and the page says why.
- **Remove from the group.** It comes off your list of nodes. Its config file is kept, and
  the node isn't touched: if it's up it keeps answering DNS, so take it off your router's
  DNS servers too. While it's on your network it shows up under New device found, and you
  can add it back the usual way.

Each asks first, and each is in the action log. Remove is refused while a job runs on the node.

Keys: a rotation won't start while a node is out of service (return it or remove it first),
and a node in a rotation under way can't be taken out or removed.

From the command line: [`espdns node out|in|remove`](reference/cli.md#node).

> **Artie's tip:** out of service isn't out of mind. Bring it back, or remove it, before you
> forget which router still points at it.

## Pinned nodes

The controller signs a release for a node only when it's **pinned** to its address. Pins
live in `pins/` in the data directory, one file per node: its ID (the chip's MAC address),
the pinned address, and the last sequence number (*seq*) signed for it, per kind of release.

Why: anything on a node's address can send a `/status`, a device that took the address or a
spoofed reply. So the controller signs for the ID pinned to the address, with the next seq
it recorded, never for the ID a `/status` claims. If the address answers as another node,
the push is refused.

A node is pinned when it's adopted, when `espdns recover` finds it
([Recovering from the nodes](#recovering-from-the-nodes)), when a config moves it and it's
confirmed at the new address, or by hand. List the pins and seqs:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller pin -data /data
```

Pin by hand when you replace a board and put the new one at the old address. The node must
answer there as that ID (the `NODE` column of `espdns discover`, Details on its page, or its
label):

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  pin -data /data -host 192.0.2.54 -node <its ID>
```

The old board's pin moves off the address and keeps its seqs, in case it comes back.

Identify (flickering the LED) is the one exception: it's signed for a node that isn't adopted
yet, so you can find the board in your hand. At worst an LED flickers.

## A node without keys (setup mode)

A node trusts the two public keys in its keys partition, which Install over USB writes from
the controller's `keys/release.pem` and `keys/recovery.pub` (no image is prepared until both
are there). A node with no keys, or a damaged keys record, comes up in **setup mode**: it
answers DNS on its board's defaults, reports `no keys`, and refuses every release. Nothing
over the network can give it keys.

+ Add node shows it with **Write keys over USB**. Plug it in, choose **Write keys only (keeps
settings)** and pick its port. The page writes the 8 KB keys partition at 0x1b000 and
nothing else, reads it back, and restarts the board. If it can't reach the board, hold BOOT
while you plug it in and try again.

## Rotating keys

Every node trusts two public keys: the release key the controller signs with, and the
recovery key you keep offline. A rotation replaces either or both, as a KEYS release per
node signed offline with the recovery key the nodes trust now. The why is in
[security.md, Rotating keys](security.md#rotating-keys).

Rotate:

- **routinely**, to retire a long-used release key or move to your own keys. Each node keeps
  the old key as its *retired key* until the push re-signs what it stores, right after;
- **after a compromise** (the release key or data directory may have leaked), at once, with
  `-compromised`. The nodes drop the old key straight away. Rotate from a controller you
  trust again: rebuilt from an earlier backup, or from scratch.

Every node in `settings.json` must be pinned, answering, and on firmware that takes KEYS
releases (`seq.keys` in its `/status`).

### 1. Prepare

The account menu, **Rotate keys**, **Prepare**. Or:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller \
  key rotate prepare -data /data -new-release generate
```

| Flag | Meaning |
|------|---------|
| `-new-release` | `generate`: a new release key, made in the controller. Or a private key file (PEM, or base64 of one). Leave it out to keep the release key. |
| `-new-recovery` | The new recovery key's **public** half (`recovery.pub`, raw or PEM). Leave it out to keep the recovery key. |
| `-recovery-pub` | The recovery public key the nodes trust now, if the controller doesn't have it (`keys/recovery.pub`). |
| `-compromised` | Drop the old release key on the nodes at once. Needs a new release key. |

Nothing is sent. It writes each node's unsigned KEYS release and a `plan.json` into
`keys/rotation/<id>/`, and the new release key into `keys/release.next.pem`. It says, per
node, what can and can't be re-signed with the new key. After a compromise, a node with
something the controller can't re-sign is refused: push that from the controller first, then
prepare again.

### 2. Sign it offline

On the machine that holds your recovery key (it never goes to the controller), from the
controller's directory. The container gets no network and only the rotation's directory:

```sh
docker run --rm -i --network none --user "$(id -u):$(id -g)" -v "$PWD/data/keys/rotation/<id>:/rotation" \
  --entrypoint /espdns espdns-controller key rotate sign -dir /rotation -key - < recovery.pem
```

The page shows this command with the ID filled in. If `.env` sets `ESPDNS_DATA`, use that
directory instead of `$PWD/data`; if it sets `ESPDNS_IMAGE`, use that image instead of
`espdns-controller`.

The key comes on standard input: a PKCS#8 or SEC1 PEM, or one line of base64 of either (as a
password manager keeps it). Any key but the one your nodes trust is refused. It prints each
node's new key fingerprints and writes its signed `release.bin` beside the unsigned files.
The key is written nowhere.

Signing on another machine: copy the rotation's directory there and back, or add each node's
`release.bin` in the panel (**Signed elsewhere?**). `firmware/tools/release.py keys-sign
--dir <the node's directory> --key recovery.pem` signs one node the same way.

### 3. Push

**Push** on the page, or:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller key rotate push -data /data -dir <id>
```

One node at a time, under the fleet lock: the KEYS release, the new keys checked, then its
config, hosted zones, overrides and blocklist re-signed and pushed, its health checked. A
failure stops there and says why; fix it and push again to carry on from that node. Nodes
taken out of `settings.json` are left out. Add `-node <its address>` to do one node per push.

When every node reports the new keys, the controller swaps its own: `keys/release.next.pem`
becomes `keys/release.pem`, the old one is kept as `keys/release.prev.pem`, and
`keys/recovery.pub` becomes the new recovery key. After a compromise, delete
`release.prev.pem` once you've looked at it. Then make a new backup.

Discard a rotation no node has taken yet with **Discard**, or:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller key rotate discard -data /data -dir <id>
```

The older copies a node keeps for a revert aren't re-signed, so after a compromise,
reverting to one is refused.

## Backup and restore

The controller keeps everything in one data directory (`ESPDNS_DATA` in `.env`, `./data` by
default): settings, node configs, hosted zones, blocklists, pins, the action log, the login
and the release key. The nodes don't need it to answer DNS, but losing it means re-importing
secrets and rewriting every config and zone.

Back up before a risky change (a firmware update, an adoption, a config that moves a node)
and after one that went well.

### What a backup is

One file, encrypted with [age](https://age-encryption.org) to a passphrase of at least 12
characters or to age public keys. It holds the release key (encrypted) and the recovery
key's public half, never the recovery key itself. Keep the passphrase apart from the file.

Left out: the fleet lock, temporary files, `firmware/` (large, and you can import it again),
and the unused zone primary API token an older controller kept (`keys/primary.token`; a
restore skips one an older backup carries, and says so). A wrong passphrase, a changed byte
or a file cut short is refused. A backup holds the fleet lock while it reads.

### Backup

**On the web page:** **System**, then **Backup**. Type a passphrase and download. The page
asks for your password first.

**On the command line,** with the passphrase from an environment variable or a file's first
line. The backup goes to standard output:

```sh
printenv ESPDNS_BACKUP_PASSPHRASE | docker compose run --rm -T --entrypoint /espdns \
  espdns-controller backup -data /data -out - > espdns-backup-2026-10-07.age

docker compose run --rm -T --entrypoint /espdns espdns-controller \
  backup -data /data -out - < passphrase.txt > espdns-backup-2026-10-07.age
```

`-recipient age1...` encrypts to an age public key instead (repeat for several). `-firmware`
includes `firmware/`. If a change holds the lock, the backup waits up to 10 minutes
(`-wait`).

### Check a backup

A restore dry run checks the passphrase, every path, size and sum, the settings, the key and
the login, and writes nothing:

```sh
docker compose run --rm -v "$PWD/espdns-backup-2026-10-07.age:/backup.age:ro" \
  --entrypoint /espdns espdns-controller restore -data /data -in /backup.age -dry-run
```

### Restore

A restore goes into an empty data directory with the controller stopped. On a new host, set
up `.env` and an empty data directory as in
[Docker Compose](developer.md#docker-compose).

1. Stop the controller:

   ```sh
   docker compose down
   ```

2. Check the backup with the dry run above.
3. Restore it (the passphrase is asked once):

   ```sh
   docker compose run --rm -v "$PWD/espdns-backup-2026-10-07.age:/backup.age:ro" \
     --entrypoint /espdns espdns-controller restore -data /data -in /backup.age
   ```

   - No terminal: add `-T` and pipe the passphrase as the first line (`< passphrase.txt`).
   - Made with `-recipient`: give the age identity file with `-identity`, mounted like the
     backup.
   - A data directory that isn't empty is refused; `-force` moves its contents into
     `.before-restore-<time>/` first. Nothing is deleted, and nothing goes in place until
     every check passes.

4. Start the controller and log in with the restored login:

   ```sh
   docker compose up -d
   ```

5. If the backup had no firmware, load it again
   ([Get the firmware into the controller](#get-the-firmware-into-the-controller)).

## Recovering from the nodes

No backup? The nodes still run, and a fresh data directory can manage them again: the flock
homes back to a new coop. `espdns recover` rebuilds what it can, and only reads the nodes.

1. Set up a new controller with an empty data directory
   ([Docker Compose](developer.md#docker-compose)).
2. Import the release key the nodes trust, with their public keys to check it against (here
   in `~/espdns-keys/`; Keep the controller safe in the account menu downloads
   `release.pub`, and any node's `/status` shows both fingerprints):

   ```sh
   docker compose run --rm -T -v "$HOME/espdns-keys:/mykeys:ro" --entrypoint /espdns espdns-controller \
     key import -data /data -key - -pub /mykeys/release.pub -recovery-pub /mykeys/recovery.pub \
     < "$HOME/espdns-keys/release.pem"
   ```

   Lost the release key? Then nothing reaches the nodes over the network. Make new keys (the
   first run's Make your keys), give each node them over USB with **Write keys only (keeps
   settings)** ([A node without keys](#a-node-without-keys-setup-mode)), then push each
   node's config, zones and lists again.

3. Set the login (a user name, then the password twice):

   ```sh
   docker compose run --rm --entrypoint /espdns espdns-controller passwd -data /data
   ```

4. See what the nodes give back, writing nothing:

   ```sh
   docker compose run --rm --entrypoint /espdns espdns-controller recover -data /data -dry-run
   ```

   It finds nodes over mDNS and in `settings.json`; add `-host 192.0.2.53,192.0.2.54` for
   ones mDNS can't reach. Anything can answer mDNS as a node, so read the list before going
   on. `-mdns 0 -host ...` reads only the addresses you give.

5. Run it without `-dry-run`. It writes `settings.json` if there's none, with the nodes in
   service that trust your key (`-add-to-settings` adds them to an existing one); pins each
   one; keeps each `/status` and a report in `recovered/<time>/`; and says which config file
   here matches each node and whether the zone files here match what it serves.

The nodes can't give back config contents, hosted zone files, blocklists, the DNS peers, zone
primary and canary in `settings.json`, or the history and push records. Add those settings
yourself and write the missing configs and zones. Compare each config with what its node runs
before you push it, and write every hosted zone before you push zones: a zones push replaces
the whole set, so a zone left out stops being served.

Seqs need nothing: a new release's seq is the current time in milliseconds, above the node's
last one as long as the controller's clock is right.

## Releases

A release is one version of firmware and controller together, its `SHA256SUMS` signed with
the project's key; its chip images are key-neutral. [Releasing](releasing.md) covers what it
holds and how to check one with `espdns release verify`.
