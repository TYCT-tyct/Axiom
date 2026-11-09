#!/usr/bin/env bash
# Deploys one verified release onto this machine (docs/ops/backup-restore.md「生产入口」).
#
#   scripts/ops/deploy.sh [-f deploy/compose.yaml -f deploy/compose.edge.yaml] [--env-file FILE] [--database] <release.json>
#
# The image is read back from the Compose configuration this run will load, never from the env file alone: interpolation
# takes an exported DEPLOY_IMAGE before the --env-file, so a value left in the shell is what pull and up consume. Reading
# the resolved configuration verifies that value, and exporting it after verification pins every later command to the same
# digest. The release record (and the sbom.spdx.json beside it) is required: verification binds the digest, the signing
# identity and the SBOM to the signature, and a failed step stops the run before anything is pulled or started.
#
# The fallback file is deploy/compose.yaml, as for Compose; a deployment with the edge also passes deploy/compose.edge.yaml.
set -euo pipefail

env_file=deploy/.env
files=()
record=
database=()
while [ $# -gt 0 ]; do
  case $1 in
  -f)
    files+=("$2")
    shift 2
    ;;
  --env-file)
    env_file=$2
    shift 2
    ;;
  --database)
    database=(--database)
    shift
    ;;
  -*)
    echo "deploy: unknown option $1" >&2
    exit 2
    ;;
  *)
    record=$1
    shift
    ;;
  esac
done
[ ${#files[@]} -gt 0 ] || files=(deploy/compose.yaml)

die() {
  printf 'deploy: %s\n' "$*" >&2
  exit 1
}

[ -n "$record" ] || die "usage: scripts/ops/deploy.sh [-f FILE]... [--env-file FILE] [--database] <release.json>"
[ -r "$record" ] || die "cannot read the release record: $record"
[ -r "$env_file" ] || die "cannot read $env_file (deploy/.env.example lists what it needs)"
for file in "${files[@]}"; do
  [ -r "$file" ] || die "cannot read $file"
done
command -v jq > /dev/null || die "jq is required to read the resolved Compose configuration"
command -v cosign > /dev/null || die "cosign is not on PATH (install the release tools: scripts/ci/install-release-tools.sh)"

compose_args=()
for file in "${files[@]}"; do
  compose_args+=(-f "$file")
done
compose() { docker compose "${compose_args[@]}" --env-file "$env_file" "$@"; }
up_args=("${compose_args[@]}" --env-file "$env_file" "${database[@]}")

# Both program services must run the same reference, and it must be a digest: a tag can move between verification and
# pull, and a build would put something other than the verified bytes on this machine.
config="$(compose config --format json)"
image="$(jq -r '.services.axiom.image // empty' <<< "$config")"
migrate_image="$(jq -r '.services.migrate.image // empty' <<< "$config")"
[ -n "$image" ] || die "the Compose configuration names no axiom image"
[ "$image" = "$migrate_image" ] || die "axiom and migrate would run different images: $image, $migrate_image"
[[ "$image" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]] || die "the deployment image must be a digest, not $image"

bash "$(dirname "${BASH_SOURCE[0]}")/../ci/verify-release.sh" "$image" "$record"

# Interpolation prefers this exported value over the env file for every command below, so pull and up consume exactly the
# digest that was just verified even if the file changes during the run.
export DEPLOY_IMAGE="$image"
compose pull axiom migrate
# The new build's content-hashed pages must be in the shared volume before the new binary serves them; the old build's
# files stay, so a request that lands on an old node still finds its chunks.
compose run --rm --no-deps --user 0:0 axiom web-assets /assets
# Everything from here runs on the verified digest already pulled; up.sh never replaces the database under a running
# node, and --database is the database upgrade that drains the node first.
bash "$(dirname "${BASH_SOURCE[0]}")/up.sh" "${up_args[@]}"
