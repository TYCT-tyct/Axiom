# Sourced by the CI scripts that publish a container's port on loopback. Left to pick (-p 127.0.0.1::PORT), Docker takes
# a port from the kernel's ephemeral range, which every outbound connection on the shared CI host draws from too, and
# gave up after its few tries on consecutive ports (main run 42604281494, 2026-11-09). This picks from 20000-29999,
# below that range, where a port is held only by a process that asked for that number, and tries another number when
# the bind still loses: the same rule as internal/platform/containertest (WithHostPolicy, Start).

# run_published NAME PORT ARGS...: docker run --detach --name NAME -p 127.0.0.1:HOST:PORT ARGS..., at most eight
# tries while Docker cannot bind HOST; prints HOST. A try that lost its port leaves no container behind.
run_published() {
  local name=$1 port=$2 host attempt error
  shift 2
  for attempt in 1 2 3 4 5 6 7 8; do
    host=$((20000 + RANDOM % 10000))
    if error=$(docker run --detach --name "$name" -p "127.0.0.1:$host:$port" "$@" 2>&1 > /dev/null); then
      echo "$host"
      return 0
    fi
    if [[ $error != *"programming external connectivity"* ]]; then
      break
    fi
    docker rm --force "$name" > /dev/null 2>&1 || true
    echo "publish.sh: try $attempt of 8 lost host port $host, trying another: $error" >&2
  done
  printf '%s\n' "$error" >&2
  return 1
}
