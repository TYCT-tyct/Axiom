#!/usr/bin/env python3
"""Fail when the quality ratchet's counts grow, and say which baseline numbers to lower when they fall.

Why: the 2026-11-04 review found 474 nolint directives in hand-written non-test Go, 40 functions over cognitive
complexity 15, and 50 files over 500 lines, and nothing in CI counted them, so every pull request could add one with a
plausible reason. The baseline makes the totals a ratchet: they may only fall, and the same pull request that lowers one
must lower the committed number so the gain stays.

What is measured, in hand-written non-test Go:
- nolint directives per linter, read the way golangci-lint's nolint filter reads them; a directive naming several
  linters counts once under each, so the per-linter counts sum above the number of directives.
- functions over gocognit's configured threshold (15), using gocognit, the library golangci-lint's gocognit linter
  uses, pinned in tools/go.mod so the numbers match lint. //gocognit:ignore is not honored: the ratchet sees what lint
  was told to skip.
- files over 500 physical lines. This is stricter than revive's file-length-limit, which skips comments and blank
  lines, and is what the review counted.

A hard ceiling applies even with a nolint or //gocognit:ignore: no function may exceed cognitive complexity 30 and no
file may exceed 800 lines unless it is named in the baseline's gocognit_ceiling or file_ceiling, and each named value may
only shrink. Tests are files ending in _test.go and anything under a testdata directory. Generated code is a file named
*.gen.go (sqlc's output_files_suffix is .gen) or a Go file whose first declaration is preceded by the
`// Code generated ... DO NOT EDIT.` header, which is what go/ast.IsGenerated and golangci-lint's generated-code
exclusion recognize.

usage: python3 scripts/ci/quality_ratchet.py [--root DIR] [--write-baseline]
       --write-baseline lowers the committed baseline to the tree, and refuses when a count grew, so it can create the
       first baseline but never weaken one.
"""

import argparse
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BASELINE = Path(".github/quality-baseline.json")
VERSION = 1
COGNITIVE_LIMIT = 15
COGNITIVE_CEILING = 30
FILE_LIMIT = 500
FILE_CEILING = 800
DESCRIPTION = (
    "The quality ratchet: each pull request could add one plausible exemption, so these numbers may only fall. "
    "scripts/ci/quality_ratchet.py measures them; a nolint directive naming several linters counts under each, so the "
    "per-linter counts sum above the number of directives. The ceiling lists name the only functions over cognitive "
    "complexity 30 and files over 800 lines, and each value may only shrink."
)


def measure(root):
    """The measurement of root, from tools/qualityratchet in the tree that owns this script."""
    command = ["go", "run", "-C", str(ROOT / "tools"), "./qualityratchet", "--root", str(root)]
    try:
        completed = subprocess.run(command, capture_output=True, text=True, encoding="utf-8")
    except OSError as exc:
        raise SystemExit(f"quality ratchet: cannot run go: {exc}")
    if completed.returncode:
        raise SystemExit(f"quality ratchet: measuring {root} failed:\n{completed.stderr.strip()}")
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"quality ratchet: the measurement is not JSON: {exc}")


def current(measured):
    """The counts and ceiling lists from a measurement of the tree."""
    nolint = measured.get("nolint")
    functions = measured.get("functions_over_15")
    files = measured.get("files_over_500")
    if not isinstance(nolint, dict) or not isinstance(functions, list) or not isinstance(files, list):
        raise SystemExit("quality ratchet: the measurement has no nolint map or function or file list")
    return {
        "nolint_directives": measured.get("nolint_directives", 0),
        "nolint": {name: count for name, count in nolint.items() if count},
        "functions_over_gocognit_15": len(functions),
        "gocognit_ceiling": {function["name"]: function["complexity"] for function in functions
                             if function["complexity"] > COGNITIVE_CEILING},
        "files_over_500_lines": len(files),
        "file_ceiling": {file["path"]: file["lines"] for file in files if file["lines"] > FILE_CEILING},
    }


