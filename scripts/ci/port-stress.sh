#!/usr/bin/env bash
# Reproduce Docker failing to publish a port it picked while other sockets churn the ephemeral range, and show that
# containertest.Start does not fail (TestStartUnderPortChurn). The test holds most of the range, so it runs in a
# Docker-in-Docker container whose own network namespace has a range of 1000 ports, never against the shared host's.
# Needs a Docker that may start a privileged container and the nginx:alpine image (pulled when missing).
#
#   scripts/ci/port-stress.sh [CONTAINERS]   # CONTAINERS each half starts, 20 by default
set -euo pipefail

count=${1:-20}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
name="axiom-port-stress-$$"
work=$(mktemp -d)
cleanup() {
  docker rm --force --volumes "$name" > /dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

(cd "$root" && CGO_ENABLED=0 go test -c -o "$work/containertest.test" ./internal/platform/containertest)
cgroup=()
[ -z "${AXIOM_TEST_CGROUP_PARENT:-}" ] || cgroup=(--cgroup-parent "$AXIOM_TEST_CGROUP_PARENT")
docker run --detach --privileged --name "$name" "${cgroup[@]}" --sysctl net.ipv4.ip_local_port_range="40000 40999" \
  --volume "$work:/stress:ro" docker:29-dind > /dev/null
for _ in $(seq 60); do
  if docker exec "$name" docker info > /dev/null 2>&1; then break; fi
  sleep 1
done
docker image inspect nginx:alpine > /dev/null 2>&1 || docker pull --quiet nginx:alpine > /dev/null
docker save nginx:alpine | docker exec -i "$name" docker load > /dev/null
# Inside the container testcontainers would look for the daemon at the bridge gateway; it is on this namespace's loopback.
docker exec --env AXIOM_PORT_STRESS="$count" --env TESTCONTAINERS_RYUK_DISABLED=true \
  --env TESTCONTAINERS_HOST_OVERRIDE=127.0.0.1 "$name" \
  /stress/containertest.test -test.run '^TestStartUnderPortChurn$' -test.v -test.count=1
