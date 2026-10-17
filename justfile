# Developer commands; CI runs the same recipes. Prerequisites are listed in README.md.

set windows-shell := ["bash", "-c"]

python := if os_family() == "windows" { "python" } else { "python3" }
tool := "go tool -modfile=tools/go.mod"
web := "pnpm --dir web"

# Run every check the CI build job runs, except one run of each benchmark: `check-all` adds those, and CI runs it on
# every pull request and main push
check: docs modules adapters-removable scope api tidy generated lint deadcode test vuln secrets web

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

# Only internal/platform/pg sets the tenant scope or switches the role, in Go and SQL, tests and migrations included;
# scripts/check_scope.py holds the rule and its reasons
scope:
    {{ python }} {{ quote(justfile_directory() / "scripts/check_scope.py") }}

# Every function is reachable from a program or a test: code nothing calls and nothing tests fails. Modules are built
# bottom-up, so a module's tests call it before the modules above it do; the functions only tests reach are listed,
# without failing, for the milestone's quality audit, by which time the program calls every module. Test-only
# packages such as pgtest, egresstest and suppliers' adaptertest, like net/http/httptest, are left out of that list;
# egress.NewForTesting lives in egress itself, so it stays listed.
deadcode:
    out="$({{ tool }} deadcode -test ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }
    out="$({{ tool }} deadcode ./...)" || exit 1; listed="$(printf '%s\n' "$out" | grep -v -e '^$' -e '^internal[/\\]platform[/\\]pgtest[/\\]' -e '^internal[/\\]platform[/\\]egresstest[/\\]' -e '^internal[/\\]suppliers[/\\]internal[/\\]adaptertest[/\\]')"; [ $? -le 1 ] || exit 1; [ -z "$listed" ] || printf '%s\n' "$listed" | sed 's/^/only tests reach: /'
    cd tools && out="$(go tool deadcode ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }

# Tests with only the flags go test can cache, so a result is reused until the package or an input it read changes. The
# packages share one PostgreSQL cluster, which internal/platform/pgtest/cmd/withcluster starts and removes; `go test
# ./pkg` alone starts its own. The test build still compiles every benchmark; `bench-once` runs them under the race
# detector, and bench-compare compares their allocations without it. At most four packages at once: eight, the default
# on the 8-core host, raced with the shared cluster and starved the timing-sensitive database tests; four is the limit
# the server proof used.
test: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=4 ./...
    go test -C tools -race ./...

# Every benchmark compiles and runs once, with the race detector. CI runs this on pull requests and main pushes;
# bench-compare still compares allocations on PRs, without -race.
bench-once: web-embed
    go run ./internal/platform/pgtest/cmd/withcluster go test -race -p=4 -run '^$' -bench . -benchtime 1x ./...

# Every check, including one run of each benchmark under the race detector
check-all: check bench-once

# The checks that read Markdown: the docs tests (including the pull request template), module boundaries, and the pg
# module table. A Markdown-only PR runs this instead of the full build.
md-checks: docs modules
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

compose := "docker compose -f deploy/compose.yaml"

# Build the program for Linux with the frontend embedded and start it with its database on this machine, writing random local passwords to
# deploy/.env, readable only by its owner, the first time; returns once it reports ready, or, when a step outlasts
# its limit, fails with the containers' state, health-check output and logs. New passwords would not open a database
# created with the old ones, so a missing deploy/.env next to an existing database volume stops it; only the migration
# administrator's password is added to a deploy/.env written before that role existed, and postgres/roles.sh, run
# before migrating, creates the role in such a database.
up: web-embed
    umask 077; [ -f deploy/.env ] || { volumes="$(docker volume ls --quiet --filter name=^axiom_postgres$)" || exit 1; [ -z "$volumes" ] || { echo "deploy/.env is missing but the database volume axiom_postgres exists: restore the file, or delete the local database with: just down -v"; exit 1; }; printf 'AXIOM_POSTGRES_PASSWORD=%s\nAXIOM_APP_PASSWORD=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > deploy/.env; }
    grep -q '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env || { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_MIGRATOR_PASSWORD=%s\n' "$(openssl rand -hex 24)"; } >> deploy/.env; chmod 600 deploy/.env
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
    {{ compose }} up --detach --wait --wait-timeout 60 postgres && {{ compose }} exec -T postgres sh < deploy/postgres/roles.sh && {{ compose }} up --detach --build --wait --wait-timeout 60 || { {{ compose }} ps --all; docker inspect --format '{{{{.Name}}: {{{{json .State.Health}}' $({{ compose }} ps --all --quiet); {{ compose }} logs; exit 1; }

# Stop everything; `just down -v` also deletes the local database and the master key made for it. By project name — the
# one COMPOSE_PROJECT_NAME names for a CI job, otherwise axiom — so it works without deploy/.env.
down *flags:
    docker compose --project-name "${COMPOSE_PROJECT_NAME:-axiom}" down {{ flags }}

# Merged migrations never change: against the base commit, files under migrations/ may only be added (a rename
# counts as a deletion). A git failure, such as a missing base or a shallow clone without the merge base, fails the
# check: an empty list from a failed diff would pass it.
migrations base:
    changed="$(git diff --name-only --no-renames --diff-filter=a {{ quote(base) }}...HEAD -- '*/migrations/*')" || exit 1; [ -z "$changed" ] || { echo "merged migrations were changed or removed:"; echo "$changed"; exit 1; }

vuln:
    {{ tool }} govulncheck ./...
    cd tools && go tool govulncheck ./...

# No secret in this branch's commits, checked before they are pushed: CI scans every commit of a pull request, and a
# secret, or a test value that looks like one, stays in the history until the branch is replaced. The version
# matches GITLEAKS_VERSION in .github/workflows/workflow.yml. Without origin/main, gitleaks would scan nothing and
# still pass, so a missing base fails instead.
secrets:
    git rev-parse --verify --quiet 'refs/remotes/origin/main^{commit}' > /dev/null || { echo "origin/main is missing, so the branch cannot be scanned: run git fetch origin main"; exit 1; }
    go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact --no-banner --log-opts=origin/main..HEAD .

docs:
    {{ python }} -m unittest discover -s scripts
    {{ python }} -m unittest discover -s scripts/workflow
    {{ python }} scripts/workflow/check_docs.py

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
# and dependencies; duplicated code; unit tests; the build within its size budget; and the end-to-end tests against
# that build, served by the program's own server from the embedded copy, which are told the version it was stamped with.
# A passing end-to-end run is reused when the hash of everything it checked (web/, api/, all Go code, go.mod/go.sum and
# the test configuration) matches an earlier pass on this host; any Go change misses.
web: web-embed
    {{ web }} typecheck
    {{ web }} lint
    {{ web }} limits
    {{ web }} unused
    {{ web }} duplicates
    {{ web }} test
    {{ web }} budget
    version="$({{ axiom_version }})" && {{ python }} scripts/web_cache.py e2e --version "$version" -- {{ web }} e2e
