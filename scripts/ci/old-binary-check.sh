#!/usr/bin/env bash
# The rolling release's other half: build the program the deployment replaces, migrate a database with this tree's
# migrations, start that program against the schema and wait for its readiness probe. The Go test only freezes the
# previous release's startup audit of the checksum table; this starts the old binary itself.
#
# The old program is the release this deployment replaces: BASE for a train (the commit the train was assembled on),
# the base named by a main landing's Train trailer, or HEAD^ for a plain main commit. Older releases are not required
# to start: a release may make a one-way change while no production node exists, and billing's
# 00040_fallbacks_no_markup.sql is one (docs/domains/billing.md「决策与理由」). Such a change makes this check fail;
# whether to accept that is a release decision.
#
# The old tree has no built frontend, and its pages cannot affect whether it reads the new schema. A minimal embedded
# frontend lets it start without the old Node toolchain and minutes of frontend build; what is checked is the database
# and the program's startup path, not the pages.
#
# usage: old-binary-check.sh --current-image IMAGE [--old-ref REF]
#   --current-image   the image built from this tree, used to run the migrations; it must already be pulled
#   --old-ref         the release to start; default BASE, the tip's Train base, or HEAD^
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
usage="usage: $0 --current-image IMAGE [--old-ref REF]"

current_image=
old_ref=
while [ $# -gt 0 ]; do
  case "$1" in
  --current-image) current_image="${2:?$usage}"; shift 2 ;;
  --old-ref) old_ref="${2:?$usage}"; shift 2 ;;
  *) echo "$usage" >&2; exit 2 ;;
  esac
done
if [ -z "$current_image" ]; then
  echo "$usage" >&2
  exit 2
fi
if [ -z "$old_ref" ]; then
  if [ -n "${BASE:-}" ]; then
    old_ref="$BASE"
  else
    # A push to main lands a whole train at once. The tip's trailer names the base the train was assembled on, so the
    # old release is that base, not the car before the tip: intermediate cars never existed on main as releases.
    old_ref="$(git -C "$repo" log -1 --format=%B HEAD | sed -n 's/^Train: #[0-9][0-9]* [0-9a-f]\{40\} on \([0-9a-f]\{40\}\)$/\1/p' | tail -1)"
    old_ref="${old_ref:-HEAD^}"
  fi
fi

# The old release must be an ancestor: a divergent commit's schema is not the one this deployment replaces.
old_commit="$(git -C "$repo" rev-parse --verify "$old_ref^{commit}")" || {
  echo "::error::cannot resolve the old release $old_ref" >&2
  exit 1
}
git -C "$repo" merge-base --is-ancestor "$old_commit" HEAD || {
  echo "::error::the old release $old_commit is not an ancestor of HEAD" >&2
  exit 1
}
[ "$old_commit" != "$(git -C "$repo" rev-parse HEAD)" ] || {
  echo "::error::the old release is HEAD; there is no older release to start" >&2
  exit 1
}

