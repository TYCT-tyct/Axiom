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

compose_args=()
for file in "${files[@]}"; do
  compose_args+=(-f "$file")
done
compose() { docker compose "${compose_args[@]}" --env-file "$env_file" "$@"; }

# The variables the merged files interpolate, read before interpolation: a later file that replaces a value (the edge
# fixes AXIOM_TRUSTED_PROXIES) removes the earlier reference with it. $${...} is the container's shell, not Compose.
known="$(compose config --no-interpolate --format json | { grep -oE '(^|[^$])\$\{AXIOM_[A-Za-z0-9_]+' || true; } |
  sed -E 's/.*\$\{//' | sort -u)"
unread=()
while IFS= read -r line || [ -n "$line" ]; do
  [[ $line =~ ^[[:space:]]*(export[[:space:]]+)?(AXIOM_[A-Za-z0-9_]+)[[:space:]]*=[[:space:]]*(.*)$ ]] || continue
  # Empty is unset to the program; .env.example carries such lines for settings a deployment may not use.
  case ${BASH_REMATCH[3]} in '' | '""' | "''" | '#'*) continue ;; esac
  grep -qxF "${BASH_REMATCH[2]}" <<< "$known" || unread+=("${BASH_REMATCH[2]}")
done < "$env_file"
[ ${#unread[@]} -eq 0 ] || die "$env_file sets ${unread[*]}, which no service in ${files[*]} reads: fix the name, or remove it if this deployment fixes or does not use it (deploy/.env.example)"

running() { compose ps --status running --quiet "$1"; }
node="$(running axiom)"
db="$(running postgres)"
if [ -n "$node" ] && [ -n "$db" ]; then
  if ! $database; then
    want_hash="$(compose config --hash postgres | cut -d' ' -f2)"
    have_hash="$(docker inspect --format '{{index .Config.Labels "com.docker.compose.config-hash"}}' "$db")"
    image="$(compose config --format json | jq -r .services.postgres.image)"
    want_image="$(docker image inspect --format '{{.Id}}' "$image")" || die "the database image $image is not on this machine"
    have_image="$(docker inspect --format '{{.Image}}' "$db")"
    [ "$want_hash" = "$have_hash" ] && [ "$want_image" = "$have_image" ] ||
      die "the database's configuration or image differs from the running one, and a node is running: replacing the database now would cut off the node's calls. Upgrade it with --database, which stops (drains) the node first"
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
