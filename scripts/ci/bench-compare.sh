#!/usr/bin/env bash
# A gate fix on the base branch must protect trains whose PRs have not merged it yet.
set -euo pipefail
base=$1
root=$(git rev-parse --show-toplevel)
tools=$(mktemp -d)
trap 'rm -rf "$tools"' EXIT
# Extract the module as a whole: the gate's dependencies must come from the same revision.
git archive "$base" tools | tar -x -C "$tools"
status=0
go run -C "$tools/tools" ./benchgate "$root" "$base" || status=$?
# A gate (or its dependencies) change must pass both the established and the proposed policy.
if ! git diff --quiet "$base...HEAD" -- tools/benchgate tools/go.mod tools/go.sum; then
  go run -C "$root/tools" ./benchgate "$root" "$base" || status=$?
fi
exit "$status"
