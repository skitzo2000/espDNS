# Developer guide

Everything [Getting started](getting-started.md) leaves out: running the controller with
Docker Compose, the command line, the setup's finer points, how firmware is checked,
building your own, and working on the code: its layout, the tests and the make targets.

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

This runs the same published image from a release checkout. You need the `docker compose`
plugin v2.17 or newer (`docker compose version`), git and curl. From the repository you
only need `controller/compose.yaml` and `controller/.env.example`, so clone the release's tag
(nothing is built). The first two lines look up the newest controller release on GitHub, so
they stay right as new versions come out: `TAG` is its tag and `V` its version.

```sh
TAG=$(curl -fsSL https://api.github.com/repos/skitzo2000/espdns/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')
V=${TAG##*v}
git clone --depth 1 --branch "$TAG" https://github.com/skitzo2000/espdns.git
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

To update, set `ESPDNS_IMAGE` in `.env` to the new release's image, run
`docker compose pull` and `docker compose up -d --no-build`, then update the nodes from the
Nodes page ([Operations](operations.md)). The controller asks GitHub for the newest
firmware release when it starts, every day, and when a new node shows up. A node on an
older version is offered the update, and its firmware is downloaded when you apply it. The
firmware and the controller have their own versions, so a controller release never makes a
node out of date.

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

- **Get firmware**, for a given firmware version (or its tag, `firmware-v0.0.3`):

  ```sh
  docker compose run --rm -T --entrypoint /espdns espdns-controller release fetch -data /data -version 0.0.2
  ```

  It gets the chip images your nodes need; `-image esp32p4-rev1` gets one you name.

  For another GitHub repository (a fork's), use `-url`, or `"release_url"` in
  `settings.json` for the pages ([Settings](reference/settings.md)). Either way, only a
  release the project's key signed is taken.

- **Check the nodes:**

  ```sh
  docker compose run --rm --entrypoint /espdns espdns-controller status -all -data /data
  ```

## Setup in detail

There's one user. The dashboard is read-only until a password is set, and the first one can
only be set from a browser on the same machine. The password is stored as an argon2id hash.

A node takes a change only if it's signed with the release key whose public half is in its
keys partition. Every change counts: firmware, configs, zones, blocklists, even "flash your
LED" and "reboot".

| Key | Used for | Where it lives |
|---|---|---|
| Release key (slot 0) | Signing every change | The controller's data directory (`keys/release.pem`) |
| Recovery key (slot 1) | Rotating the nodes to new keys, if the release key is lost or leaked | Offline only. The controller keeps its public half |

The controller makes the release key (mode 0600). Your browser makes the recovery key and
sends the controller only the public half. Firmware images hold no key, so the project's
firmware works with yours. Neither button replaces an existing key.

When you install a board, the address goes into the node's board partition, never into the
saved board. Without both keys you get "set up your keys first". The installed node boots
on its address with your keys, but isn't added yet: it runs on board and firmware defaults,
forwarding to public resolvers (Quad9, `9.9.9.9` and `149.112.112.112`).

A node without keys (installed another way, or with a damaged keys record) comes up in setup
mode: it answers DNS but takes nothing over the network, and **+ Add node** says so. **Write
keys only (keeps settings)**, under Advanced in the same panel, writes just your keys
without erasing anything, and reads them back before the board restarts
([Operations](operations.md#a-node-without-keys-setup-mode)).

A board with no Ethernet uses Wi-Fi (2.4 GHz only); the browser asks for the network after
installing. Use it only where you can't run a cable.

Installed nodes show up on the Nodes page as **New device found** (via mDNS). The address is
one of:

- **A fixed address on the node**, with its **Gateway**. The gateway must be in the node's
  own network (its next hop, whatever your router routes beyond it). One that isn't is
  marked, and **Add node** waits until they fit.
- **DHCP, reserved for it in your router**: reserve its current address for its MAC (copy
  it from the panel) in your router first. It isn't offered on a network listed in
  `settings.json`'s `no_dhcp`.

Picking an existing name fills the form from its config, and your choices are saved back
to it. A name no node uses has **Delete this name**, which asks first; the file stays in
its history. **Add node** tries the node first, changing nothing. If its address isn't in a
network your zone primary allows zone transfers from, it tells you once which to allow.

Node not listed? mDNS doesn't cross subnets: type its address in **+ Add node**.

## How Get firmware checks a release

Nodes run chip images: one per chip, the same for everyone. **Get firmware** gets only
the chip images you need. In Install over USB, that's the chosen board's. In the account
menu under **Firmware**, it is what your nodes need (from each node's board). If a node
hasn't reported yet, it also gets the chip images the controller already has, since it
can't tell what that node needs. With no nodes, it says there's nothing to get yet.
Applying a firmware update gets the chip image it needs the same way. Either way, it:

1. downloads the newest published firmware release's `SHA256SUMS` and `SHA256SUMS.sig`
   from GitHub,
2. checks the signature with the project's key (`ceccd5865252c2df`, `/keys/project.pub` in
   its image) before downloading anything else,
3. downloads the chip images it needs, each checked against `SHA256SUMS`,
4. imports them, each with its provenance (the signed files it came with). The other
   chip images in the controller stay as they are.

If any check fails, nothing is imported. A chip image the release doesn't have is an error
that names the ones it has. The pages call it **project release v0.0.2**.
Before each firmware update the controller rechecks the provenance, then signs the update
with your release key. Your nodes trust only your key ([Releasing](releasing.md#using-a-release)).

## Build your own firmware

You can build the same chip images from source and sign them with your own release key. The
controller labels such a build **your build v0.0.2** and checks it before every update, like
a project release.

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
  (`esp32p4-rev1 esp32s3-eth esp32s3-octal esp32s3-quad` are the ones the catalog's boards run).
- Each image takes several minutes. Output lands in `build/release/images/`.
- It fails on a compiler warning, a changed component lock, or an image with an address or
  keys built in.

Then package, sign and import with the controller image, from `controller/` (the
[Compose setup](#docker-compose)). Save `release.pub` (from **Keep the controller safe**)
as `build/release/release.pub`. The private key is read on standard input, from your data
directory:

```sh
# Package the chip images, with SHA256SUMS (every catalog board's chip image must be there)
docker compose run --rm -T -v "$PWD/../build/release:/release" --entrypoint /espdns espdns-controller \
  release build -version "$(cat ../firmware/VERSION)" -images /release/images -catalog /catalog -out /release/dist

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

