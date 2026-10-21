#!/usr/bin/env bash
# Set up, check or remove a host of self-hosted GitHub Actions runners for this repository, so a new or rebuilt CI
# host gets the environment the workflows expect instead of one made by hand: the 2026-10-17 move to a new host by
# hand failed 22 builds on a missing gcc, a pnpm installer that needs a newer Node than the system's, and Playwright
# downloads timing out. Run as root from a checkout of this repository on the host; it holds no address or secret.
#
#   GH_TOKEN=<actions-write-token> RUNNER_TOKEN=<token> scripts/ci/runner-host.sh install
#       token: gh api -X POST repos/TYCT-tyct/Axiom/actions/runners/registration-token --jq .token (valid one hour)
#   scripts/ci/runner-host.sh check     # what install sets up is present and works; exits 1 naming what is not
#   scripts/ci/runner-host.sh remove    # then delete the runner registrations it prints on GitHub
#
# Settings, from the environment: RUNNERS (8), PREFIX (axiom-t: runners axiom-t1...), LABELS (axiom,axiom-temp),
# TRAIN_RUNNERS (4: the first runners also get axiom-train; this label is managed separately from LABELS).
# install also needs GH_TOKEN with repository Actions write permission to reconcile existing runners' labels.
# PROCS (6: GOMAXPROCS of each job), CPU_QUOTA (8000%: of the whole host), CPU_WEIGHT (20: against 100 for the host's
# own services, so CI yields to them), MEMORY_HIGH (45G), MEMORY_MAX (50G). Docker must already be installed.
set -euo pipefail

repo_url=https://github.com/TYCT-tyct/Axiom
runner_version=2.338.0
user=axci
home=/home/$user
toolcache=/opt/hostedtoolcache
# systemd nests axiom-ci.slice under axiom.slice by its name; the weight goes on the parent, whose siblings are the
# host's own slices
slice=axiom-ci.slice
parent_dropin=/etc/systemd/system/axiom.slice.d/axiom-ci.conf
runners=${RUNNERS:-8} prefix=${PREFIX:-axiom-t} labels=${LABELS:-axiom,axiom-temp} procs=${PROCS:-6}
train_runners=${TRAIN_RUNNERS:-4}
cpu_weight=${CPU_WEIGHT:-20}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
go_version=$(awk '$1 == "go" { print $2 }' "$root/go.mod")
node_version=$(cat "$root/web/.node-version")
playwright_version=$(sed -n 's/.*"@playwright\/test": "\(.*\)".*/\1/p' "$root/web/package.json")
node_bin=$toolcache/node/$node_version/x64/bin

service() { echo "actions.runner.TYCT-tyct-Axiom.$prefix$1.service"; }
as_user() { sudo -u "$user" -H env PATH="$node_bin:$toolcache/go/$go_version/x64/bin:/usr/bin:/bin" "$@"; }

runner_labels() { # number: exclude axiom-train from LABELS so only TRAIN_RUNNERS decides membership
  local label result= items
  IFS=, read -ra items <<< "$labels"
  for label in "${items[@]}"; do
    if [ "$label" != axiom-train ] && [ -n "$label" ]; then result+="${result:+,}$label"; fi
  done
  if [ "$1" -le "$train_runners" ]; then result+="${result:+,}axiom-train"; fi
  printf '%s\n' "$result"
}

runner_api() { # method runner-id [curl args]: registration tokens cannot update an existing runner's labels
  curl -fsSL -X "$1" -H "Authorization: Bearer $GH_TOKEN" -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/TYCT-tyct/Axiom/actions/runners/$2/labels" "${@:3}"
}

# download URL FILE SHA256: a swapped or truncated download fails the checksum
download() {
  curl -sSfL --retry 3 --retry-all-errors -o "$2" "$1"
  echo "$3  $2" | sha256sum -c --quiet -
}

# write_changed FILE CONTENT: writes CONTENT unless FILE already holds it; succeeds only when it wrote
write_changed() { [ "$(cat "$1" 2>/dev/null)" != "$2" ] && printf '%s\n' "$2" > "$1"; }

# A tool in the layout actions/setup-go and actions/setup-node look up first (<cache>/<tool>/<version>/x64 with its
# .complete marker), so no job downloads or unpacks it; parallel jobs unpacking one tool overwrite each other.
cached() { [ -f "$toolcache/$1/$2/x64.complete" ]; } # tool version
cache_tool() { # tool version url sha256
  local dir=$toolcache/$1/$2/x64 tarball
  tarball=$(mktemp)
  download "$3" "$tarball" "$4" || { rm -f "$tarball"; return 1; }
  rm -rf "$dir" && mkdir -p "$dir"
  tar -xf "$tarball" -C "$dir" --strip-components=1
  rm -f "$tarball"
  touch "$dir.complete"
}

