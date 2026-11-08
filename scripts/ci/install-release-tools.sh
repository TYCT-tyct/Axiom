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

sha256() { sha256sum "$1" | cut -d ' ' -f1; }

# release-asset.sh tries the download URL, then the REST API, and leaves a file at $5 only if it matches the checksum
download() { # owner/repo tag asset sha256 destination
  bash "$(dirname "$0")/release-asset.sh" "$@"
}

if [ ! -x "$dir/cosign" ] || [ "$(sha256 "$dir/cosign")" != "$cosign_sha256" ]; then
  download sigstore/cosign "v$cosign_version" "$cosign_asset" "$cosign_sha256" "$dir/cosign"
  chmod 0755 "$dir/cosign"
fi

if [ ! -x "$dir/syft" ] || [ "$(sha256 "$dir/syft")" != "$syft_sha256" ]; then
  download anchore/syft "v$syft_version" "$syft_asset" "$syft_sha256" "$dir/syft.tar.gz"
  tar -xzf "$dir/syft.tar.gz" -C "$dir" syft
  rm -f "$dir/syft.tar.gz"
  chmod 0755 "$dir/syft"
fi

printf '%s\n' "$dir"