To build your own controller image too, run `docker compose up -d --build` from
`controller/`, with `ESPDNS_IMAGE` left out of `.env`. [Releasing](releasing.md) explains
the release's files.

## Versions and releases

The firmware and the controller are versioned and released separately, from the same
repository:

| Part | Version | Tag | Release |
| --- | --- | --- | --- |
| Firmware | `firmware/VERSION` | `firmware-v<version>` | The chip images, `SHA256SUMS`, `SHA256SUMS.sig` |
| Controller | `controller/VERSION` | `controller-v<version>` | The controller's image, `compose.yaml`, `SHA256SUMS`, `SHA256SUMS.sig` |

Bump the one you changed: anything under `firmware/` (and a board catalog change the chip
images need) is a firmware release; anything under `controller/` is a controller release.
Change both and release both, each on its own tag. The nodes are offered firmware releases
only, so a controller release never makes them look out of date. Releases up to 0.0.2
(`v0.0.1`, `v0.0.2`) held both parts and count as firmware releases.

The controller and the nodes check they fit together: each node's `/status` says the
protocol it speaks (`"proto": 1`), and a node on a protocol the controller doesn't speak is
offered no update, with the reason on the Nodes page. Raise it in the firmware
(`STATUS_PROTO` in `firmware/main/ota.c`) only for a change that breaks what a controller
expects, and the controller's range (`ProtoMin`, `ProtoMax` in
`controller/internal/updates`) with it.

