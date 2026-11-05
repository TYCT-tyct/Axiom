# Developer commands; CI runs the same recipes. Prerequisites are listed in README.md.

set windows-shell := ["bash", "-c"]

python := if os_family() == "windows" { "python" } else { "python3" }
tool := "go tool -modfile=tools/go.mod"
web := "pnpm --dir web"

# Run every check CI runs, except one run of each benchmark and the deployment check: `check-all` adds the benchmarks.
# This is the local pre-push gate, so it keeps `secrets` and `docs`, which a PR build leaves to check-docs.yml and check-secrets.yml (see
# `check-ci`)
check: docs check-static test quality-ratchet-coverage secrets web

# The checks that need no run of a test suite: CI's static job adds `script-tests` on a PR, and `docs` (which includes
# `script-tests`) plus `secrets` on a train or main. The mode selects the quality ratchet's behaviour: `pr` (the default)
# requires every improvement to be recorded in the baseline, `train` passes --allow-unrecorded-gains for a train or main
# build and reports what to record (see the quality-ratchet recipe).
check-static mode="pr": modules adapters-removable artifacts scope api doc-names tidy generated (quality-ratchet mode) lint deadcode vuln

# Regenerate code from its contracts (api/*.yaml) and its queries (sqlc.yaml)
generate: web-install
    go generate ./...
    {{ tool }} sqlc generate
    {{ web }} --filter @axiom/api generate

generate_command := quote(just_executable()) + " --justfile " + quote(justfile()) + " generate"

# Generated code is what the generators write now, and only that. Every committed *.gen.go and *.gen.ts is deleted and
# `generator` (just generate) writes them again: what changed, was added or was not written back fails, so a stale
# file does, and so does a hand-written file named like generated code, which lint and the frontend checks skip by
# its name. A generated file with uncommitted changes would be lost by the deleting, so it fails first, untouched.
# Then, once the generators have run, a Go file with the header `// Code generated ... DO NOT EDIT.` before its first
# code, which makes golangci-lint (exclusions.generated in .golangci.yml) skip every linter on it, must be named
# *.gen.go, so a generator's output under another name fails too (sqlc.yaml's output_files_suffix). The header match
# is wider than that rule (indented, after a block comment, trailing white space, so a CRLF header too), so it never
# misses a header the linter reads. A generator that fails leaves the files deleted: git restore brings them back.
generated generator=generate_command:
    pending="$(git status --porcelain --untracked-files=all -- '*.gen.go' '*.gen.ts')" || exit 1; [ -z "$pending" ] || { echo "generated files with uncommitted changes would be deleted and written again: commit or discard them, then run this again:"; echo "$pending"; exit 1; }
    list="$(mktemp)" && git ls-files -z -- '*.gen.go' '*.gen.ts' > "$list" && xargs -0 rm -f -- < "$list"; status=$?; rm -f "$list"; exit "$status"
    {{ generator }}
    changed="$(git status --porcelain --untracked-files=all -- '*.gen.go' '*.gen.ts')" || exit 1; [ -z "$changed" ] || { echo "generated code is stale or not committed, or a hand-written file is named like generated code: run just generate and commit the result"; echo "$changed"; exit 1; }
    found="$(git grep --untracked -l -E '^(.*\*/)?[[:space:]]*// Code generated .* DO NOT EDIT\.[[:space:]]*$' -- '*.go' ':!*.gen.go')"; [ $? -le 1 ] || exit 1; [ -z "$found" ] || { echo "only generated files carry the generated-code header, and they are named *.gen.go (output_files_suffix in sqlc.yaml):"; echo "$found"; exit 1; }

# Format Go code in place
fmt:
    {{ tool }} golangci-lint fmt

# go.mod and go.sum list exactly what the code uses
tidy:
    go mod tidy -diff
    cd tools && go mod tidy -diff

lint:
    {{ tool }} golangci-lint run
    cd tools && go tool golangci-lint run ./...

