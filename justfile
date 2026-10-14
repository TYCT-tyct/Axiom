# Developer commands; CI runs the same recipes. Prerequisites are listed in README.md.

set windows-shell := ["bash", "-c"]

python := if os_family() == "windows" { "python" } else { "python3" }
tool := "go tool -modfile=tools/go.mod"

# Run every check the CI build job runs
check: docs modules tidy lint deadcode test vuln

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

# Every function is reachable from a program; code only tests reach is dead. The one exception: packages named
# *test, like net/http/httptest, exist only to serve tests.
deadcode:
    out="$({{ tool }} deadcode ./...)" || exit 1; out="$(printf '%s' "$out" | grep -vE '[/\\][a-z]+test[/\\][a-z_]+\.go:' || true)"; [ -z "$out" ] || { echo "$out"; exit 1; }
    cd tools && out="$(go tool deadcode ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }

# Tests, plus one iteration of every benchmark so benchmarks keep compiling and running
test:
    go test -race -bench . -benchtime 1x ./...
    go test -C tools -race ./...

# Run the benchmarks at a base commit and here: more allocations fail, timings are only reported
bench-compare base:
    go run -C tools ./benchgate .. {{ quote(base) }}

compose := "docker compose -f deploy/compose.yaml"

# Build the program for Linux and start it on this machine; returns once it reports ready, or within a minute fails
# with the containers' state, health-check output and logs
up:
    CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
    {{ compose }} up --detach --build --wait --wait-timeout 60 || { {{ compose }} ps --all; docker inspect --format '{{{{.Name}}: {{{{json .State.Health}}' $({{ compose }} ps --all --quiet); {{ compose }} logs; exit 1; }

down:
    {{ compose }} down

# Merged migrations never change: against the base commit, files under migrations/ may only be added (a rename
# counts as a deletion)
migrations base:
    changed="$(git diff --name-only --no-renames --diff-filter=a {{ quote(base) }}...HEAD -- '*/migrations/*')"; [ -z "$changed" ] || { echo "merged migrations were changed or removed:"; echo "$changed"; exit 1; }

vuln:
    {{ tool }} govulncheck ./...

docs:
    {{ python }} -m unittest discover -s scripts
    {{ python }} -m unittest discover -s scripts/workflow
    {{ python }} scripts/workflow/check_docs.py