The steps are in [Releasing](releasing.md#making-a-release).

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

## Project layout

| Path | What |
|---|---|
| `boards/` | The board catalog, one JSON file per board ([boards/README.md](../boards/README.md)) |
| `configs/` | Example node config and `settings.json` |
| `controller/cmd/espdns-controller/` | The controller: its HTTP server, API and jobs |
| `controller/cmd/espdns/` | The `espdns` CLI |
| `controller/internal/` | The packages both share: the fleet and rolling push, signed releases, keys, the board catalog and image builder, blocklists, zones, backup |
| `controller/web/` | The pages, built into the binary, and the vendored flashers in `web/vendor/` |
| `controller/e2e/` | The browser tests |
| `firmware/main/` | The node firmware. The DNS core (wire format, zones, answers, cache, forwarding, blocklists, releases, keys) is portable C, tested on the host |
| `firmware/images/` | One directory per chip image, its `sdkconfig.defaults` on top of the shared one |
| `firmware/components/w5500/` | The W5500 Ethernet driver |
| `firmware/tests/` | The firmware's host tests |
| `firmware/tools/` | Python tools: key generation, the keys partition, release manifests, the network update, image export |
| `firmware/enclosure/` | A parametric FreeCAD case for the Guition P4 board (`make -C firmware enclosure`, with `freecadcmd` on the host) |
| `keys/project.pub` | The project's release public key |
| `scripts/` | `bump-version.sh`, and the release scripts in `scripts/release/` |
| `.github/workflows/` | CI and the release workflows |

## Tests

The tests run in Docker, except the browser tests. From the checkout's top:

```sh
make -C firmware test     # the firmware's host tests, in the pinned ESP-IDF image
make -C controller test   # go vet and go test (Go on the host)
```

`make -C firmware test` needs only make and Docker. The host tests build the portable core
with AddressSanitizer and UBSan. They also check the Makefile's containers (mounts and
networks) and the network update's three steps against a fake node. For the controller's
tests without Go, use the `docker run` command in [CONTRIBUTING.md](../CONTRIBUTING.md#the-controllers-tests).

### Browser tests

`make -C controller e2e` runs `controller/e2e/` in Chromium with Playwright, pinned by
`e2e/package-lock.json`. It needs Go and Node.js on the host. The first run downloads
Playwright's Chromium to `~/.cache/ms-playwright` (no root).

The tests build a controller from the checkout and run it on `127.0.0.1:18480`
(`E2E_PORT=`), with a throwaway data directory and login. Nothing goes on the network: no
nodes, no mDNS, and Get firmware fetches from a stand-in release server on the next port
(`E2E_FORGE_PORT=`), signed by a throwaway key. They check every page's security headers
and Content Security Policy, the GUI's links and panels against fake APIs, the login and
session, and Install over USB and Write keys only against a stubbed serial port.
`E2E_SHOTS=<dir>` also saves screenshots at phone and desktop widths, light and dark.

Run them after a change to a page, the login, the security headers
(`controller/internal/websec`) or a vendored flasher.

CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs the host tests, the
controller's tests and a build of every chip image. It doesn't run the browser tests.

## Working on the firmware

`firmware/Makefile` runs everything in the pinned ESP-IDF image. Nothing is installed on
the host but make, Docker and Python 3. From `firmware/`:

```sh
make test                              # host tests
make build IMAGE=esp32s3-octal         # one chip image, in build/<image>/
make images                            # every chip image
make export-images                     # every chip image, exported into dist/images/ for the controller
make factory BOARD=ws-s3-eth ADDRESS=192.0.2.53/24 GATEWAY=192.0.2.1   # a whole-flash image for a new node
make flash IMAGE=esp32s3-eth           # flash a build over USB (PORT=, default /dev/ttyACM0)
make monitor IMAGE=esp32s3-eth         # the serial console (Ctrl+] quits); make log SECS=30 captures it
make ota IMAGE=esp32s3-eth HOST=192.0.2.53   # a signed network update, waited on until it confirms
make status HOST=192.0.2.53            # the node's /status
make boottime HOST=192.0.2.53          # how long the node is silent across a reboot
make components                        # solve and fetch the component locks again
make menuconfig IMAGE=...              # ESP-IDF's menuconfig for a build
make clean IMAGE=...
```

- `IMAGE=` is a chip image from `firmware/images/`. `BOARD=` names a catalog board instead,
  and builds its chip image with that board built in, for a node without a board partition.
- Each container gets only what its step needs. The source is mounted read-only, and builds
  and tests run with no network. Only the component fetch, `make components` and a push
  reach the network. `firmware/secrets/`, the private keys, is hidden from every container
  except the one that signs, which has no network.
- A build fails on a compiler warning. Its version is `firmware/VERSION`, never
  `git describe`.
- A build changes no component lock. A lock that no longer matches
  `main/idf_component.yml` stops the fetch; run `make components` and commit the locks.
- `make ota`, `status` and `boottime` need `HOST=`. Nothing names a default node or address.
  Put your own in `firmware/local.mk` (git-ignored; [`local.mk.example`](../firmware/local.mk.example)
  shows each), such as `HOST` and `DEFAULT_BOARD`. `controller/local.mk` is the same for
  the controller.
- `make ota` holds the fleet lock, like the controller's rollouts, and refuses to run while
  one is changing the fleet.

### A bench node

To work on a node outside your controller, give it development keys of its own:

1. `make keys` makes a key pair in `firmware/keys/` (public) and `firmware/secrets/`
   (private, mode 0600). Both are git-ignored, and an existing file is never overwritten.
2. `make factory BOARD=<board> ADDRESS=<address>/<prefix> GATEWAY=<gateway>` writes
   `dist/<board>/dns2-<board>.bin`: bootloader, partition table, the board partition with
   the node's address, the keys partition with `firmware/keys`, and the app. There's no
   default address, so a new node never gets another's by accident (`ADDRESS=dhcp` only on
   a network with a DHCP server). `FACTORY_KEYS=` leaves the keys out, and the node comes up
   in setup mode.
