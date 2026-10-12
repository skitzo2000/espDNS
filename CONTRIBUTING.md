# Contributing to espDNS

Thanks for helping. espDNS is alpha (0.0.x), so bug reports from real networks are very
welcome. Everything here runs in Docker: you need Docker, Docker Compose v2.17 or newer,
and git.

## Reporting a bug

Open an issue on the repository with:

- The version. For the controller, run
  `docker compose run --rm -T --entrypoint /espdns espdns-controller version` from
  `controller/`; for a node, it's `"version"` in `http://<node>/status`.
- The board (its catalog name, such as `ws-s3-eth`) and whether it's on Ethernet or Wi-Fi.
- What you did, what you expected, and what happened.
- Logs. For the controller, `docker compose logs --tail 200` from `controller/`; for a
  node, its `http://<node>/status` and `http://<node>/health`.

**Remove your own values first.** Swap real addresses, zone names, domain names and user
names for documentation values: 192.0.2.0/24, 198.51.100.0/24, 2001:db8::/32,
example.com, home.arpa. Never paste a key, token, password or backup.

**Don't open an issue for a security problem.** Report it privately, as
[SECURITY.md](SECURITY.md) says.

## Building and testing

Run these from the top of your clone.

### The controller's tests

`go vet` and `go test` for the controller and the CLI, in the Go image CI uses:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e CGO_ENABLED=0 \
  -v "$PWD:/src:ro" -w /src/controller \
  golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514 \
  sh -c 'go vet ./... && go test ./...'
```

One test checks that [docs/releasing.md](docs/releasing.md) names the fingerprint of the
project's release key, [`keys/project.pub`](keys/project.pub). Your own keys don't affect
any test.

### The controller's image

```sh
docker build -t espdns-controller:dev \
  --build-context boards=boards --build-context version=. --build-context keys=keys \
  controller
docker run --rm --entrypoint /espdns espdns-controller:dev version
```

To run your change, rebuild and restart the controller from `controller/` with
`docker compose up -d --build`.

### The firmware

This builds a chip image the way a release does, in the pinned ESP-IDF image. It fetches
ESP-IDF components (so it needs the network) and fails on any compiler warning. Name one
chip image from `firmware/images/` (here `esp32s3-octal`), or leave it off to build them
all:

```sh
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/src" -w /src \
  espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2 \
  sh scripts/release/build-firmware.sh build/dev esp32s3-octal
```

The image lands in `build/dev/esp32s3-octal/`. Build in a clean checkout, not one that
holds your keys or your controller's data directory: the container mounts the whole
checkout. The full release build is in [docs/releasing.md](docs/releasing.md).

### What CI runs

The CI workflow ([`.github/workflows/ci.yml`](.github/workflows/ci.yml)) runs on every push
and pull request:

- the firmware's host unit tests (with AddressSanitizer and UBSan);
- the controller's `go vet` and `go test`;
- a build of every chip image, which fails on a compiler warning or a changed component
  lock;
- a check that no private address (RFC 1918, or RFC 6598's shared range) appears in a
  build file, the board catalog, an example, a test or a fixture. Use documentation
  addresses instead.

A developer guide for running the firmware's host tests and the browser tests locally will
follow.

## Pull requests

- One change per pull request, based on `main`.
- Add or change tests for what you change. CI must pass.
- If you change what a user sees or runs, update the docs in the same pull request.
  Commands in the docs are `docker`, `docker compose` or `git` commands.
- Add a line under `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md).
- Keep real addresses, zone names, domain names, host names and people's names out of
  everything: code, tests, examples, docs and commit messages. Use 192.0.2.0/24,
  198.51.100.0/24, 203.0.113.0/24, 2001:db8::/32, example.com and home.arpa. Your own
  settings belong in your data directory, which is never committed.
- Never commit a private key, token, password or backup. The only key in the repository is
  the project's release public key, `keys/project.pub`. Your own keys stay in your
  controller's data directory and offline, never in the checkout.
- Write plain English, in short sentences, in docs, comments and commit messages.
- espDNS is under the GNU Affero General Public License, version 3 only
  (AGPL-3.0-only, [LICENSE](LICENSE)). By sending a pull request you agree your
  contribution is licensed under AGPL-3.0-only too.

> **Artie's tip:** run the controller's tests before you push. CI runs them anyway, and a
> red build ruffles feathers.

## Code of conduct

Be kind and respectful. This project follows the
[Contributor Covenant, version 2.1](https://www.contributor-covenant.org/version/2/1/code_of_conduct/).
Report unacceptable behaviour to the maintainers privately, the same way as a security
problem: on [github.com/skitzo2000/espdns](https://github.com/skitzo2000/espdns), the
**Security** tab, **Report a vulnerability** (see [SECURITY.md](SECURITY.md)).
