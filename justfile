# Developer commands. CI runs these same recipes, so a green `just check` here means a green build in CI.
# Needs Go (GOTOOLCHAIN=auto fetches the version in go.mod), Python 3.11+, a C compiler for the race detector,
# and on Windows Git Bash.

set windows-shell := ["bash", "-c"]

python := if os_family() == "windows" { "python" } else { "python3" }
tool := "go tool -modfile=tools/go.mod"

# Run every check the CI build job runs
check: tidy lint modules deadcode test bench-smoke vuln docs

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
    out="$({{ tool }} deadcode ./...)"; [ -z "$out" ] || { echo "$out"; exit 1; }

test:
    go test -race ./...

# Benchmarks still compile and run; the comparison with main runs in CI
bench-smoke:
    go test -run '^$' -bench . -benchtime 1x ./...

vuln:
    {{ tool }} govulncheck ./...

docs:
    {{ python }} -m unittest discover -s scripts
    {{ python }} -m unittest discover -s scripts/workflow
    {{ python }} scripts/workflow/check_docs.py
