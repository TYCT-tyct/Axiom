#!/usr/bin/env bash
# Download one pinned GitHub release asset and check its SHA-256: scripts/ci/release-asset.sh <owner/repo> <tag> <asset> <sha256> <out>
#
# The browser URL (github.com/<repo>/releases/download/…) is tried first. When it fails, the asset is fetched through the
# REST API instead (the release's asset URL with Accept: application/octet-stream), which GitHub serves from a different
# front end: on 2026-11-08 the download URLs answered 504 for every asset for more than 20 minutes while the API path
# answered 200, and every CI job failed in its setup. GH_TOKEN, when set, authenticates the API calls so parallel jobs
# do not share the anonymous rate limit. Whatever path is used, the checksum decides: a swapped file still fails.
set -euo pipefail

[ $# -eq 5 ] || { echo "usage: scripts/ci/release-asset.sh <owner/repo> <tag> <asset> <sha256> <out>" >&2; exit 2; }
repo=$1 tag=$2 asset=$3 sha256=$4 out=$5

tmp="$(mktemp "$out.part.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

auth=()
[ -z "${GH_TOKEN:-}" ] || auth=(-H "Authorization: Bearer $GH_TOKEN")

# --retry alone does not retry a dropped connection (curl exit 56); --retry-all-errors does, and a partial file is
# rewritten, never appended to.
fetch() { curl -sSfL --retry 2 --retry-all-errors -o "$tmp" "$@"; }
matches() { [ "$(sha256sum "$tmp" | cut -d ' ' -f1)" = "$sha256" ]; }

if ! { fetch "https://github.com/$repo/releases/download/$tag/$asset" && matches; }; then
  echo "release-asset: the download URL failed for $repo $tag $asset; trying the API" >&2
  url="$(curl -sSfL --retry 3 --retry-all-errors "${auth[@]}" "https://api.github.com/repos/$repo/releases/tags/$tag" |
    python3 -c 'import json, sys
name = sys.argv[1]
print(next(a["url"] for a in json.load(sys.stdin)["assets"] if a["name"] == name))' "$asset")"
  fetch "${auth[@]}" -H "Accept: application/octet-stream" "$url"
  matches || { echo "checksum mismatch for $repo $tag $asset" >&2; exit 1; }
fi
mv -f "$tmp" "$out"