# The quality ratchet: nolint per linter, functions over gocognit 15, test functions over 15 and files over 500 lines
# may only fall, and no function or file may newly cross the hard ceiling 30, test functions included, or 800. Test
# functions have their own ceiling list, so a test exception cannot excuse production code. .github/quality-baseline.json
# holds the counts and the named exceptions; the script says which number to record when one improves (`--write-baseline`
# lowers the counts and raises coverage, and refuses a worsening).
# The default `pr` mode requires every improvement past the tolerance to be recorded in the same pull request. A train or
# main build passes `train`, which adds --allow-unrecorded-gains: several PRs can improve the same number by less than the
# tolerance each and cross it together, so the build reports the improvement instead of failing and ejecting one of them.
# Every worsening still fails. The next PR's static check records an unrecorded static gain; main's run is the report for
# coverage, whose suite does not run on a PR. The workflow chooses the mode explicitly; the script does not guess it.
# With BASE set (PRs and trains), it also reads the baseline committed at the base, so code and baseline cannot grow
# together; a base without the baseline is its first introduction. A static-only write keeps the recorded coverage.
# The static half of the quality ratchet: nolint, complexity and file length may only fall.
quality-ratchet mode="pr":
    {{ python }} scripts/ci/quality_ratchet.py {{ if mode == "train" { "--allow-unrecorded-gains" } else { "" } }}

# After `just test` wrote coverage.out: a package may not fall below its baseline by more than the tolerance, a new
# package must start at or above the floor, and an improvement past the tolerance is recorded by the PR that made it, or
# reported by a train or main build (`train`). Generated code and the withcluster test command are excluded; tools/ is
# another module and is not measured here. It reads the profile the race suite already wrote, so the suite runs once.
# The coverage half of the quality ratchet: per-package statement coverage may only fall.
quality-ratchet-coverage mode="pr":
    {{ python }} scripts/ci/quality_ratchet.py --coverage coverage.out {{ if mode == "train" { "--allow-unrecorded-gains" } else { "" } }}

# Module imports follow the dependency table in docs/architecture.md
modules:
    go run -C tools ./checkmodules ..

# Every adapter can be removed: for each one in the registry, deleting its package and its line there leaves the rest of
# the program building and passing go vet (internal/suppliers/internal/removable)
adapters-removable:
    go run ./internal/suppliers/internal/removable .

# Every contract in api/ is valid OpenAPI 3.0.3 and follows the conventions of internal/platform/README.md「接口约定」:
# security and permission declared, the error body, cursors, idempotency keys, and what two contracts share defined alike
api:
    go run -C tools ./checkapi ..

# Rolling release runs old and new nodes side by side: a field a schema in base did not require stays optional, or a
# new page reading an old node, or an old page writing to a new one, breaks (#400, #401). Needs the base's contracts.
api-compat base:
    #!/usr/bin/env bash
    set -euo pipefail
    old="$(mktemp -d)"; trap 'rm -rf "$old"' EXIT
    git ls-tree --name-only {{ quote(base) }} api/ | grep '\.yaml$' | while read -r f; do git show {{ quote(base) }}:"$f" > "$old/${f#api/}"; done
    go run -C tools ./checkapi .. "$old"

# Docs name only tests and Go declarations that exist (tools/checkdocnames)
doc-names:
    go run -C tools ./checkdocnames ..

# No build output, binary or file over 1 MB is committed: the files the branch adds or changes, and the untracked ones a
# `git add` would take, are checked by size, by suffix and for a NUL byte (every file on main, and without origin/main);
# scripts/check_artifacts.py holds the rules and the allowlist of what the tree keeps on purpose
artifacts:
    {{ python }} {{ quote(justfile_directory() / "scripts/check_artifacts.py") }}

# Only internal/platform/pg sets the tenant scope or switches the role, in Go and SQL, tests and migrations included;
# scripts/check_scope.py holds the rule and its reasons
scope:
    {{ python }} {{ quote(justfile_directory() / "scripts/check_scope.py") }}

# Every function is reachable from a program or a test: code nothing calls and nothing tests fails. Modules are built
# bottom-up, so a module's tests call it before the modules above it do; the functions only tests reach are listed,
# without failing, for the milestone's quality audit, by which time the program calls every module. Test-only
# packages such as pgtest, egresstest, alloctest, telemetrytest and suppliers' adaptertest, like net/http/httptest,
# are left out of that list; egress.NewForTesting lives in egress itself, so it stays listed.
deadcode:
    out="$({{ tool }} deadcode -test ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }
    out="$({{ tool }} deadcode ./...)" || exit 1; listed="$(printf '%s\n' "$out" | grep -v -e '^$' -e '^internal[/\\]platform[/\\]pgtest[/\\]' -e '^internal[/\\]platform[/\\]egresstest[/\\]' -e '^internal[/\\]platform[/\\]alloctest[/\\]' -e '^internal[/\\]platform[/\\]telemetry[/\\]telemetrytest[/\\]' -e '^internal[/\\]suppliers[/\\]internal[/\\]adaptertest[/\\]')"; [ $? -le 1 ] || exit 1; [ -z "$listed" ] || printf '%s\n' "$listed" | sed -E 's/^(.+\.go):([0-9]+):[0-9]+: unreachable func: (.+)$/only tests reach: \3 (\1 line \2)/'
    cd tools && out="$(go tool deadcode ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }

