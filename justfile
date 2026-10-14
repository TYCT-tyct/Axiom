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

# Module imports follow the dependency table in docs/architecture.md
modules:
    {{ python }} scripts/check_modules.py

# Every function is reachable from a program; code only tests reach is dead
deadcode:
    out="$({{ tool }} deadcode ./...)" || exit 1; [ -z "$out" ] || { echo "$out"; exit 1; }

# Tests, plus one iteration of every benchmark so benchmarks keep compiling and running
test:
    go test -race -bench . -benchtime 1x ./...

vuln:
    {{ tool }} govulncheck ./...

docs:
    {{ python }} -m unittest discover -s scripts
    {{ python }} -m unittest discover -s scripts/workflow
    {{ python }} scripts/workflow/check_docs.py
