#!/usr/bin/env bash
# Put one pinned release binary into a directory and print it: fetch-release-binary.sh <name> <version> <sha256> <owner/repo> <tag> <asset> <dest>
#
# The release tarball is kept in the runner's tool cache ($RUNNER_TOOL_CACHE/<name>/<version>/), downloaded once and
# checked against the pinned SHA-256 every time it is used, so a job re-downloads only when the copy is missing or does
# not match, and a github.com outage no longer fails every job (train run 42464569846 lost six jobs to one 504).
# A swapped or truncated tarball fails the checksum before anything is extracted. Jobs on one host share the cache;
# release-asset.sh renames a download into place only after it verifies.
set -euo pipefail

name=$1 version=$2 sha256=$3 repo=$4 tag=$5 asset=$6 dest=$7
cache="${RUNNER_TOOL_CACHE:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}}/$name/$version"
tarball="$cache/$name.tar.gz"

verified() { [ -f "$1" ] && [ "$(sha256sum "$1" | cut -d ' ' -f1)" = "$sha256" ]; }

if ! verified "$tarball"; then
  mkdir -p "$cache"
  # Tries the download URL, then the REST API (see release-asset.sh); the checksum decides either way, and the file
  # lands at $tarball only once it verified.
  bash "$(dirname "$0")/release-asset.sh" "$repo" "$tag" "$asset" "$sha256" "$tarball"
fi
tar -xzf "$tarball" -C "$dest" "$name"
