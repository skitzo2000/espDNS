# Releasing espDNS

A release is one version of the firmware and the controller together. They share the
repository's [`VERSION`](../VERSION). Every step here is a docker or git command, so Docker
and git are all you need.

## What a release holds

Push a version tag and CI builds the files
([`.github/workflows/release.yml`](../.github/workflows/release.yml)), then puts them on
GitHub as a draft release that only the repository's writers can see:

| File | What it is |
| --- | --- |
| `espdns-<version>-image-<image>.tar.gz` | Each chip image (`firmware/images/*`), exported as the controller imports it: `<image>/` with the bootloader, both partition tables, the OTA data, the app and `image.json` |
| `espdns-<version>-controller-image.txt` | The controller's image, pushed to the GitHub container registry: `ghcr.io/<owner>/espdns-controller:<version>@sha256:<digest>` |
| `SHA256SUMS` | The SHA-256 of every file above, as `sha256sum` writes it |
| `SHA256SUMS.sig` | The project's release key's signature over `SHA256SUMS`, added by a person (below) |

The images are generic and key-neutral: no board, no address, no site defaults and no keys.
Install over USB adds those (a board partition and a keys partition with your two public
keys). Every catalog board's chip image is in every release. There are no factory images
(whole-flash images): a release can't know your keys or addresses.

The signature is ECDSA P-256 over the SHA-256 of `SHA256SUMS`, 64 bytes (`r || s`), made
with the project's release key. Its public half is [`keys/project.pub`](../keys/project.pub)
(fingerprint `ceccd5865252c2df`, the first 8 bytes of its SHA-256; `/keys/project.pub` in the
controller's image).

That key signs `SHA256SUMS` and nothing else, and no node trusts it. It never goes to CI:
CI builds the files, and a person signs, with the key on standard input.

## Once: the repository's settings

There's no secret to set: each job uses the token GitHub gives the run, and asks only for
what it writes. The repository needs:

- Actions enabled (Settings, Actions, General). The default read-only permissions are fine.
- The package made public, once, after the first release. That release creates
  `espdns-controller` under the repository's owner, private by default. On the package's
  page, Package settings, set its visibility to public.

## Making a release

1. Bump the version. From the repository's top, on a branch from `main`, with the changes
   written under `## [Unreleased]` in [`CHANGELOG.md`](../CHANGELOG.md):

   ```sh
   docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp --network none -v "$PWD:/src" -w /src \
     espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2 \
     sh scripts/bump-version.sh patch      # or minor, major, or the version itself: 0.0.4
   ```

   It writes `VERSION` and turns Unreleased into the new version, dated today. Commit it and
   merge it to `main` like any change.

2. Tag it on `main`, at that merge:

   ```sh
   git tag -a v0.0.2 -m "espDNS 0.0.2" && git push origin v0.0.2
   ```

   The workflow runs on `v*` tags only. It stops at once if the tag isn't `v` plus
   `VERSION`, or `CHANGELOG.md` has no section for it. Then it builds the chip images (one
   job each; a new `firmware/images/<image>` goes into the matrix of both the CI and release
   workflows), then the controller's image (so failed
   firmware pushes nothing), then the tarballs, `SHA256SUMS` and the draft, with the
   changelog as its notes. To rerun a tag, delete the draft first: an existing release is
   never changed.

3. Build the controller's image to sign with from the tag you checked out, so the public
   key it checks with comes from your copy of the source (`keys/project.pub`):

   ```sh
   git checkout v0.0.2
   docker build -t espdns-controller:v0.0.2 \
     --build-context boards=boards --build-context version=. --build-context keys=keys controller
   ```

4. Download the draft's `SHA256SUMS` into an empty directory, `sign/`, and sign it
   with the release key on standard input, from its file or piped from your password
   manager. Never pass it as an argument or an environment variable.

   ```sh
   docker run --rm -i -u "$(id -u):$(id -g)" -v "$PWD/sign:/release" --network none \
     --entrypoint /espdns espdns-controller:v0.0.2 \
     release sign -dir /release -pub /keys/project.pub < release.pem
   ```

   It writes `sign/SHA256SUMS.sig`, and refuses any key that isn't the project's (`-pub`).

5. Add `SHA256SUMS.sig` to the draft and publish it.

