#!/bin/sh
# Vendors esptool-js's ES module bundle into web/vendor/esptool-js/ (served by the
# controller, embedded in the binary): `make vendor-etj V=<version> SHA512=<integrity>`.
# usb.js's Write keys only drives it directly (ESPLoader, Transport): one write of the keys
# partition, no erase, no ESP Web Tools dialog. Pin the version esp-web-tools' bundle was
# built with (its package.json's esptool-js range, resolved when it was published).
#
#   V       the npm version, e.g. 0.6.0
#   SHA512  the registry's integrity for that version, sha512-<base64>, as
#           https://registry.npmjs.org/esptool-js/<version> gives it (dist.integrity)
#
# Downloads the package's tarball from the npm registry, refuses it unless its sha512 is the
# one given, then replaces the directory's bundle.js (one file: esptool-js with pako and
# atob-lite inlined, no imports) and LICENSE, writes VERSION and regenerates SHA256SUMS
# (which the controller's tests check against the embedded bytes). THIRD_PARTY.md is kept.
set -eu

die() { echo "vendor-esptool-js: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: $0 <version> sha512-<base64>"
version=$1
want=$2
case $version in
'' | *[!0-9A-Za-z.+-]*) die "version '$version': digits, letters, '.', '+' and '-' only" ;;
esac
case $want in
sha512-?*) ;;
*) die "SHA512 must be the registry's integrity, sha512-<base64> (dist.integrity)" ;;
esac

sri() {
	if command -v openssl >/dev/null 2>&1; then
		printf 'sha512-%s' "$(openssl dgst -sha512 -binary "$1" | base64 | tr -d '\n')"
	elif command -v python3 >/dev/null 2>&1; then
		python3 -c 'import base64,hashlib,sys; print("sha512-" + base64.b64encode(hashlib.sha512(open(sys.argv[1], "rb").read()).digest()).decode(), end="")' "$1"
	else
		die "need openssl or python3 to check the download"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

here=$(cd "$(dirname "$0")/.." && pwd)
dest=$here/web/vendor/esptool-js
[ -f "$dest/THIRD_PARTY.md" ] || die "$dest/THIRD_PARTY.md is missing"

tmp=$(mktemp -d "$here/web/vendor/.update.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

url=https://registry.npmjs.org/esptool-js/-/esptool-js-$version.tgz
curl -fsSL --proto '=https' -o "$tmp/package.tgz" "$url" || die "download failed: $url"
got=$(sri "$tmp/package.tgz")
[ "$got" = "$want" ] || die "refusing $url: its sha512 is $got, not $want"

tar -xzf "$tmp/package.tgz" -C "$tmp" package/bundle.js package/LICENSE ||
	die "the tarball has no package/bundle.js or package/LICENSE"
for f in bundle.js LICENSE; do
	[ -f "$tmp/package/$f" ] && [ ! -L "$tmp/package/$f" ] || die "not a plain file: $f"
done

new=$tmp/new
mkdir "$new"
cp "$tmp"/package/bundle.js "$tmp"/package/LICENSE "$dest"/THIRD_PARTY.md "$new"/
printf 'esptool-js %s\n%s\n' "$version" "$want" >"$new"/VERSION
(cd "$new" && LC_ALL=C ls | while read -r f; do sha256 "$f"; done) >"$tmp"/SHA256SUMS
mv "$tmp"/SHA256SUMS "$new"/SHA256SUMS

mv "$dest" "$tmp"/old
mv "$new" "$dest" || { mv "$tmp"/old "$dest"; die "could not move the new files into $dest"; }
echo "esptool-js $version vendored in $dest."
echo "Check THIRD_PARTY.md against its bundled dependencies, then make test and make e2e."
