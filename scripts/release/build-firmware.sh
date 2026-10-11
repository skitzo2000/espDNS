#!/bin/sh
# Builds a release's chip images (docs/releasing.md), generic, and exports each for the
# controller's builder:
#
#   build-firmware.sh OUT [IMAGE ...]     every firmware/images/* without IMAGEs
#   build-firmware.sh --check BUILD       only the checks below, on a build directory
#
# Runs in the pinned ESP-IDF image (firmware/Makefile IDF_IMAGE), as the release workflow
# and docs/releasing.md run it, from the top of a clean checkout (none of your keys, data
# or site files in it: the container mounts all of it, with network):
#
#   docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "$PWD:/src" -w /src \
#     espressif/idf:v5.5.5@sha256:... sh scripts/release/build-firmware.sh build/release/images
#
# Each image is built afresh in firmware/build/release/<image> (never a build made with a
# deployment's settings), with no board, no address, no site defaults and no keys built in:
# what makes a node is its board partition, its keys partition and the config adoption gives
# it. It fails on a compiler warning, on a build that changed a component lock, or on an
# image with a deployment's address or defaults compiled in, with keys built in (the
# espdns:keys-seed= marker, a KEYS_SEED= build), with a test hook, or without the keys
# partition's marker (espdns:keys=partition-v1). OUT/<image>/ is then
# firmware/tools/export_image.py's export: what `espdns release build` packs.
set -eu
[ $# -ge 1 ] || { echo "usage: $0 OUT [IMAGE ...] | $0 --check BUILD" >&2; exit 2; }
root=$(cd "$(dirname "$0")/../.." && pwd)
fw=$root/firmware

# check NAME BUILD: what a release's image must be (firmware/main/CMakeLists.txt, keys_node.h)
check() {
	# Generic: nothing of a deployment's compiled in, no seed, no test hook
	if grep -E 'DNS2_(STATIC_IP|SITE_DEFAULTS)=' "$2/compile_commands.json" >/dev/null; then
		echo "$1: built with an address or site defaults: a release's images carry none" >&2
		return 1
	fi
	if grep -E 'ESPDNS_KEYS_SEED|DNS2_TEST_|DNS2_BENCH_' "$2/compile_commands.json" >/dev/null ||
		grep -aq 'espdns:keys-seed=' "$2/dns2.bin"; then
		echo "$1: built with keys (KEYS_SEED=) or a test hook: a release's images are key-neutral" >&2
		return 1
	fi
	if ! grep -aq 'espdns:keys=partition-v1' "$2/dns2.bin"; then
		echo "$1: no espdns:keys=partition-v1 marker: not firmware that reads the keys partition" >&2
		return 1
	fi
}

if [ "$1" = --check ]; then
	[ $# -eq 2 ] || { echo "usage: $0 --check BUILD" >&2; exit 2; }
	check "$2" "$2" && echo "$2: key-neutral, generic"
	exit
fi
mkdir -p "$1"
out=$(cd "$1" && pwd)
shift
[ $# -gt 0 ] || set -- $(ls "$fw/images")
# In the ESP-IDF image the entrypoint has exported its environment; a CI job's step has not
command -v idf.py >/dev/null 2>&1 || . "$IDF_PATH/export.sh" >/dev/null
cd "$fw"
for img in "$@"; do
	defaults=images/$img/sdkconfig.defaults
	[ -f "$defaults" ] || { echo "no chip image $img (firmware/images/$img)" >&2; exit 1; }
	target=$(sed -n 's/^CONFIG_IDF_TARGET="\(.*\)"/\1/p' "$defaults")
	b=build/release/$img
	rm -rf "$b" "$b.log"
	mkdir -p build/release
	echo "== $img ($target)"
	if ! idf.py -B "$b" -DIMAGE="$img" -DBOARD= -DBOARDS_DIR="$root/boards" -DIDF_TARGET="$target" \
		-DSTATIC_IP= -DSTATIC_NETMASK= -DSTATIC_GATEWAY= -DSITE_DEFAULTS= -DKEYS_SEED= -DDNS2_TEST_NO_KEYS= \
		-DSDKCONFIG="$b/sdkconfig" \
		build >"$b.log" 2>&1; then
		tail -n 60 "$b.log" >&2
		echo "$img: the build failed (firmware/$b.log)" >&2
		exit 1
	fi
	if grep -E '(: warning:|^warning:)' "$b.log" >&2; then
		echo "$img: compiler warnings (firmware/$b.log)" >&2
		exit 1
	fi
	check "$img" "$b" || exit 1
	python tools/export_image.py "$img" --build "$b" --out "$out/$img"
done
# The committed component locks, used as they are: a build that solved again would have
# rewritten one, and built other components than the release says
if command -v git >/dev/null 2>&1 && git -C "$root" rev-parse >/dev/null 2>&1; then
	changed=$(git -C "$root" status --porcelain -- 'firmware/dependencies.lock.*')
	if [ -n "$changed" ]; then
		echo "$changed" >&2
		echo "the build changed a component lock (firmware/dependencies.lock.*)" >&2
		exit 1
	fi
fi