# Tests with only the flags go test can cache, so a result is reused until the package or an input it read changes. The
# packages share one PostgreSQL cluster, which internal/platform/pgtest/cmd/withcluster starts and removes; `go test
# ./pkg` alone starts its own. The test build still compiles every benchmark; `bench-once` runs them under the race
# detector, and bench-compare compares their allocations without it. At most four packages at once: eight, the default
# on the 8-core host, raced with the shared cluster and starved the timing-sensitive database tests; four is the limit
# the server proof used. -coverprofile adds coverage instrumentation to that same run and writes coverage.out for
# quality-ratchet-coverage; the suite is not run twice. On the CI host the covered runs took 9-12m against 13m
# uncovered, dominated by host load; reading the profile costs under a second.
test: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=4 -coverprofile=coverage.out ./...
    go test -C tools -race ./...

# Focused remote verification for callback transactions and their mutation proofs (#309).
payments-test:
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=2 -timeout 5m ./internal/payments
    go test -race -p=2 ./internal/platform/clientip
    go test -race -p=2 -run '^TestPaymentCallbacksBypassOnlyConsoleAuthentication$' ./cmd/axiom
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=2 -timeout 10m ./cmd/axiom -run 'TestTopUp|TestPaymentMethods|TestReplay|TestOrder|TestIPLimitsOnOrders|TestReturn|TestARefusal|TestRefusal|TestContractRules'
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=2 -timeout 5m ./internal/accounts -run 'TestOrigin|TestAuthorizedScope'

# Every benchmark compiles and runs once, with the race detector. CI runs this on main pushes; a train runs bench-compare
# instead, which compares allocations without -race. A pull request runs neither.
bench-once: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=4 -run '^$' -bench . -benchtime 1x ./...

# The load test of docs/performance.md「怎么验证」: 5000 streams, a burst and a mass cancel, megabyte requests, a hot account,
# a cold-key flood and two database outages, on the shared PostgreSQL cluster, about 12 minutes at scale 1 and heavy on the
# machine (without the race detector, which it would slow beyond meaning). It is not part of `check`; run it on the CI
# server, never beside other work: bash ~/axiom-ops/remote-check.sh <worktree> load. A scale below 1 shrinks the counts and
# the times to try it on a small machine, and `only` (a pattern such as ^1_) runs the scenarios whose names match. Prints the results table last.
load scale="1" only="": web-embed
    AXIOM_LOAD_SCALE={{ scale }} go run ./internal/platform/pgtest/cmd/withcluster go test -tags load -count=1 -timeout 35m -run '^TestLoad$/{{ only }}' -v ./cmd/axiom

# Every check, including one run of each benchmark under the race detector
check-all: check bench-once

# What a pull request's build runs: `check` without `secrets` and the workflow half of `docs`, which check-docs.yml's
# required `secrets` and `docs` jobs already run on it, so they would run twice per build. `script-tests` is the half of
# `docs` check-docs.yml does not run. A train or main runs `docs` and `secrets` too (CI's static job).
check-ci: script-tests check-static test quality-ratchet-coverage web

# The checks a Markdown-only pull request runs instead of the full build: the docs tests (including the pull request
# template), module boundaries, the pg module table, and the size and binary rule, which a document must pass too.
md-checks: docs doc-names modules artifacts
    go test ./internal/platform/pg -run '^TestModuleOrderFollowsArchitecture$'

# Record a protocol sample: send the request file (a path from the repository root) to a live upstream and save the
# stream as internal/protocol/testdata/<format>/<scenario>/, scrubbed and checked. Base URLs and keys come from the
# environment or .env, as .env.example lists; internal/protocol/README.md「录制样本」 has the rules.
record format scenario request:
    go run ./internal/protocol/internal/record internal/protocol/testdata {{ quote(format) }} {{ quote(scenario) }} {{ quote(request) }}

# Run the benchmarks at a base commit and here: more allocations fail, timings are only reported. Jobs on a shared
# machine can run it at the same time: every benchmark has a fixed iteration count with the collector off on one
# processor per binary, so another job's load cannot move its allocation count (tools/benchgate, docs/performance.md).
bench-compare base:
    go run -C tools ./benchgate .. {{ quote(base) }}

