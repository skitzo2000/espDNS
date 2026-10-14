#!/bin/sh
# Moves the firmware or the controller to its next version (docs/releasing.md):
#
#   scripts/bump-version.sh firmware patch|minor|major    the next one (0.0.1 -> 0.0.2, 0.1.0, 1.0.0)
#   scripts/bump-version.sh controller 0.0.4              that one, which must be newer
#
# It writes that part's VERSION (firmware/VERSION, which the firmware's build reads, or
# controller/VERSION, which the controller's builds link in) and, in CHANGELOG.md, makes the
# "## [Unreleased]" section the new version's ("## [firmware 0.0.4] - <date>"), dated today
# (UTC), with a new empty "## [Unreleased]" above it. A version needs its changelog: an
# Unreleased section with nothing in it is refused, as is a file without one. Nothing else is
# touched: review the diff and commit it; builds made from then on say the new version.
#
# ESPDNS_ROOT: the repository to change (default: this script's parent; the tests use another).
set -eu
root=${ESPDNS_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}
usage() { echo "usage: $0 firmware|controller patch|minor|major|MAJOR.MINOR.PATCH" >&2; exit 2; }
[ $# -eq 2 ] || usage
case $1 in firmware | controller) part=$1 ;; *) usage ;; esac
shift
file=$root/$part/VERSION
# Each number at most 9 digits, as the controller (internal/version, MaxDigits) and the
# firmware's build (firmware/version.cmake) take them: within the shell's arithmetic, and the
# longest fits the app descriptor's 32 bytes.
semver='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'

cur=$(cat "$file")
printf '%s\n' "$cur" | grep -Eq "$semver" || { echo "$file: '$cur' is not MAJOR.MINOR.PATCH" >&2; exit 1; }
IFS=. read -r ma mi pa <<END
$cur
END
case $1 in
patch) new=$ma.$mi.$((pa + 1)) ;;
minor) new=$ma.$((mi + 1)).0 ;;
major) new=$((ma + 1)).0.0 ;;
*)
	printf '%s\n' "$1" | grep -Eq "$semver" || usage
	new=$1
	IFS=. read -r na ni np <<END
$new
END
	if [ "$na" -lt "$ma" ] || { [ "$na" -eq "$ma" ] && { [ "$ni" -lt "$mi" ] || { [ "$ni" -eq "$mi" ] && [ "$np" -le "$pa" ]; }; }; }; then
		echo "$new is not newer than $cur" >&2
		exit 1
	fi
	;;
esac
printf '%s\n' "$new" | grep -Eq "$semver" || { echo "$new: a number of more than 9 digits" >&2; exit 1; }

log=$root/CHANGELOG.md
[ -f "$log" ] || { echo "no $log" >&2; exit 1; }
# The Unreleased section: from its heading to the next "## " heading. It must say something.
awk '
	/^## \[Unreleased\]$/ { found = 1; in_s = 1; next }
	in_s && /^## / { in_s = 0 }
	in_s && /[^[:space:]]/ { body = 1 }
	END { exit !found ? 3 : !body ? 4 : 0 }
' "$log" || case $? in
3) echo "$log: no \"## [Unreleased]\" section: write what changed there first" >&2; exit 1 ;;
4) echo "$log: \"## [Unreleased]\" is empty: write what changed in $new there first" >&2; exit 1 ;;
*) exit 1 ;;
esac

today=$(date -u +%Y-%m-%d)
tmp=$(mktemp "$root/.bump.XXXXXX")
trap 'rm -f "$tmp"' EXIT
awk -v p="$part" -v v="$new" -v d="$today" '
	!done && /^## \[Unreleased\]$/ { print; print ""; print "## [" p " " v "] - " d; done = 1; next }
	{ print }
' "$log" > "$tmp"
cat "$tmp" > "$log"
printf '%s\n' "$new" > "$file"
echo "$part $cur -> $new: $part/VERSION and CHANGELOG.md written; review the diff and commit it"