3. Write it at offset 0 over USB. This erases the board:

   ```sh
   IDF=espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2
   docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp --device /dev/ttyACM0 \
     --group-add "$(stat -c %g /dev/ttyACM0)" -v "$PWD/dist:/dist:ro" "$IDF" \
     python -m esptool -p /dev/ttyACM0 write_flash 0x0 /dist/<board>/dns2-<board>.bin
   ```

4. From then on, `make ota HOST=<address> IMAGE=<image>` signs each build with
   `firmware/secrets/release.pem` and pushes it. The update boots on trial and rolls back
   if it isn't healthy within 90 s.

`make flash` writes a build's bootloader, partition table and app. It leaves the board and
keys partitions as they are.

Images are key-neutral: no key is built in. `KEYS_SEED=keys` builds in `firmware/keys` as a
seed, which a node writes to an empty keys partition at its first boot. Releases never
carry a seed.

### Test builds

These build hooks test failure paths on a bench node:

- **Rollback:** `make ota IMAGE=<image> BUILD=build/rollback IDF_EXTRA=-DDNS2_TEST_FAIL_HEALTH=1 FORCE=1`
  fails its health check. The node should roll back to the firmware before it.
- **Setup mode:** `make ota IMAGE=<image> TEST_NO_KEYS=1` (built in `build/nokeys-<image>`)
  ignores the keys partition. The node comes up in setup mode, refuses releases with `403`,
  and rolls back by itself after 60 s.
