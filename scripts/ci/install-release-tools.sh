#!/usr/bin/env bash
# Install the pinned tools that sign and verify a release image, and print the directory holding them.
#
# Both binaries are pinned by version and by the SHA-256 their release publishes, so a swapped or truncated download
# fails before it becomes executable. The directory defaults to RUNNER_TEMP, which each CI job owns: a runner's shared
# home would let two jobs install over one another, as the setup action notes for pnpm.
set -euo pipefail

cosign_version=3.1.4
syft_version=1.55.0
[ "$(uname -s)" = Linux ] || { echo "release tools are pinned for Linux, not $(uname -s)" >&2; exit 1; }
case "$(uname -m)" in
  x86_64 | amd64)
    cosign_asset=cosign-linux-amd64
    cosign_sha256=424e2c97d0b45fd7a6b608a87f090f35633f4c23ece5f092d48bba11aaf4bbfa
    syft_asset=syft_${syft_version}_linux_amd64.tar.gz
    syft_sha256=bb65249542db5fedeeb4d3f9b018b75e7264dbfbbb10486f044581a92f90490d
    ;;
  aarch64 | arm64)
    cosign_asset=cosign-linux-arm64
    cosign_sha256=ade83b7aac62e131cacfe96d958948b2150cc9efec46a570a2649b8e27dedad2
    syft_asset=syft_${syft_version}_linux_arm64.tar.gz
    syft_sha256=929bd163db2d7cd2255d3bcb8967208079813d7b46b0c952f7b794d8e2c7d304
    ;;
  *)
    echo "release tools are pinned for linux/amd64 and linux/arm64, not $(uname -m)" >&2
    exit 1
    ;;
esac

dir="${1:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/axiom-release-tools}"
mkdir -p "$dir"

tmp=
cleanup() { [ -z "$tmp" ] || rm -f "$tmp"; }
trap cleanup EXIT

sha256() { sha256sum "$1" | cut -d ' ' -f1; }

download() { # url sha256 destination
  tmp="$(mktemp "$3.part.XXXXXX")"
  curl -sSfL --retry 3 --retry-all-errors -o "$tmp" "$1"
  [ "$(sha256 "$tmp")" = "$2" ] || { echo "checksum mismatch for $1" >&2; exit 1; }
}

if [ ! -x "$dir/cosign" ] || [ "$(sha256 "$dir/cosign")" != "$cosign_sha256" ]; then
  download "https://github.com/sigstore/cosign/releases/download/v$cosign_version/$cosign_asset" "$cosign_sha256" "$dir/cosign"
  chmod 0755 "$tmp"
  mv -f "$tmp" "$dir/cosign"
  tmp=
fi

if [ ! -x "$dir/syft" ] || [ "$(sha256 "$dir/syft")" != "$syft_sha256" ]; then
  download "https://github.com/anchore/syft/releases/download/v$syft_version/$syft_asset" "$syft_sha256" "$dir/syft.tar.gz"
  tar -xzf "$tmp" -C "$dir" syft
  rm -f "$tmp"
  tmp=
  chmod 0755 "$dir/syft"
fi

printf '%s\n' "$dir"
