#!/bin/sh
# Usage: scripts/bump.sh vX.Y.Z
#
# Sets the require of github.com/koji-1009/geta in getaotel/go.mod and
# getavet/go.mod to vX.Y.Z, the version about to be released. Commit the
# diff, merge it, then push the tag vX.Y.Z on the merged commit; the release
# workflow tags getaotel/vX.Y.Z and getavet/vX.Y.Z on the same commit.
# See RELEASING.md.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: $0 vX.Y.Z" >&2
	exit 2
fi
version=$1

# v0 and v1 only: a v2 or later release changes every module path (/v2).
if ! printf '%s\n' "$version" | grep -Eq '^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'; then
	echo "$0: $version is not a v0 or v1 semantic version (vX.Y.Z or vX.Y.Z-pre)" >&2
	exit 2
fi

root=$(cd "$(dirname "$0")/.." && pwd)
for module in getaotel getavet; do
	go mod edit -require="github.com/koji-1009/geta@$version" "$root/$module/go.mod"
	echo "$module/go.mod: github.com/koji-1009/geta $version"
done