def number(value, name, path):
    if not isinstance(value, int) or isinstance(value, bool) or value < 0:
        raise SystemExit(f"quality ratchet: {path}: {name} must be a non-negative integer")
    return value


def entries(value, name, amount, path):
    """A ceiling list as name -> amount, rejecting a repeated name and a malformed value."""
    if not isinstance(value, list):
        raise SystemExit(f"quality ratchet: {path}: {name} must be a list")
    found = {}
    for entry in value:
        if not isinstance(entry, dict) or not isinstance(entry.get(name), str) or not entry[name]:
            raise SystemExit(f"quality ratchet: {path}: every {name} entry needs a non-empty {name}")
        if entry[name] in found:
            raise SystemExit(f"quality ratchet: {path}: {name} {entry[name]!r} is listed twice")
        found[entry[name]] = number(entry.get(amount), f"{name} {entry[name]!r} {amount}", path)
    return found


def load(path):
    """The committed baseline, checked enough that a typo cannot pass as a lower number."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise SystemExit(f"quality ratchet: cannot read {path}: {exc}")
    except json.JSONDecodeError as exc:
        raise SystemExit(f"quality ratchet: {path} is not valid JSON: {exc}")
    if not isinstance(data, dict) or data.get("version") != VERSION:
        raise SystemExit(f"quality ratchet: {path}: version must be {VERSION}")
    if data.get("ceilings") != {"gocognit": COGNITIVE_CEILING, "file_lines": FILE_CEILING}:
        raise SystemExit(f"quality ratchet: {path}: ceilings must be gocognit {COGNITIVE_CEILING} "
                         f"and file_lines {FILE_CEILING}")
    nolint = data.get("nolint")
    if not isinstance(nolint, dict):
        raise SystemExit(f"quality ratchet: {path}: nolint must be a map of linter to count")
    for name, count in nolint.items():
        if not isinstance(name, str) or not name:
            raise SystemExit(f"quality ratchet: {path}: every nolint key must be a linter name")
        number(count, f"nolint.{name}", path)
    return {
        "nolint": nolint,
        "functions_over_gocognit_15": number(data.get("functions_over_gocognit_15"),
                                            "functions_over_gocognit_15", path),
        "gocognit_ceiling": entries(data.get("gocognit_ceiling"), "function", "complexity", path),
        "files_over_500_lines": number(data.get("files_over_500_lines"), "files_over_500_lines", path),
        "file_ceiling": entries(data.get("file_ceiling"), "file", "lines", path),
    }


def compare(now, baseline):
    """The problems (counts above the baseline or a new ceiling breach) and the numbers that fell."""
    problems, fallen = [], []
    for linter in sorted(set(now["nolint"]) | set(baseline["nolint"])):
        have, want = now["nolint"].get(linter, 0), baseline["nolint"].get(linter, 0)
        if have > want:
            problems.append(f"nolint:{linter} grew from {want} to {have}")
        elif have < want:
            fallen.append(f"nolint:{linter} fell from {want} to {have}")

    for key, label in (("functions_over_gocognit_15", f"functions over gocognit {COGNITIVE_LIMIT}"),
                       ("files_over_500_lines", f"files over {FILE_LIMIT} lines")):
        have, want = now[key], baseline[key]
        if have > want:
            problems.append(f"{label} grew from {want} to {have}")
        elif have < want:
            fallen.append(f"{label} fell from {want} to {have}")

    problems += ceiling_problems(now["gocognit_ceiling"], baseline["gocognit_ceiling"],
                                 "function", f"cognitive-complexity ceiling {COGNITIVE_CEILING}")
    fallen += ceiling_falls(now["gocognit_ceiling"], baseline["gocognit_ceiling"],
                            "function", f"cognitive-complexity ceiling {COGNITIVE_CEILING}")
    problems += ceiling_problems(now["file_ceiling"], baseline["file_ceiling"],
                                 "file", f"{FILE_CEILING}-line ceiling")
    fallen += ceiling_falls(now["file_ceiling"], baseline["file_ceiling"], "file", f"{FILE_CEILING}-line ceiling")
    return problems, fallen


def ceiling_problems(now, baseline, name, label):
    """A named item above the hard ceiling that is new or grew past its recorded value."""
    problems = []
    for item in sorted(now):
        have, want = now[item], baseline.get(item)
        if want is None:
            problems.append(f"new {name} over the {label}: {item} ({have})")
        elif have > want:
            problems.append(f"{name} {item} grew from {want} to {have}, over the {label}")
    return problems


def ceiling_falls(now, baseline, name, label):
    """A named item that fell or left the ceiling, whose baseline value the pull request must lower or remove."""
    fallen = []
    for item in sorted(baseline):
        have, want = now.get(item), baseline[item]
        if have is None:
            fallen.append(f"{name} {item} is no longer over the {label}; remove it from the baseline")
        elif have < want:
            fallen.append(f"{name} {item} fell from {want} to {have}; lower its baseline value")
    return fallen


def report(problems, fallen):
    """Print both kinds of failure; 1 when either is present."""
    if problems:
        print("quality ratchet: the tree grew past the baseline:", file=sys.stderr)
        for problem in problems:
            print(f"  - {problem}", file=sys.stderr)
    if fallen:
        print(f"quality ratchet: these numbers fell; lower {BASELINE.as_posix()} in this PR:", file=sys.stderr)
        for fall in fallen:
            print(f"  - {fall}", file=sys.stderr)
    return 1 if problems or fallen else 0


def document(now):
    """The baseline file for a measurement, with stable ordering."""
    return {
        "version": VERSION,
        "description": DESCRIPTION,
        "ceilings": {"gocognit": COGNITIVE_CEILING, "file_lines": FILE_CEILING},
        "nolint": {name: now["nolint"][name] for name in sorted(now["nolint"])},
        "functions_over_gocognit_15": now["functions_over_gocognit_15"],
        "gocognit_ceiling": [{"function": name, "complexity": now["gocognit_ceiling"][name]}
                             for name in sorted(now["gocognit_ceiling"])],
        "files_over_500_lines": now["files_over_500_lines"],
        "file_ceiling": [{"file": name, "lines": now["file_ceiling"][name]}
                         for name in sorted(now["file_ceiling"])],
    }


def summary(now):
    return (f"quality ratchet: {now['nolint_directives']} nolint directives "
            f"({sum(now['nolint'].values())} per-linter entries) in {len(now['nolint'])} linters, "
            f"{now['functions_over_gocognit_15']} functions over {COGNITIVE_LIMIT}, "
            f"{now['files_over_500_lines']} files over {FILE_LIMIT} lines; baseline matches")


def main(argv=None):
    parser = argparse.ArgumentParser(description="Fail when the quality ratchet's numbers grow.")
    parser.add_argument("--root", type=Path, default=ROOT, help="the tree to measure (default: this repository)")
    parser.add_argument("--write-baseline", action="store_true",
                        help="lower the baseline to the tree; refuse when a count grew")
    args = parser.parse_args(argv)

    root = args.root.resolve()
    path = root / BASELINE
    now = current(measure(root))

    if args.write_baseline:
        if path.exists():
            problems, _ = compare(now, load(path))
            if problems:
                return report(problems, [])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(document(now), indent=2) + "\n", encoding="utf-8")
        print(f"quality ratchet: wrote {path}")
        return 0

    if not path.exists():
        raise SystemExit(f"quality ratchet: {path} is missing; run this script with --write-baseline to create it")
    problems, fallen = compare(now, load(path))
    status = report(problems, fallen)
    if not status:
        print(summary(now))
    return status


if __name__ == "__main__":
    sys.exit(main())
