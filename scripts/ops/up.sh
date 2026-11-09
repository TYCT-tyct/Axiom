#!/usr/bin/env bash
# Starts this machine's stack on the program image already here, and never replaces the database under a running node
# (docs/ops/backup-restore.md「生产入口」). scripts/ops/deploy.sh runs it after verifying and pulling a release;
# scripts/ci/deploy-check.sh runs it on the image it built.
#
#   scripts/ops/up.sh [-f FILE]... [--env-file FILE] [--database]
#
# An application release leaves the database container alone. Recreating it while a node serves cuts that node's calls
# off from their settlement and drops its session locks, and the node's drain (stop_grace_period) protects nothing that
# happens to the database first. So while a node runs, the database must already match this configuration (the image
# and Compose's hash of the service) and every other service is started without it. A change to the database — its
# image under deploy/postgres or its environment in the env file — is an upgrade of its own: --database stops the node
# first, which drains it, and only then builds and replaces the database. With no node running there is nothing to
# drain, so the first start and a stack that is down take that same path.
#
# Before anything else, every non-empty AXIOM_* setting in the env file must be one these Compose files read: Compose
# passes nothing it is not told to, so a setting no service names reaches no program, and the program cannot refuse a
# variable it never sees.
set -euo pipefail

env_file=deploy/.env
files=()
database=false
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
    database=true
    shift
    ;;
  *)
    echo "up: unknown argument $1" >&2
    exit 2
    ;;
  esac
done
[ ${#files[@]} -gt 0 ] || files=(deploy/compose.yaml)

die() {
  printf 'up: %s\n' "$*" >&2
  exit 1
}
. "${BASH_SOURCE[0]%/*}/env-file.sh"

compose_args=()
for file in "${files[@]}"; do
  compose_args+=(-f "$file")
done
compose() { docker compose "${compose_args[@]}" --env-file "$env_file" "$@"; }

# The variables the merged files interpolate, read before interpolation and with every profile, so that the ops jobs'
# settings count however a Compose version lists services outside the active profiles: a later file that replaces a
# value (the edge fixes AXIOM_TRUSTED_PROXIES) removes the earlier reference with it. $${...} is the container's shell.
known="$(compose --profile '*' config --no-interpolate --format json |
  { grep -oE '(^|[^$])\$\{AXIOM_[A-Za-z0-9_]+' || true; } | sed -E 's/.*\$\{//' | sort -u)"
# A setting's value as Compose's env file gives it (env-file.sh). Empty is unset to the program; .env.example carries
# such lines.
declare -A values=()
while IFS=$'\t' read -r name value; do
  values[$name]=$value
done < <(env_assignments "$env_file")
unread=()
for name in $(printf '%s\n' "${!values[@]}" | sort); do
  [ -z "${values[$name]}" ] || grep -qxF "$name" <<< "$known" || unread+=("$name")
done
[ ${#unread[@]} -eq 0 ] || die "$env_file sets ${unread[*]}, which no service in ${files[*]} reads: fix the name, or remove it if this deployment fixes or does not use it (deploy/.env.example)"

# What the database image is built from, recorded as a label on the image: the build context without roles.sh, which is
# mounted and replayed at every start rather than built in. A changed Dockerfile or entrypoint leaves the tag and the
# container on the old image until something builds it, so the image ID alone cannot show that the tree moved on.
config="$(compose config --format json)"
context="$(jq -r .services.postgres.build.context <<< "$config")"
export DEPLOY_POSTGRES_SOURCE
DEPLOY_POSTGRES_SOURCE="$(cd "$context" && find . -type f ! -path ./roles.sh -print0 | LC_ALL=C sort -z |
  xargs -0 sha256sum | sha256sum | cut -d' ' -f1)"

running() { compose ps --status running --quiet "$1"; }
node="$(running axiom)"
db="$(running postgres)"
if [ -n "$node" ] && [ -n "$db" ]; then
  if ! $database; then
    want_hash="$(compose config --hash postgres | cut -d' ' -f2)"
    have_hash="$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$db")"
    image="$(jq -r .services.postgres.image <<< "$config")"
    want_image="$(docker image inspect --format '{{.Id}}' "$image")" || die "the database image $image is not on this machine"
    have_image="$(docker inspect --format '{{.Image}}' "$db")"
    have_source="$(docker image inspect --format '{{index .Config.Labels "axiom.postgres.source"}}' "$have_image")"
    [ "$want_hash" = "$have_hash" ] && [ "$want_image" = "$have_image" ] && [ "$DEPLOY_POSTGRES_SOURCE" = "$have_source" ] ||
      die "the database's configuration, image or build inputs (deploy/postgres) differ from the running one, and a node is running: replacing the database now would cut off the node's calls. Upgrade it with --database, which stops (drains) the node first"
    compose exec -T postgres sh < deploy/postgres/roles.sh
    mapfile -t services < <(compose config --services | grep -vx postgres)
    compose up --detach --no-build --wait --no-deps "${services[@]}"
    exit 0
  fi
  compose stop axiom
fi

# The database image is built from this tree (pgBackRest on the pinned PostgreSQL tag), not consumed as a digest like
# the program, and only here: even a rebuild from a warm cache names a new image, which Compose would replace the
# database for (#605). The healthcheck is the startup condition, including crash recovery; the bound is for a
# crash-looping container, whose restart resets its start period so it may never report unhealthy. Five minutes covers
# the 2-minute start period, the three 10-second failure probes and a slow Docker daemon.
compose build postgres
compose up --detach --no-build --wait --wait-timeout 300 postgres
compose exec -T postgres sh < deploy/postgres/roles.sh
compose up --detach --no-build --wait
