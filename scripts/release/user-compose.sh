#!/bin/sh
# Prints a release's compose.yaml: controller/compose.user.yaml with the controller's image
# filled in, pinned by digest (the line of espdns-<version>-controller-image.txt):
#
#   user-compose.sh controller/compose.user.yaml ghcr.io/owner/espdns-controller:0.0.2@sha256:<64 hex>
#
# A user downloads it into an empty folder and runs docker compose up -d.
set -eu
[ $# -eq 2 ] || { echo "usage: $0 TEMPLATE IMAGE@sha256:DIGEST" >&2; exit 2; }
template=$1 ref=$2
printf '%s\n' "$ref" | grep -Eqx '[a-z0-9][a-z0-9._/-]*(:[0-9]+)?/[a-z0-9._/-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}' ||
	{ echo "$0: $ref: not an image reference pinned by digest (name:tag@sha256:...)" >&2; exit 1; }
n=$(grep -c '^    image: "@ESPDNS_IMAGE@"$' "$template" || true)
[ "$n" = 1 ] || { echo "$0: $template: want one line '    image: \"@ESPDNS_IMAGE@\"', found $n" >&2; exit 1; }
sed "s|^    image: \"@ESPDNS_IMAGE@\"\$|    image: $ref|" "$template"
