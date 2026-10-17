#!/usr/bin/env bash
# Set up, check or remove a host of self-hosted GitHub Actions runners for this repository, so a new or rebuilt CI
# host gets the environment the workflows expect instead of one made by hand: the 2026-10-17 move to a new host by
# hand failed 22 builds on a missing gcc, a pnpm installer that needs a newer Node than the system's, and Playwright
# downloads timing out. Run as root from a checkout of this repository on the host; it holds no address or secret.
#
#   RUNNER_TOKEN=<token> scripts/ci/runner-host.sh install
#       token: gh api -X POST repos/TYCT-tyct/Axiom/actions/runners/registration-token --jq .token (valid one hour)
#   scripts/ci/runner-host.sh check     # what install sets up is present and works; exits 1 naming what is not
#   scripts/ci/runner-host.sh remove    # then delete the runner registrations it prints on GitHub
#
# Settings, from the environment: RUNNERS (8), PREFIX (axiom-t: runners axiom-t1...), LABELS (axiom,axiom-temp),
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

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
go_version=$(awk '$1 == "go" { print $2 }' "$root/go.mod")
node_version=$(cat "$root/web/.node-version")
playwright_version=$(sed -n 's/.*"@playwright\/test": "\(.*\)".*/\1/p' "$root/web/package.json")
node_bin=$toolcache/node/$node_version/x64/bin

service() { echo "actions.runner.TYCT-tyct-Axiom.$prefix$1.service"; }
as_user() { sudo -u "$user" -H env PATH="$node_bin:$toolcache/go/$go_version/x64/bin:/usr/bin:/bin" "$@"; }

# download URL FILE SHA256: a swapped or truncated download fails the checksum
download() {
  curl -sSfL --retry 3 --retry-all-errors -o "$2" "$1"
  echo "$3  $2" | sha256sum -c --quiet -
}

# A tool in the layout actions/setup-go and actions/setup-node look up first (<cache>/<tool>/<version>/x64 with its
# .complete marker), so no job downloads or unpacks it; parallel jobs unpacking one tool overwrite each other.
cache_tool() { # tool version url sha256
  local dir=$toolcache/$1/$2/x64
  [ -f "$dir.complete" ] && return
  local tarball
  tarball=$(mktemp)
  download "$3" "$tarball" "$4"
  rm -rf "$dir" && mkdir -p "$dir"
  tar -xf "$tarball" -C "$dir" --strip-components=1
  rm -f "$tarball"
  touch "$dir.complete"
}

install_tools() {
  mkdir -p "$toolcache"
  local go_file=go$go_version.linux-amd64.tar.gz node_file=node-v$node_version-linux-x64.tar.xz
  cache_tool go "$go_version" "https://go.dev/dl/$go_file" \
    "$(curl -sSfL 'https://go.dev/dl/?mode=json&include=all' | jq -r --arg f "$go_file" '.[].files[] | select(.filename == $f).sha256')"
  cache_tool node "$node_version" "https://nodejs.org/dist/v$node_version/$node_file" \
    "$(curl -sSfL "https://nodejs.org/dist/v$node_version/SHASUMS256.txt" | awk -v f="$node_file" '$2 == f { print $1 }')"
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
  printf '[Slice]\nCPUWeight=%s\n' "${CPU_WEIGHT:-20}" > "$parent_dropin"
  systemctl daemon-reload
}

install_runner() { # number
  local name=$prefix$1 dir=$home/$prefix$1 tarball=$home/actions-runner-linux-x64-$runner_version.tar.gz
  if [ ! -f "$tarball" ]; then
    local url=https://github.com/actions/runner/releases/download/v$runner_version/actions-runner-linux-x64-$runner_version.tar.gz
    download "$url" "$tarball" "$(curl -sSfL "https://api.github.com/repos/actions/runner/releases/tags/v$runner_version" \
      | jq -r --arg f "$(basename "$tarball")" '.assets[] | select(.name == $f).digest | ltrimstr("sha256:")')"
  fi
  if [ ! -f "$dir/.runner" ]; then
    as_user mkdir -p "$dir"
    as_user tar -xzf "$tarball" -C "$dir"
    (cd "$dir" && as_user ./config.sh --unattended --replace --url "$repo_url" --token "$RUNNER_TOKEN" \
      --name "$name" --labels "$labels" --work _work)
    (cd "$dir" && ./svc.sh install "$user")
  fi
  # Each runner its own temporary directory and Go caches: jobs sharing one GOCACHE crashed the linker with SIGBUS
  # on an object file another job was rewriting (2026-10-17)
  as_user mkdir -p "$dir/_tmp" "$dir/_gocache" "$dir/_lintcache"
  cat > "$dir/.env" <<EOF