work="$(mktemp -d)"
pg_name="axiom-old-binary-$$-$RANDOM"
old_pid=
cleanup() {
  local status=$?
  if [ -n "$old_pid" ]; then
    kill "$old_pid" 2>/dev/null || true
    for _ in $(seq 1 10); do
      kill -0 "$old_pid" 2>/dev/null || break
      sleep 1
    done
    kill -9 "$old_pid" 2>/dev/null || true
    wait "$old_pid" 2>/dev/null || true
  fi
  docker rm --force "$pg_name" > /dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Build the old release without its frontend build. The directory is tracked with NOT_BUILT.txt, so the files below
# are all New needs; every app must have an entry page and the build must have a content security policy.
echo "building the old release $old_commit"
mkdir -p "$work/old"
git -C "$repo" archive --format=tar "$old_commit" | tar -x -C "$work/old"
frontend="$work/old/internal/platform/webui/build"
[ -d "$frontend" ] || {
  echo "::error::the old release has no internal/platform/webui/build" >&2
  exit 1
}
mkdir -p "$frontend/site" "$frontend/console" "$frontend/admin"
printf "default-src 'self'\n" > "$frontend/content-security-policy.txt"
for app in site console admin; do
  printf '<!doctype html><title>%s</title>\n' "$app" > "$frontend/$app/index.html"
done
rm -f "$frontend/NOT_BUILT.txt"
(
  cd "$work/old"
  CGO_ENABLED=0 timeout 300s go build -trimpath -buildvcs=false -o "$work/old-axiom" ./cmd/axiom
) || {
  echo "::error::cannot build the old release $old_commit" >&2
  exit 1
}

# A database of its own, created and migrated the way a deployment does it. The kernel picks the host port, so jobs
# sharing a runner never collide; the container is only named and labelled for teardown and for clean_stale.
postgres_image="${DEPLOY_POSTGRES_IMAGE:-postgres:18.6-alpine}"
postgres_password="$(openssl rand -hex 16)"
app_password="$(openssl rand -hex 16)"
migrator_password="$(openssl rand -hex 16)"
cgroup=()
[ -z "${AXIOM_TEST_CGROUP_PARENT:-}" ] || cgroup=(--cgroup-parent "$AXIOM_TEST_CGROUP_PARENT")
docker run --detach --name "$pg_name" \
  --label axiom.ci.old-binary-check=true \
  --restart no "${cgroup[@]}" \
  -e POSTGRES_PASSWORD="$postgres_password" -e POSTGRES_DB=axiom \
  -e AXIOM_APP_PASSWORD="$app_password" -e AXIOM_MIGRATOR_PASSWORD="$migrator_password" \
  -v "$repo/deploy/postgres/roles.sh:/docker-entrypoint-initdb.d/roles.sh:ro" \
  -p 127.0.0.1::5432 "$postgres_image" > /dev/null || {
  echo "::error::cannot start the old-binary check's database" >&2
  exit 1
}
# The entrypoint's temporary server answers while the roles are created, then shuts down and the final server starts;
# one success can land in that gap. Two probes two seconds apart are on the final server.
database_ready=false
for _ in $(seq 1 60); do
  if docker exec "$pg_name" psql -U postgres -d axiom -Atc 'select 1' > /dev/null 2>&1; then
    sleep 2
    if docker exec "$pg_name" psql -U postgres -d axiom -Atc 'select 1' > /dev/null 2>&1; then
      database_ready=true
      break
    fi
  fi
  sleep 1
done
if [ "$database_ready" != true ]; then
  echo "::error::the old-binary check's database did not become ready" >&2
  docker logs --tail 100 "$pg_name" >&2 || true
  exit 1
fi
database_port="$(docker port "$pg_name" 5432/tcp | head -1)"
database_port="${database_port##*:}"
migrator_url="postgres://axiom_migrator:$migrator_password@127.0.0.1:$database_port/axiom?sslmode=disable"
app_url="postgres://axiom_app:$app_password@127.0.0.1:$database_port/axiom?sslmode=disable"

echo "migrating a database to this tree"
docker run --rm --network host \
  -e "AXIOM_MIGRATE_DATABASE_URL=$migrator_url" \
  -e AXIOM_MIGRATE_CONNECT_TIMEOUT=10s -e AXIOM_MIGRATE_TIMEOUT=5m \
  "$current_image" migrate

# The old program starts with only the variables it knows: an AXIOM_* variable its release does not read fails startup.
mkdir -p "$work/keys" "$work/run"
umask 077
for key in master hash identity; do
  printf '1:%s\n' "$(head -c 32 /dev/urandom | base64 | tr -d '\n')" > "$work/keys/$key"
done
probe_port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
echo "starting the old release $old_commit"
(
  cd "$work/run"
  exec env -i PATH="$PATH" \
    AXIOM_DATABASE_URL="$app_url" \
    AXIOM_MASTER_KEY_FILE="$work/keys/master" \
    AXIOM_KEY_HASH_FILE="$work/keys/hash" \
    AXIOM_IDENTITY_KEY_FILE="$work/keys/identity" \
    AXIOM_PUBLIC_ADDR=127.0.0.1:0 AXIOM_ADMIN_ADDR=127.0.0.1:0 \
    AXIOM_PROBES_ADDR="127.0.0.1:$probe_port" \
    AXIOM_STARTUP_TIMEOUT=60s AXIOM_SHUTDOWN_TIMEOUT=5s \
    "$work/old-axiom" serve
) > "$work/old.log" 2>&1 &
old_pid=$!

ready=false
for _ in $(seq 1 60); do
  if ! kill -0 "$old_pid" 2>/dev/null; then
    break
  fi
  status="$(curl --silent --max-time 2 --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$probe_port/readyz" || true)"
  if [ "$status" = 200 ]; then
    ready=true
    break
  fi
  sleep 1
done
if [ "$ready" != true ]; then
  echo "::error::the old release $old_commit does not serve /readyz against the new schema" >&2
  echo "--- old release log ---" >&2
  tail -100 "$work/old.log" >&2 || true
  exit 1
fi
echo "the old release $old_commit serves /readyz against the schema migrated from this tree"