- **Supervisor:** `IDF_EXTRA=-DDNS2_TEST_START_FAILS=4` starts blocking only on its fifth
  try. `IDF_EXTRA=-DDNS2_TEST_STALL_S=180` hangs the UDP receive task 180 s after each boot:
  the node reboots for it 3 times, then stays up in fault until normal firmware goes back
  on. Give each its own `BUILD=build/<name>`.

## Working on the controller

From `controller/`:

```sh
make build        # the controller and the CLI, ./espdns-controller and ./espdns (Go on the host)
make run          # the controller in the foreground, on http://127.0.0.1:8480
make firmware     # import the chip images from make export-images (firmware/dist/images/)
make test         # go vet and go test
```

`make run` uses `controller/data` (`DATA=` for another directory). Stop the container
first: they share the port. To try a change in the container instead, run
`docker compose up -d --build`.

The controller's version is `controller/VERSION`, linked in by `make build` and the
Dockerfile. A build without it, such as `go run` or the tests, says `dev`.

The image's build context is an allowlist ([`.dockerignore`](../controller/.dockerignore)):
`go.mod`, `go.sum`, `cmd/`, `internal/` and `web/`. The board catalog and
`keys/project.pub` go in as named build contexts. `container_test.go` checks the hardening
in `compose.yaml`.

## Pinned versions

Everything a build fetches from outside the repository is pinned:

- Container images by digest, with their tag: `IDF_IMAGE` in `firmware/Makefile`, the
  controller's `Dockerfile` and the CI containers. The Go version is `controller/go.mod`'s
  everywhere.
- CI actions by commit, with the tag in a comment.
- ESP-IDF components by the committed lock for each chip target,
  `firmware/dependencies.lock.<target>`.
- The vendored flashers by version and checksum (below).

`controller/pins_test.go` refuses an image or action that isn't pinned, two Go versions, or
a chip target without its lock. `controller/releasescripts_test.go` refuses an ESP-IDF
image in the docs that isn't `IDF_IMAGE`.

To bump one, on purpose:

- An image: `docker buildx imagetools inspect <name>:<tag>` gives the digest. Write
  `<name>:<tag>@sha256:<digest>`. A new Go changes `go.mod`'s go line, the Dockerfile and CI
  together.
- An action: `git ls-remote https://github.com/<owner>/<repo> refs/tags/<tag>` gives the
  commit. Write `<owner>/<repo>@<commit>  # <tag>`.
- The components: change `firmware/main/idf_component.yml` if needed, then
  `make -C firmware components`. It solves again for every chip target and writes the
  locks. Commit them.

## Updating the vendored flashers

Install over USB uses [ESP Web Tools](https://github.com/esphome/esp-web-tools), and Write
keys only uses esptool-js. Both are committed in `controller/web/vendor/` and built into the
binary, so the pages load nothing from another origin. Each directory has a `VERSION` (the
version and its npm integrity), `SHA256SUMS`, `LICENSE` and `THIRD_PARTY.md`.

Keep esptool-js at the version ESP Web Tools was built with (0.6.0 for ESP Web Tools
10.4.0). From `controller/`:

```sh
curl -s https://registry.npmjs.org/esp-web-tools/10.4.0 | jq -r .dist.integrity
make vendor-ewt V=10.4.0 SHA512=sha512-...
curl -s https://registry.npmjs.org/esptool-js/0.6.0 | jq -r .dist.integrity
make vendor-etj V=0.6.0 SHA512=sha512-...
make test
make e2e
```

Each target refuses a tarball whose sha512 differs, then replaces the files and writes
`VERSION` and `SHA256SUMS`. Check `THIRD_PARTY.md` against the new version's dependencies
by hand. If ESP Web Tools' inline styles change, the Content Security Policy's hashes must
change with them: `USBStyleHashes` and `USBStyleMapHashes` in `internal/websec`, and the
tests that list them. Then install a board with Install over USB before you commit.
