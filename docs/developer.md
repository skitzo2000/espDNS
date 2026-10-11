# Developer guide

Everything [Getting started](getting-started.md) leaves out: running the controller with
Docker Compose, the command line, the setup's finer points, how firmware is checked, and
building your own.

The examples use documentation addresses. Swap in your own:

| Example | What it stands for |
|---|---|
| `192.0.2.0/24` | Your LAN |
| `192.0.2.1` | Your router (the gateway) |
| `192.0.2.53`, `192.0.2.54` | The two nodes' static addresses |

## Docker Compose

This setup and the one in [Getting started](getting-started.md) share the project name
`espdns` and the container `espdns-controller`: run one or the other. Switching to the
Getting started file replaces this container, and it uses the `data` folder beside that
file, not your data directory.

The same published image, run from a release checkout. You need the `docker compose`
plugin v2.17 or newer (`docker compose version`) and git. From the repository you only need
`controller/compose.yaml` and `controller/.env.example`, so clone the release's tag (nothing
is built). Set `V` to the newest version on the
[releases page](https://github.com/skitzo2000/espdns/releases):

```sh
V=0.0.2
git clone --depth 1 --branch "v$V" https://github.com/skitzo2000/espdns.git
cd espdns/controller
```

Make the data directory and settings file, then start it. Keep the data directory outside
the checkout: it will hold your release key.

```sh
mkdir -m 700 "$HOME/espdns-data"
printf 'ESPDNS_UID=%s\nESPDNS_GID=%s\nESPDNS_DATA=%s\nESPDNS_IMAGE=%s\n' \
  "$(id -u)" "$(id -g)" "$HOME/espdns-data" "ghcr.io/skitzo2000/espdns-controller:$V" > .env
docker compose pull
docker compose up -d --no-build
```

- `.env` sets the user the controller runs as (you, so its files stay yours) and the image.
  Without it Compose stops with `required variable ESPDNS_UID is missing a value`.
  [`.env.example`](../controller/.env.example) lists the other settings.
- To pin the image by digest, use the line in the release's
  `espdns-<version>-controller-image.txt` as `ESPDNS_IMAGE`.
- `--no-build` runs the published image as is; without it Compose builds from the checkout.
- Compose won't make the data directory, and the controller won't start if it can't write
  there.
- The project's public key in the image, `/keys/project.pub`, only checks the project's
  releases. No node trusts it.

Compose names the container `espdns-controller`, like the `docker run` in Getting started,
so run one or the other.

Check it's healthy:

```sh
docker compose ps
docker compose logs -f
```

Day to day, from `controller/`:

```sh
docker compose down             # stop it (the nodes keep answering DNS)
docker compose up -d --no-build # start it again
```

**Updates.** Set `ESPDNS_IMAGE` in `.env` to the new release's image, run
`docker compose pull` and `docker compose up -d --no-build`, press **Get firmware**, then
update the nodes from the Nodes page ([Operations](operations.md)).

## The command line

The `espdns` CLI is in the controller's image, at `/espdns`. With the container from
Getting started, run it with `docker exec`:

```sh
docker exec espdns-controller /espdns <command> -data /data
```

Add `-it` for a command that asks you something, such as `passwd`.

With Compose, from `controller/`:

```sh
docker compose run --rm --entrypoint /espdns espdns-controller <command> -data /data
```

The rest of the docs use the Compose form. Every command is in the
[CLI reference](reference/cli.md).

The browser's setup steps, from the command line:

- **Set a password.** It asks for a user name, then the password twice, without echo:

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller passwd -data /data
  ```

- **Get firmware**, for a given version:

  ```sh
  docker compose run --rm -T --entrypoint /espdns espdns-controller release fetch -data /data -version "v$V"
  ```

  For a mirror on another forge, use `-url`, or `"release_url"` in
  `settings.json` for the pages ([Settings](reference/settings.md)). Either way, only a
  release the project's key signed is taken.

- **Check the nodes:**

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller status -all -data /data
  ```

## Setup in detail

**The password.** One user. The dashboard is read-only until a password is set, and the
first one can only be set from a browser on the same machine. It's stored as an argon2id
hash.

**The keys.** A node takes a change only if it's signed with the **release key** whose
public half is in its keys partition. Every change counts: firmware, configs, zones,
blocklists, even "flash your LED" and "reboot".

| Key | Used for | Where it lives |
|---|---|---|
| **Release key** (slot 0) | Signing every change | The controller's data directory (`keys/release.pem`) |
| **Recovery key** (slot 1) | Rotating the nodes to new keys, if the release key is lost or leaked | Offline only. The controller keeps its public half |

The controller makes the release key (mode 0600). Your browser makes the recovery key and
sends the controller only the public half. Firmware images hold no key, so the project's
firmware works with yours. Neither button replaces an existing key.

**Installing a board.** The address goes into the node's board partition, never into the
saved board. Without both keys you get "set up your keys first". The installed node boots
on its address with your keys, but isn't added yet: it runs on board and firmware defaults,
forwarding to public resolvers (Quad9, `9.9.9.9` and `149.112.112.112`).

**Setup mode.** A node without keys (installed another way, or with a damaged keys record)
comes up in setup mode: it answers DNS but takes nothing over the network, and
**+ Add node** says so. **Write keys only (keeps settings)**, under Advanced in the same
panel, writes just your keys: no erase, and the keys are read back before the board
restarts ([Operations](operations.md#a-node-without-keys-setup-mode)).

**Wi-Fi boards.** A board with no Ethernet uses Wi-Fi (2.4 GHz only); the browser asks for
the network after installing. Use it only where you can't run a cable.

**Adding a node.** Installed nodes show up on the Nodes page as **New device found** (via
mDNS). The address is one of:

- **A fixed address on the node**, with its **Gateway**. The gateway must be in the node's
  own network (its next hop, whatever your router routes beyond it). One that isn't is
  marked, and **Add node** waits until they fit.
- **DHCP, reserved for it in your router**: reserve its current address for its MAC (copy
  it from the panel) in your router first. Not offered on a network listed in
  `settings.json`'s `no_dhcp`.

Picking an existing name fills the form from its config, and your choices are saved back
to it. A name no node uses has **Delete this name**, which asks first; the file stays in
its history. **Add node** tries the node first, changing nothing. If its address isn't in a
network your zone primary allows zone transfers from, it tells you once which to allow.

Node not listed? mDNS doesn't cross subnets: type its address in **+ Add node**.

## How Get firmware checks a release

Nodes run **chip images**: one per chip, the same for everyone. **Get firmware** (in
Install over USB, or the account menu under **Firmware**):

1. downloads the newest published release's `SHA256SUMS` and `SHA256SUMS.sig` from GitHub,
2. checks the signature with the project's key (`ceccd5865252c2df`, `/keys/project.pub` in
   its image) before downloading anything else,
3. downloads each chip image and checks it against `SHA256SUMS`,
4. imports them, each with its **provenance** (the signed files it came with).

If any check fails, nothing is imported. The pages call it **project release v0.0.2**.
Before each firmware update the controller rechecks the provenance, then signs the update
with your release key. Your nodes trust only your key ([Releasing](releasing.md#using-a-release)).

## Build your own firmware

Same chip images, from source, signed with **your** release key. The controller labels it
**your build v0.0.2** and checks it before every update, like a project release.

You'll need about 15 GB of disk for the pinned ESP-IDF image. Use a clean checkout: the
build container mounts all of it, with network access for ESP-IDF components, so keep your
keys and data directory out. From the checkout's top:

```sh
IDF=espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/src" -w /src "$IDF" \
  sh scripts/release/build-firmware.sh build/release/images
```

- It builds every chip image, key-neutral and generic: no board, address, keys or site
  settings. To build only some, name them after the output directory
  (`esp32p4-rev1 esp32s3-eth esp32s3-octal` are the ones the catalog's boards run).
- Several minutes per image. Output lands in `build/release/images/`.
- It fails on a compiler warning, a changed component lock, or an image with an address or
  keys built in.

Then package, sign and import with the controller image, from `controller/` (the
[Compose setup](#docker-compose)). Save `release.pub` (from **Keep the controller safe**)
as `build/release/release.pub`. The private key is read on standard input, from your data
directory:

```sh
# Package the chip images, with SHA256SUMS (every catalog board's chip image must be there)
docker compose run --rm -T -v "$PWD/../build/release:/release" --entrypoint /espdns espdns-controller \
  release build -version "$(cat ../VERSION)" -images /release/images -catalog /catalog -out /release/dist

# Sign SHA256SUMS with your release key (on standard input)
docker compose run --rm -T -v "$PWD/../build/release:/release" --entrypoint /espdns espdns-controller \
  release sign -dir /release/dist -pub /release/release.pub < "$HOME/espdns-data/keys/release.pem"

# Check the signature and every file, then import the chip images
docker compose run --rm -T -v "$PWD/../build/release:/release:ro" --entrypoint /espdns espdns-controller \
  release import -dir /release/dist -data /data
```

- `release build` writes only into a missing or empty directory: remove
  `build/release/dist` before running it again.
- `release sign` takes only the key that matches `-pub`.
- `release import` checks everything first and imports nothing if a check fails. It ends by
  naming the signer ("your release key") and the chip images imported.

For your own controller image too: `docker compose up -d --build` from `controller/`, with
`ESPDNS_IMAGE` left out of `.env`. [Releasing](releasing.md) explains the release's files.

## Keys made outside the browser

To make both key pairs offline, use the firmware's key tool in the ESP-IDF image (no
network in the container, and the tool never overwrites a key file). From the checkout's
top:

```sh
mkdir -m 700 "$HOME/espdns-keys"
IDF=espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp --network none \
  -v "$PWD/firmware/tools:/tools:ro" -v "$HOME/espdns-keys:/keys" "$IDF" \
  sh -c 'python /tools/keygen.py /keys/release.pem /keys/release.pub &&
    python /tools/keygen.py /keys/recovery.pem /keys/recovery.pub'
```

Both pairs land in `~/espdns-keys/` (private keys mode 0600), with each fingerprint
printed. Then, on the setup's Make your keys:

1. **Use a release key you have**, and run its command from `controller/`:

   ```sh
   docker compose run --rm -T -v "$HOME/espdns-keys:/mykeys:ro" --entrypoint /espdns espdns-controller \
     key import -data /data -pub /mykeys/release.pub -recovery-pub /mykeys/recovery.pub \
     < "$HOME/espdns-keys/release.pem"
   ```

   The key comes in on standard input, never as an argument, and is imported only if it
   matches `release.pub`. The recovery key is refused, on purpose. Press **Check again** to
   see the fingerprint.
2. **Use a recovery key you have**, and choose `recovery.pub` (only the public half).
3. Move `recovery.pem` offline, tick **I saved recovery.pem somewhere safe, off this
   computer**, and **Done**.
