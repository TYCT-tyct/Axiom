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
# The journey (#321) also needs the PostgreSQL and Valkey that withcluster started on the host: reach them through the
# host gateway instead of the loopback address of the host's own network, and read the Valkey password from its file.
shared=()
if [ -n "${PGTEST_CLUSTER_URL:-}" ]; then
  host=host.docker.internal
  via_gateway() { sed -e "s/127\.0\.0\.1/$host/" -e "s/localhost/$host/" <<< "$1"; }
  password_dir=$(dirname "$KVTEST_PASSWORD_FILE")
  shared=(--add-host "$host:host-gateway" --volume "$password_dir:$password_dir:ro"
    --env "PGTEST_CLUSTER_URL=$(via_gateway "$PGTEST_CLUSTER_URL")" --env "KVTEST_URL=$(via_gateway "$KVTEST_URL")"
    --env KVTEST_PASSWORD_FILE)
fi
# The shared 80-core budget / eight runners allows ten cores each; eight leaves room for host-side setup.
# A private cgroup namespace lets Node count this quota, rather than the parent's 80 cores.
docker create --name "$name" \
  --cpus 8 --cgroup-parent=axiom-ci.slice --cgroupns=private --user "$(id -u):$(id -g)" \
  --volume "$GITHUB_WORKSPACE:$GITHUB_WORKSPACE" --workdir "$GITHUB_WORKSPACE" \
  --volume "$RUNNER_TEMP:$RUNNER_TEMP" --volume "$RUNNER_TOOL_CACHE:$RUNNER_TOOL_CACHE:ro" \
  --volume "$go_cache:$go_cache" --volume "$module_cache:$module_cache" \
  --volume "$pnpm_store:$pnpm_store" \
  --volume "$web_cache:/ci/axiom" \
  --env PATH --env CI --env GATE_STRESS --env E2E_SHARD_INDEX --env E2E_SHARD_COUNT --env HOME=/tmp/axiom-web-home \
  --env GOCACHE="$go_cache" --env GOMODCACHE="$module_cache" --env AXIOM_CACHE_DIR=/ci/axiom \
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