LANG=en_US.UTF-8
RUNNER_TOOL_CACHE=$toolcache
TMPDIR=$dir/_tmp
PATH=$node_bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
GOMAXPROCS=$procs
GOCACHE=$dir/_gocache
GOLANGCI_LINT_CACHE=$dir/_lintcache
EOF
  chown "$user:" "$dir/.env"
  mkdir -p "/etc/systemd/system/$(service "$1").d"
  printf '[Service]\nSlice=%s\n' "$slice" > "/etc/systemd/system/$(service "$1").d/slice.conf"
  systemctl daemon-reload
  systemctl restart "$(service "$1")"
}

install() {
  [ -n "${RUNNER_TOKEN:-}" ] || { echo "RUNNER_TOKEN is not set; see the top of this script" >&2; exit 1; }
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
  ok "go $go_version in the tool cache" test -f "$toolcache/go/$go_version/x64.complete"
  ok "node $node_version in the tool cache" test -f "$toolcache/node/$node_version/x64.complete"
  local race
  race=$(as_user mktemp -d)
  printf 'package main\nfunc main() {}\n' | as_user tee "$race/main.go" >/dev/null
  ok "go test -race builds (cgo, gcc)" as_user env GOCACHE="$race/cache" go build -race -o "$race/main" "$race/main.go"
  rm -rf "$race"
  local browser
  browser=$(as_user npx -y "playwright@$playwright_version" install --dry-run --only-shell chromium 2>/dev/null | awk '/Install location:/ { print $3; exit }') || true
  ok "chromium headless shell for playwright $playwright_version" test -n "$browser" -a -d "$browser"
  ok "$slice and the CPU weight on its parent" test -f "/etc/systemd/system/$slice" -a -f "$parent_dropin"
  for i in $(seq 1 "$runners"); do
    ok "$prefix$i is running" systemctl is-active "$(service "$i")"
    ok "$prefix$i runs in $slice" test "$(systemctl show "$(service "$i")" -p Slice --value)" = "$slice"
    ok "$prefix$i has GOMAXPROCS=$procs" grep -qx "GOMAXPROCS=$procs" "$home/$prefix$i/.env"
    ok "$prefix$i has its own GOCACHE" grep -qx "GOCACHE=$home/$prefix$i/_gocache" "$home/$prefix$i/.env"
  done
  return "$failed"
}

remove() {
  for i in $(seq 1 "$runners"); do
    systemctl disable --now "$(service "$i")" 2>/dev/null || true
    rm -rf "/etc/systemd/system/$(service "$i")" "/etc/systemd/system/$(service "$i").d"
  done
  rm -f "/etc/systemd/system/$slice" "$parent_dropin"
  systemctl daemon-reload
  pkill -u "$user" 2>/dev/null || true
  ! id "$user" >/dev/null 2>&1 || userdel -r "$user"
  rm -rf "$home" "$toolcache"
  echo "Removed. gcc, libc6-dev and chromium's system libraries stay installed. Delete these registrations on GitHub:"
  echo "  gh api repos/TYCT-tyct/Axiom/actions/runners --jq '.runners[] | select(.name | startswith(\"$prefix\")) | .id'"
  echo "  then gh api -X DELETE repos/TYCT-tyct/Axiom/actions/runners/<id> for each"
}

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
case "${1:-}" in
  install | check | remove) "$1" ;;
  *) sed -n '2,14p' "$0" | cut -c3- >&2; exit 2 ;;
esac
