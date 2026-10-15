# Developer commands; CI runs the same recipes. Prerequisites are listed in README.md.

set windows-shell := ["bash", "-c"]

python := if os_family() == "windows" { "python" } else { "python3" }
tool := "go tool -modfile=tools/go.mod"
web := "pnpm --dir web"

# Run every check the CI build job runs
check: docs modules scope tidy generated lint deadcode test vuln secrets web

# Regenerate code from its contracts (api/*.yaml) and its queries (sqlc.yaml). Every generated file is deleted first, so
# the files left are exactly the generators' output: a stale one stays out, and so does a hand-written one named
# *.gen.go or *.gen.ts, which lint and the frontend checks skip by that name.
generate: web-install
    git ls-files -z --cached --others --exclude-standard -- '*.gen.go' '*.gen.ts' | xargs -0 rm -f
    go generate ./...
    {{ tool }} sqlc generate
    {{ web }} --filter @axiom/api generate

# Generated code matches its contracts: regenerating changes, adds or removes nothing. Only *.gen.go and *.gen.ts are
# compared, so a generator's output under another name would never be seen stale: it must carry the suffix (sqlc.yaml).
generated: generated-names generate
    changed="$(git status --porcelain --untracked-files=all -- '*.gen.go' '*.gen.ts')"; [ -z "$changed" ] || { echo "generated code is stale or not committed, or a hand-written file is named like generated code: run just generate and commit the result"; echo "$changed"; exit 1; }

# Only generators' output may claim to be generated: golangci-lint (exclusions.generated in .golangci.yml) skips every
# linter on a Go file with the header `// Code generated ... DO NOT EDIT.` before its first code, whoever wrote it.
# The match is wider than that rule (the whole file, trailing white space, so a CRLF header too), so it never misses a
# header the rule reads. Files named *.gen.go are the exempt ones: `just generated` proves a generator wrote them.
[private]
generated-names:
    found="$(git grep --untracked -l -E '^// Code generated .* DO NOT EDIT\.[[:space:]]*$' -- '*.go' ':!*.gen.go')"; [ $? -le 1 ] || exit 1; [ -z "$found" ] || { echo "only generated files carry the generated-code header, and they are named *.gen.go (output_files_suffix in sqlc.yaml):"; echo "$found"; exit 1; }

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

# Only internal/platform/pg sets the tenant scope or switches the role, in Go and SQL, tests and migrations included;
# scripts/check_scope.py holds the rule and its reasons
scope:
    {{ python }} {{ quote(justfile_directory() / "scripts/check_scope.py") }}

# Every function is reachable from a program or a test: code nothing calls and nothing tests fails. Modules are built
# bottom-up, so a module's tests call it before the modules above it do; the functions only tests reach are listed,
# without failing, for the milestone's quality audit, by which time the program calls every module. Test-only
# packages such as pgtest and egresstest, like net/http/httptest, are left out of that list; egress.NewForTesting
# lives in egress itself, so it stays listed.
deadcode:
    out="$({{ tool }} deadcode -test ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }
    out="$({{ tool }} deadcode ./...)" || exit 1; listed="$(printf '%s\n' "$out" | grep -v -e '^$' -e '^internal[/\\]platform[/\\]pgtest[/\\]' -e '^internal[/\\]platform[/\\]egresstest[/\\]')"; [ $? -le 1 ] || exit 1; [ -z "$listed" ] || printf '%s\n' "$listed" | sed 's/^/only tests reach: /'
    cd tools && out="$(go tool deadcode ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }

# Tests, plus one iteration of every benchmark so benchmarks keep compiling and running
test:
    go test -race -bench . -benchtime 1x ./...
    go test -C tools -race ./...

# Record a protocol sample: send the request file (a path from the repository root) to a live upstream and save the
# stream as internal/protocol/testdata/<format>/<scenario>/, scrubbed and checked. Base URLs and keys come from the
# environment or .env, as .env.example lists; internal/protocol/README.md「录制样本」 has the rules.
record format scenario request:
    go run ./internal/protocol/internal/record internal/protocol/testdata {{ quote(format) }} {{ quote(scenario) }} {{ quote(request) }}

# Run the benchmarks at a base commit and here: more allocations fail, timings are only reported
bench-compare base:
    go run -C tools ./benchgate .. {{ quote(base) }}

compose := "docker compose -f deploy/compose.yaml"

# Build the program for Linux and start it with its database on this machine, writing random local passwords to
# deploy/.env, readable only by its owner, the first time; returns once it reports ready, or, when a step outlasts
# its limit, fails with the containers' state, health-check output and logs. New passwords would not open a database
# created with the old ones, so a missing deploy/.env next to an existing database volume stops it; only the migration
# administrator's password is added to a deploy/.env written before that role existed, and postgres/roles.sh, run
# before migrating, creates the role in such a database.
up:
    umask 077; [ -f deploy/.env ] || { volumes="$(docker volume ls --quiet --filter name=^axiom_postgres$)" || exit 1; [ -z "$volumes" ] || { echo "deploy/.env is missing but the database volume axiom_postgres exists: restore the file, or delete the local database with: just down -v"; exit 1; }; printf 'AXIOM_POSTGRES_PASSWORD=%s\nAXIOM_APP_PASSWORD=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > deploy/.env; }
    grep -q '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env || { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_MIGRATOR_PASSWORD=%s\n' "$(openssl rand -hex 24)"; } >> deploy/.env; chmod 600 deploy/.env
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
    {{ compose }} up --detach --wait --wait-timeout 60 postgres && {{ compose }} exec -T postgres sh < deploy/postgres/roles.sh && {{ compose }} up --detach --build --wait --wait-timeout 60 || { {{ compose }} ps --all; docker inspect --format '{{{{.Name}}: {{{{json .State.Health}}' $({{ compose }} ps --all --quiet); {{ compose }} logs; exit 1; }

# Stop everything; `just down -v` also deletes the local database. By project name, so it works without deploy/.env.
down *flags:
    docker compose --project-name axiom down {{ flags }}

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
# built from this tree, to notice when the program answering is another one.
web-build: web-install
    version="$({{ axiom_version }})" && AXIOM_VERSION="$version" {{ web }} build

# The frontend's checks: types; lint, which also rejects text written into a page instead of the translation files
# and functions too hard to follow; functions with too many paths or lines and files too long; unused files, exports
# and dependencies; duplicated code; unit tests; the build within its size budget; and the end-to-end tests against
# that build, which are told the version it was stamped with
web: web-build
    {{ web }} typecheck
    {{ web }} lint
    {{ web }} limits
    {{ web }} unused
    {{ web }} duplicates
    {{ web }} test
    {{ web }} budget
    version="$({{ axiom_version }})" && AXIOM_VERSION="$version" {{ web }} e2e