# Send the same requests to Axiom and to the official new-api image, both served by one fake upstream that replays the
# protocol samples, and fail on any difference docs/domains/gateway.md「与 new-api 的有意差异」 does not explain. It pulls
# an image and starts a container, so it is not part of `check`; run it on the CI server: remote-check.sh <worktree> diff
diff: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster go test -tags diff -count=1 -timeout 15m -v -run '^(TestAxiomMatchesNewAPIOnTheWire|TestDiffHarness.*)$' ./cmd/axiom

# Show what writing deploy/catalog/dialects.json into the models of the database AXIOM_DATABASE_URL names would change, and
# change nothing (docs/ops/release.md). Run it before catalog-dialect-apply.
catalog-dialect-plan:
    go run ./cmd/axiom catalog apply-dialect deploy/catalog/dialects.json

# Write deploy/catalog/dialects.json into those models through the catalog's audited spec update; a second run changes nothing
catalog-dialect-apply:
    go run ./cmd/axiom catalog apply-dialect --apply deploy/catalog/dialects.json

# The catalog's acceptance check against AXIOM_DATABASE_URL, read-only: every model has what a release needs of it
# (internal/catalog/README.md「第一版验收的检查」); fails when it finds any problem
catalog-check:
    go run ./cmd/axiom catalog check

compose := "docker compose -f deploy/compose.yaml"

