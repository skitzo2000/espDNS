#!/bin/sh
# Checks a release tag against the repository (docs/releasing.md), and prints the version:
#
#   check-version.sh firmware-v0.0.3      firmware/VERSION: the chip images' release
#   check-version.sh controller-v0.0.3    controller/VERSION: the controller's release
#
# The tag must be the part, "-v" and its VERSION, and CHANGELOG.md must have that version's
# section ("## [firmware 0.0.3] - YYYY-MM-DD", as scripts/bump-version.sh makes it) with
# something in it. The release workflows run it first and stop on any mismatch, so a tag
# pushed by mistake (another version, a version without its changelog) releases nothing.
#
# ESPDNS_ROOT: the repository to check (default: this script's); the tests use another.
set -eu
root=${ESPDNS_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}
[ $# -eq 1 ] || { echo "usage: $0 firmware-vMAJOR.MINOR.PATCH|controller-vMAJOR.MINOR.PATCH" >&2; exit 2; }
tag=$1
case $tag in
firmware-v*) part=firmware ;;
controller-v*) part=controller ;;
*) echo "tag $tag: not firmware-v<version> or controller-v<version>" >&2; exit 1 ;;
esac
v=$(cat "$root/$part/VERSION")
printf '%s\n' "$v" | grep -Eq '^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$' ||
	{ echo "$part/VERSION: '$v' is not MAJOR.MINOR.PATCH" >&2; exit 1; }
[ "$tag" = "$part-v$v" ] || { echo "tag $tag is not the $part's version: $part/VERSION says $v (tag $part-v$v)" >&2; exit 1; }
sh "$(dirname "$0")/changelog.sh" "$part $v" >/dev/null
echo "$v"
