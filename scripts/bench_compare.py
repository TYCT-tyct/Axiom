#!/usr/bin/env python3
"""Run the benchmarks at a base commit and at the working tree, fail if any benchmark allocates more, report timings.

Allocations per operation are stable from run to run, so an increase fails; time per operation is noisy on shared CI
machines, so it is only reported, through benchstat (docs/performance.md「怎么验证」).
usage: bench_compare.py <base commit>   (run from the repository root)
"""

import re
import statistics
import subprocess
import sys
import tempfile
from pathlib import Path

BENCH = ["go", "test", "-run", "^$", "-bench", ".", "-benchmem", "-count", "6", "-benchtime", "100ms", "./..."]
PACKAGE = re.compile(r"^pkg: (\S+)")
RESULT = re.compile(r"^(Benchmark\S+?)(?:-\d+)?\s+\d+\s.*?\s(\d+) allocs/op")


def allocations(output):
    """Return {package.benchmark: median allocs/op} from `go test -bench -benchmem` output."""
    runs, package = {}, ""
    for line in output.splitlines():
        if found := PACKAGE.match(line):
            package = found.group(1)
        elif found := RESULT.match(line):
            runs.setdefault(f"{package}.{found.group(1)}", []).append(int(found.group(2)))
    return {name: statistics.median(counts) for name, counts in runs.items()}


def regressions(base, head):
    return [f"{name}: {base[name]:g} -> {head[name]:g} allocs/op" for name in sorted(head.keys() & base.keys()) if head[name] > base[name]]


def run_benchmarks(directory):
    return subprocess.run(BENCH, cwd=directory, check=True, capture_output=True, text=True).stdout


def main():
    base_ref = sys.argv[1]
    with tempfile.TemporaryDirectory() as scratch:
        tree = Path(scratch, "base")
        subprocess.run(["git", "worktree", "add", "--detach", str(tree), base_ref], check=True, capture_output=True)
        try:
            base_output = run_benchmarks(tree)
        finally:
            subprocess.run(["git", "worktree", "remove", "--force", str(tree)], check=True)
        head_output = run_benchmarks(".")
        Path(scratch, "base.txt").write_text(base_output, encoding="utf-8")
        Path(scratch, "head.txt").write_text(head_output, encoding="utf-8")
        benchstat = subprocess.run(["go", "tool", "-n", "-modfile=tools/go.mod", "benchstat"],
                                   check=True, capture_output=True, text=True).stdout.strip()
        subprocess.run([benchstat, "base.txt", "head.txt"], cwd=scratch, check=True)  # relative names label the columns
    problems = regressions(allocations(base_output), allocations(head_output))
    for problem in problems:
        print("more allocations:", problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