6. Check it the way anyone will ([Checking a release](#checking-a-release)), from the
   published release.

## Checking a release

Download all of a release's files into one directory, e.g. `espdns-0.0.2/`, and run:

```sh
docker run --rm -v "$PWD/espdns-0.0.2:/release:ro" --network none \
  --entrypoint /espdns espdns-controller:v0.0.2 release verify -dir /release -pub /keys/project.pub
```

It checks `SHA256SUMS.sig` with the project's key, then every file against `SHA256SUMS`.
A missing or changed file, or a wrong signature, is an error. It prints the key's
fingerprint: `ceccd5865252c2df` is espDNS's release key.

Use the image you built from the source (Making a release, step 3) or the one you already
run, never the image of the release you're checking: the check is only as good as its key.

## Using a release

The images are key-neutral, so one release serves everyone's flock. Your nodes never trust
the project's key.

The controller takes a release only if `SHA256SUMS` is signed by the project's key
(`/keys/project.pub` in its image) or by your own release key, and refuses anything else.
Each imported chip image keeps its provenance in `firmware/images/<image>/provenance/` in
the data directory (the signed `SHA256SUMS`, its signature, the tarball and who signed it).
Before every firmware update (Nodes page or `espdns rollout`) the controller checks that
chain again, including that the app it sends is the one in the tarball, and only then signs
the update with your release key. If the chain doesn't check, nothing is sent.

The pages label each firmware **project release v0.0.2**, **your build v0.0.2** (a release
you signed yourself) or **your build** (no release behind it).

- The controller's image is `ghcr.io/skitzo2000/espdns-controller:<version>`, and
  `espdns-<version>-controller-image.txt` names it by digest. In `controller/`, put it in
  `.env` as `ESPDNS_IMAGE=<that line>` and start it without building, as
  [Docker Compose](developer.md#docker-compose) does:

  ```sh
  docker compose pull
  docker compose up -d --no-build
  ```

  Or build it from the source at the tag: `docker compose up -d --build` with no
  `ESPDNS_IMAGE`.

- The chip images for Install over USB and firmware updates come from **Get firmware**, in
  Install over USB (for a board whose chip image isn't there yet) or in the account panel
  (Firmware). It downloads the newest published release's `SHA256SUMS` and
  `SHA256SUMS.sig` from GitHub, checks the signature before downloading anything else, then
  downloads only the chip images needed, checks each, and imports them. In Install over USB
  it needs that board's chip image; in the account panel, what your nodes need. Other
  imported chip images stay as they are. To do the same from `controller/` for one version
  (`-image esp32s3-eth` names a chip image):

  ```sh
  docker compose run --rm -T --entrypoint /espdns espdns-controller release fetch -data /data -version v0.0.2
  ```

  For a mirror of the repository on another forge, add
  `-url https://git.example.com/<owner>/espdns`, or set
  `"release_url"` in `settings.json` for the pages. A release you downloaded yourself is
  imported like this, after the same checks as `release verify`:

  ```sh
  docker compose run --rm -T -v "$PWD/../espdns-0.0.2:/release:ro" --entrypoint /espdns \
    espdns-controller release import -dir /release -data /data
  ```

  Both take the fleet lock, so they're refused while a rollout or job runs.

- A new node is set up with Install over USB, from the imported chip image. It writes the
  board's definition, the node's address and your two public keys ([Getting
  started](getting-started.md#run-the-setup)).

### Your own builds

- For a release of your own, build the chip images from the source, package them with
  `release build`, sign `SHA256SUMS` with your own release key (`release sign -pub` your
  `release.pub`, the key from the controller's data directory on standard input), then run
  `release import` with no `-pub`. It's labelled "your build v0.0.2" and checked before
  every update like a project release. The commands are in
  [Build your own firmware](developer.md#build-your-own-firmware); the build itself is
  [Building a release by hand](#building-a-release-by-hand).
- Builds with no release behind them (in `firmware/builds/<board>/` in the data
  directory, or chip images you copied in yourself) have nothing to check, so they're sent
  as they are (labelled "your build"). They still pass every update's keys checks:
  - the image must read the keys partition (its `espdns:keys=partition-v1` marker; a build
    from before that is a downgrade to built-in keys, and refused);
  - a node in setup mode takes nothing;
  - a node on firmware from before the keys partition takes only a build whose keys seed is
    its own keys.

## Building a release by hand

These are the workflow's steps as docker commands, for testing a change to them or releasing
without CI. Build in a clean worktree of the tag (or your committed branch), never in the
checkout holding your keys, data directory or site files: the firmware container mounts the
whole checkout, with network.

```sh
git worktree add ../espdns-release v0.0.2
```

Then, from its top (`../espdns-release`), into `build/release/` (not in git):

```sh
IDF=espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2

# Every chip image, generic and key-neutral, exported (build/release/images/<image>/); a
# compiler warning, a changed component lock, or an address, site defaults or keys built
# in fails it
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/src" -w /src "$IDF" \
  sh scripts/release/build-firmware.sh build/release/images

# The controller's image (as in step 3 above), then the release's files and SHA256SUMS
docker build -t espdns-controller:build \
  --build-context boards=boards --build-context version=. --build-context keys=keys controller
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD/build/release:/release" --network none \
  --entrypoint /espdns espdns-controller:build release build -version "$(cat VERSION)" \
  -images /release/images -catalog /catalog -out /release/dist
```

`release build` refuses a chip image of another version, with an address built in, or not
key-neutral (no `espdns:keys=partition-v1` marker, or a keys seed built in); a catalog board
with no chip image; and a non-empty output directory. CI adds `-include` with the
controller image's reference after pushing it.

To try signing and checking, use a throwaway key from the firmware's own tool (never a real
key, never one the nodes trust):

```sh
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD/build/release:/out" -v "$PWD/firmware/tools:/tools:ro" \
  --network none "$IDF" python /tools/keygen.py /out/throwaway.pem /out/throwaway.pub
docker run --rm -i -u "$(id -u):$(id -g)" -v "$PWD/build/release:/release" --network none \
  --entrypoint /espdns espdns-controller:build \
  release sign -dir /release/dist -pub /release/throwaway.pub < build/release/throwaway.pem
docker run --rm -v "$PWD/build/release:/release:ro" --network none \
  --entrypoint /espdns espdns-controller:build release verify -dir /release/dist -pub /release/throwaway.pub
```

And import it into a throwaway data directory (`-pub` stands in for the project's key;
nothing reaches a node):

```sh
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD/build/release:/out" --network none "$IDF" mkdir -m 700 /out/data
docker run --rm -u "$(id -u):$(id -g)" -v "$PWD/build/release:/release" --network none \
  --entrypoint /espdns espdns-controller:build \
  release import -dir /release/dist -pub /release/throwaway.pub -data /release/data
```

The scripts the workflow runs are in [`scripts/release/`](../scripts/release/):

- `check-version.sh`: the tag against `VERSION` and `CHANGELOG.md`;
- `changelog.sh`: a version's changelog;
- `notes.sh`: the draft's notes (the changelog and how to check the files);
- `build-firmware.sh`: above.

The release code is `controller/internal/dist`; its tests and the scripts' run with the
controller's (`controller/releasescripts_test.go`).
