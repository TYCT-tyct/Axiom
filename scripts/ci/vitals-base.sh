#!/usr/bin/env bash
# Serves the frontend of the commit a PR or train is measured against, beside this tree's, for the overview's
# main-thread gate (web/e2e/vitals.spec.ts, docs/frontend.md「实测」). web/playwright.vitals.config.ts starts it as a second
# web server when BASE names that commit. The base is built in a worktree by its own scripts/web_cache.py, so a runner
# that built that commit before restores it instead of building it again.
#
# usage: vitals-base.sh <commit> <public address> <admin address>
set -euo pipefail

commit=$1
git worktree prune
dir=$(mktemp -d)
git worktree add --quiet --detach "$dir" "$commit"
server=
stop() {
  if [ -n "$server" ]; then kill "$server" 2> /dev/null || true; fi
  git worktree remove --force "$dir"
}
trap stop EXIT
trap 'exit 143' TERM
trap 'exit 130' INT

cd "$dir"
pnpm --dir web install --frozen-lockfile --prefer-offline >&2
# The base's own version stamp: the build cache key includes it, and the base's CI built it with this one.
version=$(just axiom-version)
python3 scripts/web_cache.py build --version "$version" -- pnpm --dir web build >&2
pnpm --dir web embed >&2
# Built, not run with `go run`, which does not pass the stop signal on to the server it starts.
go build -o serveweb ./internal/platform/webui/cmd/serveweb
./serveweb "$2" "$3" &
server=$!
wait "$server"
