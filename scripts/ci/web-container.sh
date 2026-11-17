#!/usr/bin/env bash
# Keep Chromium and serveweb on one private network namespace (#363), using the selected runner's paths and UID.
set -euo pipefail

name="axiom-web-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-$GITHUB_JOB-${E2E_SHARD_INDEX:-checks}"
cleanup() {
  if docker container inspect "$name" > /dev/null 2>&1; then docker rm --force "$name"; fi
}
if [ "$1" = cleanup ]; then
  cleanup
  exit 0
fi
# The workflow's always() step also covers cancellation before this trap runs.
trap 'status=$?; cleanup; exit "$status"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go_cache=$(go env GOCACHE)
module_cache=$(go env GOMODCACHE)
pnpm_store=$(pnpm --dir web store path)
web_cache="${AXIOM_CACHE_DIR:-$(dirname "$go_cache")/_webcache}"
mkdir -p "$go_cache" "$module_cache" "$web_cache"
id
stat -c '%u:%g %n' "$go_cache" "$web_cache"
# The journey (#321) also needs the PostgreSQL and Valkey that withcluster started on the host. They are reached at their
# container addresses on the Docker bridge: a published port on the bridge's gateway address is dropped by the host
# firewall of the CI machines (measured 2026-10-29: connection timed out), while traffic between containers is not.
shared=()
if [ -n "${PGTEST_CLUSTER_URL:-}" ]; then
  # Rewrites the URL's host:port, a port the host publishes, to the address and port inside the container behind it.
  inside() {
    local url=$1 inner=$2 published id ip
    published=$(sed -E 's#^[a-z]+://[^@/]*@?[^:/]*:([0-9]+).*#\1#' <<< "$url")
    id=$(docker ps --quiet --filter "publish=$published")
    ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$id")
    sed -E "s#@[^/@]*#@$ip:$inner#" <<< "$url"
  }
  password_dir=$(dirname "$KVTEST_PASSWORD_FILE")
  shared=(--volume "$password_dir:$password_dir:ro"
    --env "PGTEST_CLUSTER_URL=$(inside "$PGTEST_CLUSTER_URL" 5432)" --env "KVTEST_URL=$(inside "$KVTEST_URL" 6379)"
    --env KVTEST_PASSWORD_FILE --env KVTEST_ADMIN_PASSWORD_FILE)
fi
# The shared 80-core budget / eight runners allows ten cores each; eight leaves room for host-side setup.
# XDG_CACHE_HOME sits on the kept web cache: the boards demo and the font pipeline download pinned source fonts there
# once, instead of on every run (a cold download failed the boards e2e twice on 2026-11-17).
# A private cgroup namespace lets Node count this quota, rather than the parent's 80 cores.
# BASE (the commit a PR or train is measured against) lets the vitals serve that commit's frontend beside this one.
docker create --name "$name" \
  --cpus 8 --cgroup-parent=axiom-ci.slice --cgroupns=private --user "$(id -u):$(id -g)" \
  --volume "$GITHUB_WORKSPACE:$GITHUB_WORKSPACE" --workdir "$GITHUB_WORKSPACE" \
  --volume "$RUNNER_TEMP:$RUNNER_TEMP" --volume "$RUNNER_TOOL_CACHE:$RUNNER_TOOL_CACHE:ro" \
  --volume "$go_cache:$go_cache" --volume "$module_cache:$module_cache" \
  --volume "$pnpm_store:$pnpm_store" \
  --volume "$web_cache:/ci/axiom" \
  --env PATH --env CI --env GATE_STRESS --env E2E_SHARD_INDEX --env E2E_SHARD_COUNT --env BASE --env HOME=/tmp/axiom-web-home \
  --env GOCACHE="$go_cache" --env GOMODCACHE="$module_cache" --env AXIOM_CACHE_DIR=/ci/axiom \
  --env XDG_CACHE_HOME=/ci/axiom/xdg \
  --env npm_config_store_dir="$(dirname "$pnpm_store")" \
  "${shared[@]}" \
  "$WEB_IMAGE" bash -e -c '
    mkdir -p "$HOME"
    git config --global --add safe.directory "$PWD"
    cat /proc/self/cgroup
    cat /sys/fs/cgroup/cpu.max
    id
    cpus=$(node -p "require(\"node:os\").availableParallelism()")
    echo "Node availableParallelism=$cpus"
    test "$cpus" -le 8
    exec "$@"
  ' bash "$@"
docker start "$name"
pid=$(docker inspect --format '{{.State.Pid}}' "$name")
if [ "$pid" -eq 0 ]; then
  docker logs "$name"
  echo 'web container exited before cgroup verification' >&2
  exit 1
fi
# /proc/self/cgroup inside is namespace-relative (0::/); the host PID reveals the actual shared ancestry.
cat "/proc/$pid/cgroup"
grep -q '/axiom.slice/axiom-ci.slice/' "/proc/$pid/cgroup"
cat /sys/fs/cgroup/axiom.slice/axiom-ci.slice/{cpu.max,memory.high,memory.max}
docker logs --follow "$name"
exit "$(docker wait "$name")"