# Build the program for Linux with the frontend embedded and start it with its database on this machine, writing random local passwords to
# deploy/.env, readable only by its owner, the first time; returns once it reports ready, or, when a step outlasts
# its limit, fails with the containers' state, health-check output and logs. New passwords would not open a database
# created with the old ones, so a missing deploy/.env next to the project's existing database volume stops it; only the
# migration administrator's password is added to a deploy/.env written before that role existed, and postgres/roles.sh,
# run before migrating, creates the role in such a database.
up: web-embed
    umask 077; [ -f deploy/.env ] || { volumes="$(docker volume ls --quiet --filter name=^${COMPOSE_PROJECT_NAME:-axiom}_postgres$)" || exit 1; [ -z "$volumes" ] || { echo "deploy/.env is missing but the project's database volume exists: restore the file, or delete the database with: just down -v"; exit 1; }; printf 'AXIOM_POSTGRES_PASSWORD=%s\nAXIOM_APP_PASSWORD=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > deploy/.env; }
    grep -q '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env || { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_MIGRATOR_PASSWORD=%s\n' "$(openssl rand -hex 24)"; } >> deploy/.env; chmod 600 deploy/.env
    grep -q '^AXIOM_RETENTION_PASSWORD=' deploy/.env || { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_RETENTION_PASSWORD=%s\n' "$(openssl rand -hex 24)"; } >> deploy/.env
    grep -q '^AXIOM_RETENTION_DATABASE_URL=' deploy/.env || { password="$(sed -n 's/^AXIOM_RETENTION_PASSWORD=//p' deploy/.env)"; [ -z "$password" ] || { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_RETENTION_DATABASE_URL=postgres://axiom_retention:%s@postgres/axiom?sslmode=disable&pool_max_conns=2\n' "$password"; } >> deploy/.env; }; chmod 600 deploy/.env
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
    {{ compose }} up --detach --build --wait --wait-timeout 60 postgres && {{ compose }} exec -T postgres sh < deploy/postgres/roles.sh && {{ compose }} up --detach --build --wait --wait-timeout 60 || { {{ compose }} ps --all; docker inspect --format '{{{{.Name}}: {{{{json .State.Health}}' $({{ compose }} ps --all --quiet); {{ compose }} logs; exit 1; }

# Stop everything; `just down -v` also deletes the local database and the master key made for it. By project name — the
# one COMPOSE_PROJECT_NAME names for a CI job, otherwise axiom — so it works without deploy/.env.
down *flags:
    docker compose --project-name "${COMPOSE_PROJECT_NAME:-axiom}" down {{ flags }}

# Rehearse the backup and the restore end to end: its own compose project, a local S3 server instead of the bucket, the
# database and the key ring wiped and restored from a set, and what came back compared with what was there
# (docs/ops/backup-restore.md「恢复演练」). It removes everything it creates, volumes included, and never touches a
# deployment or a real bucket: about ten minutes, one run at a time on a machine.
backup-rehearsal:
    bash scripts/ops/rehearse.sh

# Merged migrations never change: against the base commit, files under migrations/ may only be added (a rename
# counts as a deletion). A git failure, such as a missing base or a shallow clone without the merge base, fails the
# check: an empty list from a failed diff would pass it.
migrations base:
    changed="$(git diff --name-only --no-renames --diff-filter=a {{ quote(base) }}...HEAD -- '*/migrations/*')" || exit 1; [ -z "$changed" ] || { echo "merged migrations were changed or removed:"; echo "$changed"; exit 1; }
    {{ python }} {{ quote(justfile_directory() / "scripts/migration_order.py") }} {{ quote(base) }}

vuln:
    {{ tool }} govulncheck ./...
    cd tools && go tool govulncheck ./...

# No secret in this branch's commits, checked before they are pushed: CI scans every commit of a pull request, and a
# secret, or a test value that looks like one, stays in the history until the branch is replaced. The version
# matches GITLEAKS_VERSION in .github/workflows/check-secrets.yml. Without origin/main, gitleaks would scan nothing and
# still pass, so a missing base fails instead.
secrets:
    git rev-parse --verify --quiet 'refs/remotes/origin/main^{commit}' > /dev/null || { echo "origin/main is missing, so the branch cannot be scanned: run git fetch origin main"; exit 1; }
    go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner --log-opts=origin/main..HEAD .

docs: script-tests
    {{ python }} -m unittest discover -s scripts/workflow
    {{ python }} scripts/workflow/check_docs.py

# This repository's own script tests (scripts/test_*.py)
script-tests:
    {{ python }} -m unittest discover -s scripts

# Install the frontend's dependencies exactly as web/pnpm-lock.yaml pins them; dependencies' install scripts never run
web-install:
    {{ web }} install --frozen-lockfile

# Serve the website on :5173 with the console under :5173/console/ (also on :5174/console/), and the admin on :5175,
# reloading on every change; the console's API calls go to the program on :8080, which `just up` starts
web-dev: web-install
    {{ web }} dev

# Print the version of the program built from this tree, exactly as `axiom version` prints it
[private]
axiom-version:
    dir="$(mktemp -d)"; bin="$dir/axiom$(go env GOEXE)"; go build -o "$bin" ./cmd/axiom && "$bin" version; status=$?; rm -rf "$dir"; exit "$status"

axiom_version := quote(just_executable()) + " --justfile " + quote(justfile()) + " axiom-version"

# Build the website, console and admin into each app's dist/. The console is stamped with the version of the program
# built from this tree, to notice when the program answering is another one. The output is reused when the hash of its
# inputs (web/, the version stamp, the toolchain and VITE_/AXIOM_ variables) matches an earlier build on this host;
# scripts/web_cache.py holds the key and the cache.
web-build: web-install
    version="$({{ axiom_version }})" && {{ python }} scripts/web_cache.py build --version "$version" -- {{ web }} build

# Copy the three built apps into the Go tree for the program to embed (internal/platform/webui), each text file beside
# its brotli and gzip copies, with the content security policy the server sends. Every recipe that builds or tests the
# program depends on this, so what is embedded is always the build of this tree: a program built without it refuses to
# start and says to run this.
web-embed: web-build
    {{ web }} embed

# The frontend's checks: types; lint, which also rejects text written into a page instead of the translation files
# and functions too hard to follow; functions with too many paths or lines and files too long; unused files, exports
# and dependencies; duplicated code; unit tests; and the build within its size budget. CI runs this beside web-e2e.
web-checks: web-embed
    {{ web }} typecheck
    {{ web }} lint
    {{ web }} limits
    {{ web }} unused
    {{ web }} duplicates
    {{ web }} test
    {{ web }} budget

# Keep the shard index and count in the cached command: a partial pass cannot stand for any other shard or the suite.
web-e2e shard="1/1": web-embed
    version="$({{ axiom_version }})" && {{ python }} scripts/web_cache.py e2e --version "$version" -- {{ web }} e2e --shard={{ quote(shard) }}

# One browser against a real node with real PostgreSQL and Valkey (web/README.md「测试」): register, sign in, key, top-up,
# call, statement. It needs Docker, so it runs beside the e2e shards, not in their container.
web-journey: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster {{ web }} journey

# The web vitals of the console and admin (web/README.md「测试」), alone on the machine: they are timings.
web-vitals: web-embed
    {{ web }} vitals

# The local entry point retains every check; the workflow schedules the two parts independently.
web: web-checks web-e2e web-vitals
