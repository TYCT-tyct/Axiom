#!/usr/bin/env bash
# Put one pinned release binary into a directory and print it: fetch-release-binary.sh <name> <version> <sha256> <url> <dest>
#
# The release tarball is kept in the runner's tool cache ($RUNNER_TOOL_CACHE/<name>/<version>/), downloaded once and
# checked against the pinned SHA-256 every time it is used, so a job re-downloads only when the copy is missing or does
# not match, and a github.com outage no longer fails every job (train run 42464569846 lost six jobs to one 504).
# A swapped or truncated tarball fails the checksum before anything is extracted. Jobs on one host share the cache:
# a download goes to a private file and is renamed into place only after it verifies.
set -euo pipefail

name=$1 version=$2 sha256=$3 url=$4 dest=$5
cache="${RUNNER_TOOL_CACHE:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}}/$name/$version"
tarball="$cache/$name.tar.gz"

verified() { [ -f "$1" ] && [ "$(sha256sum "$1" | cut -d ' ' -f1)" = "$sha256" ]; }

if ! verified "$tarball"; then
  mkdir -p "$cache"
  part="$(mktemp "$tarball.part.XXXXXX")"
  trap 'rm -f "$part"' EXIT
  # --retry alone does not retry a dropped connection (curl exit 56); --retry-all-errors does, and a partial file is
  # rewritten, never appended to.
  curl -sSfL --retry 3 --retry-all-errors -o "$part" "$url"
  verified "$part" || { echo "checksum mismatch for $url" >&2; exit 1; }
  mv -f "$part" "$tarball"
fi
tar -xzf "$tarball" -C "$dest" "$name"