install_tools() {
  mkdir -p "$toolcache"
  local go_file=go$go_version.linux-amd64.tar.gz node_file=node-v$node_version-linux-x64.tar.xz
  # Checked before the call, not in it, so a cached tool costs no checksum request
  if ! cached go "$go_version"; then
    cache_tool go "$go_version" "https://go.dev/dl/$go_file" \
      "$(curl -sSfL 'https://go.dev/dl/?mode=json&include=all' | jq -r --arg f "$go_file" '.[].files[] | select(.filename == $f).sha256')"
  fi
  if ! cached node "$node_version"; then
    cache_tool node "$node_version" "https://nodejs.org/dist/v$node_version/$node_file" \
      "$(curl -sSfL "https://nodejs.org/dist/v$node_version/SHASUMS256.txt" | awk -v f="$node_file" '$2 == f { print $1 }')"
  fi
  chown -R "$user:" "$toolcache"
  # The system libraries and fonts chromium needs, then the browser itself for the runner user; the CDN can be slow
  # from some hosts, so the connection gets five minutes instead of Playwright's 30 seconds
  env PATH="$node_bin:$PATH" npx -y "playwright@$playwright_version" install-deps chromium
  as_user env PLAYWRIGHT_DOWNLOAD_CONNECTION_TIMEOUT=300000 npx -y "playwright@$playwright_version" install --only-shell chromium
}

install_slice() {
  cat > "/etc/systemd/system/$slice" <<EOF
[Unit]
Description=Axiom CI runners (CPU and memory cap)
[Slice]
CPUQuota=${CPU_QUOTA:-8000%}
MemoryHigh=${MEMORY_HIGH:-45G}
MemoryMax=${MEMORY_MAX:-50G}
EOF
  mkdir -p "$(dirname "$parent_dropin")"
  printf '[Slice]\nCPUWeight=%s\n' "$cpu_weight" > "$parent_dropin"
  systemctl daemon-reload
}

install_runner() { # number
  local name=$prefix$1 dir=$home/$prefix$1 tarball=$home/actions-runner-linux-x64-$runner_version.tar.gz
  local unit restart= desired id current
  desired=$(runner_labels "$1")
  unit=$(service "$1")
  if [ ! -f "$tarball" ]; then
    local url=https://github.com/actions/runner/releases/download/v$runner_version/actions-runner-linux-x64-$runner_version.tar.gz
    download "$url" "$tarball" "$(curl -sSfL "https://api.github.com/repos/actions/runner/releases/tags/v$runner_version" \
      | jq -r --arg f "$(basename "$tarball")" '.assets[] | select(.name == $f).digest | ltrimstr("sha256:")')"
  fi
  if [ ! -f "$dir/.runner" ]; then
    as_user mkdir -p "$dir"
    as_user tar -xzf "$tarball" -C "$dir"
    (cd "$dir" && as_user ./config.sh --unattended --replace --url "$repo_url" --token "$RUNNER_TOKEN" \
      --name "$name" --labels "$desired" --work _work)
  fi
  id=$(jq -r .agentId "$dir/.runner")
  current=$(runner_api GET "$id" | jq -r '[.labels[] | select(.type == "custom") | .name] | sort | join(",")')
  if [ "$current" != "$(jq -nr --arg labels "$desired" '$labels | split(",") | sort | join(",")')" ]; then
    runner_api PUT "$id" -H 'Content-Type: application/json' \
      --data "$(jq -nc --arg labels "$desired" '{labels: ($labels | split(",") | map(select(length > 0)))}')" >/dev/null
  fi
  # Apart from the configuration, so a service install that failed after it is retried on the next run
  [ -f "/etc/systemd/system/$unit" ] || (cd "$dir" && ./svc.sh install "$user")
  # Each runner its own temporary directory and Go caches: jobs sharing one GOCACHE crashed the linker with SIGBUS
  # on an object file another job was rewriting (2026-10-17)
  as_user mkdir -p "$dir/_tmp" "$dir/_gocache" "$dir/_lintcache"
  if write_changed "$dir/.env" "LANG=en_US.UTF-8
RUNNER_TOOL_CACHE=$toolcache
TMPDIR=$dir/_tmp
PATH=$node_bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
GOMAXPROCS=$procs
GOCACHE=$dir/_gocache
AXIOM_TEST_CGROUP_PARENT=$slice
GOLANGCI_LINT_CACHE=$dir/_lintcache"; then
    chown "$user:" "$dir/.env"
    restart=1
  fi
  mkdir -p "/etc/systemd/system/$unit.d"
  if write_changed "/etc/systemd/system/$unit.d/slice.conf" "$(printf '[Service]\nSlice=%s' "$slice")"; then restart=1; fi
  systemctl daemon-reload
  # Restarted only on a change, so running install again does not kill the jobs that are running
  if [ -n "$restart" ]; then systemctl restart "$unit"; else systemctl start "$unit"; fi
}

install() {
  [ -n "${RUNNER_TOKEN:-}" ] || { echo "RUNNER_TOKEN is not set; see the top of this script" >&2; exit 1; }
  [ -n "${GH_TOKEN:-}" ] || { echo "GH_TOKEN is not set; existing runner labels need Actions write permission" >&2; exit 1; }
  command -v docker >/dev/null || { echo "install docker first" >&2; exit 1; }
  # gcc and libc6-dev: go test -race needs cgo
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-upgrade gcc libc6-dev jq curl git sudo
  id "$user" >/dev/null 2>&1 || useradd -m -s /bin/bash "$user"
  usermod -aG docker "$user"
  cd "$home" # commands run as the user need a working directory the user can read
  install_slice
  install_tools
  for i in $(seq 1 "$runners"); do install_runner "$i"; done
  check
}

check() {
  local failed=0
  cd "$home"
  ok() { if "${@:2}" >/dev/null 2>&1; then echo "ok    $1"; else echo "FAIL  $1"; failed=1; fi; }
  ok "user $user can use docker" as_user docker info
  ok "go $go_version in the tool cache" cached go "$go_version"
  ok "node $node_version in the tool cache" cached node "$node_version"
  local race
  race=$(as_user mktemp -d)
  printf 'package main\nfunc main() {}\n' | as_user tee "$race/main.go" >/dev/null
  ok "go test -race builds (cgo, gcc)" as_user env GOCACHE="$race/cache" go build -race -o "$race/main" "$race/main.go"
  rm -rf "$race"
  local browser
  browser=$(as_user npx -y "playwright@$playwright_version" install --dry-run --only-shell chromium 2>/dev/null | awk '/Install location:/ { print $3; exit }') || true
  ok "chromium headless shell for playwright $playwright_version" test -n "$browser" -a -d "$browser"
  ok "CPUWeight=$cpu_weight on the parent of $slice" test "$(systemctl show axiom.slice -p CPUWeight --value)" = "$cpu_weight"
  for i in $(seq 1 "$runners"); do
    ok "$prefix$i is running" systemctl is-active "$(service "$i")"
    ok "$prefix$i runs in $slice" test "$(systemctl show "$(service "$i")" -p Slice --value)" = "$slice"
    ok "$prefix$i has GOMAXPROCS=$procs" grep -qx "GOMAXPROCS=$procs" "$home/$prefix$i/.env"
    ok "$prefix$i has its own GOCACHE" grep -qx "GOCACHE=$home/$prefix$i/_gocache" "$home/$prefix$i/.env"
    ok "$prefix$i has AXIOM_TEST_CGROUP_PARENT=$slice" grep -qx "AXIOM_TEST_CGROUP_PARENT=$slice" "$home/$prefix$i/.env"
  done
  return "$failed"
}

# Removes what install made and nothing else on a host the runners may share: every runner named $prefix<number>,
# found on the host rather than counted from RUNNERS; the tool versions it cached; the user once no runner of any
# name is left in its home.
remove() {
  local unit
  for unit in $(systemctl list-unit-files --no-legend "$(service '[0-9]*')" | awk '{ print $1 }'); do
    systemctl disable --now "$unit" || true
    rm -rf "/etc/systemd/system/$unit" "/etc/systemd/system/$unit.d"
  done
  rm -f "/etc/systemd/system/$slice" "$parent_dropin"
  systemctl daemon-reload
  rm -rf "$home/$prefix"[0-9]* "$toolcache/go/$go_version" "$toolcache/node/$node_version"
  rmdir "$toolcache/go" "$toolcache/node" "$toolcache" 2>/dev/null || true
  if id "$user" >/dev/null 2>&1 && ! compgen -G "$home/*/.runner" >/dev/null; then
    pkill -u "$user" || true
    userdel -r "$user"
  fi
  echo "Removed. gcc, libc6-dev and chromium's system libraries stay installed. Delete these registrations on GitHub:"
  echo "  gh api repos/TYCT-tyct/Axiom/actions/runners --jq '.runners[] | select(.name | startswith(\"$prefix\")) | .id'"
  echo "  then gh api -X DELETE repos/TYCT-tyct/Axiom/actions/runners/<id> for each"
}

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
case "${1:-}" in
  install | check | remove) "$1" ;;
  *) sed -n '2,14p' "$0" | cut -c3- >&2; exit 2 ;;
esac
