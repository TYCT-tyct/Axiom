#!/usr/bin/env python3
"""Fail when the quality ratchet's numbers grow, and say which baseline numbers to lower when they fall.

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
- statement coverage per package from the profile CI's race test run writes with -coverprofile, so the suite runs once.
  Generated code is dropped from both sides of the fraction. The baseline stores hundredths of a percentage point
  (basis points) per package: a package may fall by at most tolerance_basis_points (0.50 point) below its baseline, a
  rise beyond the same tolerance must raise the baseline in the same pull request, and a package absent from the
  baseline is new and must start at or above floor_basis_points (60%). The tolerance is not slack for new code: two runs
  of the same commit on the same machine differed by at most 0.32 point (platform/kv 93.31% vs 92.99%), and 0.50 point
  is one statement in a package of up to 200, the smallest change the arithmetic can resolve in a package of that size.
  A committed package baseline may not be lower than the base's, so a manual lowering cannot turn the tolerance into a
  repeated allowance.
- packages with a hand-written function body containing statements but no test files are reported as zero, so a new
  package without tests cannot pass as unmeasured. A package whose profile records only zero-statement blocks (an empty
  function, for example) has no statements to rate and is omitted.

A hard ceiling applies even with a nolint or //gocognit:ignore: no function may exceed cognitive complexity 30 and no
file may exceed 800 lines unless it is named in the baseline's gocognit_ceiling or file_ceiling, and each named value may
only shrink. Tests are files ending in _test.go and anything under a testdata directory. Generated code is a file named
*.gen.go (sqlc's output_files_suffix is .gen) or a Go file whose first declaration is preceded by the
`// Code generated ... DO NOT EDIT.` header, which is what go/ast.IsGenerated and golangci-lint's generated-code
exclusion recognize.

usage: python3 scripts/ci/quality_ratchet.py [--root DIR] [--base REV] [--coverage PROFILE] [--write-baseline]
       [--report-coverage]
       --base (default: $BASE, the PR or train base) also reads the baseline committed at that revision and rejects any
       count increase, new hard-ceiling exception, exception value increase, or committed coverage baseline below the
       base. With coverage, a package below the base baseline by more than the tolerance or a new package below the floor
       is rejected too. Without a baseline there, the tree is the baseline's first introduction. --coverage reads the
       profile and compares per-package coverage; --report-coverage prints the measured numbers lowest first and exits.
       --write-baseline lowers the static counts and raises coverage, keeping a higher recorded coverage value when a
       measurement is inside the tolerance; it refuses when a count grew, coverage fell by more than the tolerance, a
       new package is below the floor, or the base baseline says it may not be weakened.
"""

import argparse
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
BASELINE = Path(".github/quality-baseline.json")
VERSION = 2
COGNITIVE_LIMIT = 15
COGNITIVE_CEILING = 30
FILE_LIMIT = 500
FILE_CEILING = 800
# Hundredths of a percentage point. The floor is 60%: the measured distribution is p25 58.59%, median 84.61%, and the
# 2026-11-04 review put the tree at 57.2%; 60% starts a new package at the lower quartile, above the weakest quarter and
# the review's number, without demanding the median from a package whose first tests are still small. The tolerance is
# 0.50 point: two runs of the same commit differed by at most 0.32 point (kv 93.31% vs 92.99%), and half a point is one
# statement in a package of up to 200 statements, the smallest movement a small package can show.
COVERAGE_TOLERANCE = 50
COVERAGE_FLOOR = 6000
DESCRIPTION = (
    "The quality ratchet: each pull request could add one plausible exemption, so these numbers may only fall. "
    "scripts/ci/quality_ratchet.py measures them; a nolint directive naming several linters counts under each, so the "
    "per-linter counts sum above the number of directives. The ceiling lists name the only functions over cognitive "
    "complexity 30 and files over 800 lines, and each value may only shrink. coverage.packages is statement coverage "
    "per package in hundredths of a percentage point, measured from the profile the race test run writes, generated "
    "code excluded; tolerance_basis_points is how far a package may fall and floor_basis_points is where a new package "
    "must start. A write keeps the higher recorded value when a measurement is inside the tolerance, so the tolerance "
    "cannot lower the baseline repeatedly."
)


def measure(root, coverage=None):
    """The measurement of root, from tools/qualityratchet in the tree that owns this script."""
    command = ["go", "run", "-C", str(ROOT / "tools"), "./qualityratchet", "--root", str(root)]
    if coverage is not None:
        command += ["--coverage", str(coverage.resolve())]
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


def points(covered, total):
    """Statement coverage in hundredths of a percentage point, rounded down so a number below a threshold cannot round up
    to it."""
    if total == 0:
        return 0
    return covered * 10000 // total


def current(measured):
    """The counts and ceiling lists from a measurement of the tree, and its coverage when a profile was read."""
    nolint = measured.get("nolint")
    functions = measured.get("functions_over_15")
    files = measured.get("files_over_500")
    if not isinstance(nolint, dict) or not isinstance(functions, list) or not isinstance(files, list):
        raise SystemExit("quality ratchet: the measurement has no nolint map or function or file list")
    result = {
        "nolint_directives": measured.get("nolint_directives", 0),
        "nolint": {name: count for name, count in nolint.items() if count},
        "functions_over_gocognit_15": len(functions),
        "gocognit_ceiling": {function["name"]: function["complexity"] for function in functions
                             if function["complexity"] > COGNITIVE_CEILING},
        "files_over_500_lines": len(files),
        "file_ceiling": {file["path"]: file["lines"] for file in files if file["lines"] > FILE_CEILING},
        "coverage": None,
    }
    coverage = measured.get("coverage")
    if coverage is not None:
        if not isinstance(coverage, dict):
            raise SystemExit("quality ratchet: the measurement's coverage is not a map of package to counts")
        packages = {}
        for name, counts in coverage.items():
            if not isinstance(name, str) or not name or not isinstance(counts, dict):
                raise SystemExit("quality ratchet: every coverage entry needs a package name and counts")
            covered = number(counts.get("covered"), f"coverage of {name} covered", "the measurement")
            total = number(counts.get("total"), f"coverage of {name} total", "the measurement")
            if covered > total:
                raise SystemExit(f"quality ratchet: coverage of {name} covers {covered} of {total} statements")
            packages[name] = points(covered, total)
        result["coverage"] = {"tolerance": COVERAGE_TOLERANCE, "floor": COVERAGE_FLOOR, "packages": packages}
    return result


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


def checked_coverage(value, path):
    """The coverage section: the fixed tolerance and floor, and each package's basis points."""
    if not isinstance(value, dict):
        raise SystemExit(f"quality ratchet: {path}: coverage must be a map")
    tolerance = number(value.get("tolerance_basis_points"), "coverage.tolerance_basis_points", path)
    floor = number(value.get("floor_basis_points"), "coverage.floor_basis_points", path)
    if tolerance != COVERAGE_TOLERANCE:
        raise SystemExit(f"quality ratchet: {path}: coverage.tolerance_basis_points must be {COVERAGE_TOLERANCE}")
    if floor != COVERAGE_FLOOR:
        raise SystemExit(f"quality ratchet: {path}: coverage.floor_basis_points must be {COVERAGE_FLOOR}")
    packages = value.get("packages")
    if not isinstance(packages, dict):
        raise SystemExit(f"quality ratchet: {path}: coverage.packages must be a map of package to basis points")
    found = {}
    for name, points_value in packages.items():
        if not isinstance(name, str) or not name:
            raise SystemExit(f"quality ratchet: {path}: every coverage package needs a name")
        points_value = number(points_value, f"coverage.packages[{name!r}]", path)
        if points_value > 10000:
            raise SystemExit(f"quality ratchet: {path}: coverage of {name} is above 100.00%")
        found[name] = points_value
    return {"tolerance": tolerance, "floor": floor, "packages": found}


def checked(data, path):
    """The baseline data, checked enough that a typo cannot pass as a lower number.

    Version 1 predates coverage. A base may still carry it (this ratchet's first introduction); only the current
    baseline must be version 2, which load enforces.
    """
    version = data.get("version") if isinstance(data, dict) else None
    if version not in (1, VERSION):
        raise SystemExit(f"quality ratchet: {path}: version must be 1 or {VERSION}")
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
        "version": version,
        "nolint": nolint,
        "functions_over_gocognit_15": number(data.get("functions_over_gocognit_15"),
                                            "functions_over_gocognit_15", path),
        "gocognit_ceiling": entries(data.get("gocognit_ceiling"), "function", "complexity", path),
        "files_over_500_lines": number(data.get("files_over_500_lines"), "files_over_500_lines", path),
        "file_ceiling": entries(data.get("file_ceiling"), "file", "lines", path),
        "coverage": checked_coverage(data["coverage"], path) if version == VERSION else None,
    }


def read(path):
    """The committed baseline, checked enough that a typo cannot pass as a lower number."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise SystemExit(f"quality ratchet: cannot read {path}: {exc}")
    except json.JSONDecodeError as exc:
        raise SystemExit(f"quality ratchet: {path} is not valid JSON: {exc}")
    return checked(data, path)


def load(path):
    """The committed baseline. Version 1 predates coverage; a static-only check can still use it."""
    return read(path)


def git(root, *args):
    return subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True, encoding="utf-8")


def load_base(root, base):
    """The baseline committed at base, or None when that commit has no baseline (its first introduction).

    The base's ceilings must match the current rule, so raising the hard ceiling and its baseline together cannot pass.
    """
    top = git(root, "rev-parse", "--show-toplevel")
    if top.returncode:
        raise SystemExit(f"quality ratchet: --base needs a git worktree: {top.stderr.strip()}")
    if Path(top.stdout.strip()).resolve() != root.resolve():
        raise SystemExit("quality ratchet: --base needs --root to be the git repository root")
    commit = git(root, "rev-parse", "--verify", f"{base}^{{commit}}")
    if commit.returncode:
        raise SystemExit(f"quality ratchet: base {base!r} is not a commit: {commit.stderr.strip()}")
    revision = commit.stdout.strip()
    path = BASELINE.as_posix()
    present = git(root, "ls-tree", "--name-only", revision, "--", path)
    if present.returncode:
        raise SystemExit(f"quality ratchet: cannot read {base}:{path}: {present.stderr.strip()}")
    if not present.stdout.strip():
        return None
    shown = git(root, "show", f"{revision}:{path}")
    if shown.returncode:
        raise SystemExit(f"quality ratchet: cannot read {base}:{path}: {shown.stderr.strip()}")
    try:
        data = json.loads(shown.stdout)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"quality ratchet: {base}:{path} is not valid JSON: {exc}")
    return checked(data, f"{base}:{path}")


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

    if now["coverage"] is not None and baseline["coverage"] is not None:
        coverage_problems, coverage_falls = compare_coverage(now["coverage"], baseline["coverage"])
        problems += coverage_problems
        fallen += coverage_falls
    return problems, fallen


def percent(points_value):
    return f"{points_value / 100:.2f}%"


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


def compare_coverage(now, baseline):
    """A fall beyond the tolerance, a new package below the floor, and the rises a pull request must record."""
    problems, fallen = [], []
    tolerance, floor = baseline["tolerance"], baseline["floor"]
    for name in sorted(set(now["packages"]) | set(baseline["packages"])):
        have, want = now["packages"].get(name), baseline["packages"].get(name)
        if want is None:
            if have < floor:
                problems.append(f"new package {name} is at {percent(have)}, below the {percent(floor)} floor")
            else:
                fallen.append(f"coverage of new package {name} is {percent(have)}; add it to the baseline")
        elif have is None:
            fallen.append(f"package {name} is no longer measured; remove it from the baseline")
        elif have < want - tolerance:
            problems.append(f"coverage of {name} fell from {percent(want)} to {percent(have)} "
                            f"(tolerance {percent(tolerance)})")
        elif have > want + tolerance:
            fallen.append(f"coverage of {name} rose from {percent(want)} to {percent(have)}; raise its baseline")
    return problems, fallen


def base_problems(now, base):
    """Growth against the baseline committed at the base: a pull request cannot raise a number and its baseline together."""
    problems = []
    for linter in sorted(set(now["nolint"]) | set(base["nolint"])):
        have, want = now["nolint"].get(linter, 0), base["nolint"].get(linter, 0)
        if have > want:
            problems.append(f"base baseline: nolint:{linter} grew from {want} to {have}")

    for key, label in (("functions_over_gocognit_15", f"functions over gocognit {COGNITIVE_LIMIT}"),
                       ("files_over_500_lines", f"files over {FILE_LIMIT} lines")):
        have, want = now[key], base[key]
        if have > want:
            problems.append(f"base baseline: {label} grew from {want} to {have}")

    problems += [f"base baseline: {problem}" for problem in ceiling_problems(
        now["gocognit_ceiling"], base["gocognit_ceiling"],
        "function", f"cognitive-complexity ceiling {COGNITIVE_CEILING}")]
    problems += [f"base baseline: {problem}" for problem in ceiling_problems(
        now["file_ceiling"], base["file_ceiling"], "file", f"{FILE_CEILING}-line ceiling")]

    if now["coverage"] is not None and base["coverage"] is not None:
        tolerance, floor = base["coverage"]["tolerance"], base["coverage"]["floor"]
        for name in sorted(set(now["coverage"]["packages"]) | set(base["coverage"]["packages"])):
            have = now["coverage"]["packages"].get(name)
            want = base["coverage"]["packages"].get(name)
            if want is None:
                if have is not None and have < floor:
                    problems.append(f"base baseline: new package {name} is at {percent(have)}, "
                                    f"below the {percent(floor)} floor")
            elif have is not None and have < want - tolerance:
                problems.append(f"base baseline: coverage of {name} fell from {percent(want)} to {percent(have)}")
    return problems


def baseline_problems(baseline, base, now):
    """The committed baseline may not be lowered against the base.

    The measured comparison has a tolerance; without this check a package's baseline could be lowered by the tolerance,
    measured at the lower value, and lowered again, making the tolerance a per-pull-request allowance. A package removed
    from the tree may also leave the baseline; a package the tree still measures may not disappear from it.
    """
    recorded = (baseline or {}).get("coverage")
    remote = (base or {}).get("coverage")
    if recorded is None or remote is None:
        return []
    measured = now["coverage"]["packages"] if now["coverage"] is not None else {}
    problems = []
    for name, want in sorted(remote["packages"].items()):
        have = recorded["packages"].get(name)
        if have is None:
            if name in measured:
                problems.append(f"base baseline: {name} is still measured but missing from the baseline")
        elif have < want:
            problems.append(f"base baseline: the baseline of {name} fell from {percent(want)} to {percent(have)}")
    return problems


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


def recorded_coverage(now, baseline):
    """The coverage to write: a rise moves a package's number, a fall inside the tolerance does not.

    The tolerance exists to absorb a difference between two runs of the same commit, not to become a lower baseline.
    Keeping the higher recorded value means two small falls cannot compound into a larger one.
    """
    measured = now["coverage"]
    previous = (baseline or {}).get("coverage")
    if measured is None:
        return previous
    if previous is None:
        return measured
    packages = dict(measured["packages"])
    for name, value in previous["packages"].items():
        if name in packages:
            packages[name] = max(packages[name], value)
    return {"tolerance": measured["tolerance"], "floor": measured["floor"], "packages": packages}


def document(now, baseline=None):
    """The baseline file for a measurement, with stable ordering.

    A static-only write keeps the coverage the committed baseline already records, so lowering a count does not drop
    the coverage ratchet by accident. A coverage write keeps an existing package's higher baseline when the measurement
    is inside the tolerance.
    """
    coverage = recorded_coverage(now, baseline)
    result = {
        "version": VERSION if coverage is not None else (baseline or {}).get("version", 1),
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
    if coverage is not None:
        result["coverage"] = {
            "tolerance_basis_points": coverage["tolerance"],
            "floor_basis_points": coverage["floor"],
            "packages": {name: coverage["packages"][name] for name in sorted(coverage["packages"])},
        }
    return result


def summary(now):
    text = (f"quality ratchet: {now['nolint_directives']} nolint directives "
            f"({sum(now['nolint'].values())} per-linter entries) in {len(now['nolint'])} linters, "
            f"{now['functions_over_gocognit_15']} functions over {COGNITIVE_LIMIT}, "
            f"{now['files_over_500_lines']} files over {FILE_LIMIT} lines")
    if now["coverage"] is not None:
        text += f", coverage measured for {len(now['coverage']['packages'])} packages"
    return text + "; baseline matches"


def print_coverage(coverage, measured):
    """Every package's measured coverage, lowest first, then the distribution the floor is picked from."""
    packages = coverage["packages"]
    raw = measured.get("coverage") or {}
    for name, points_value in sorted(packages.items(), key=lambda item: (item[1], item[0])):
        counts = raw.get(name, {})
        if counts.get("total", 0) == 0:
            detail = "no test files"
        else:
            detail = f"{counts['covered']}/{counts['total']} statements"
        print(f"{percent(points_value):>8}  {name}  ({detail})")
    values = sorted(packages.values())
    if values:
        def at(fraction):
            return values[min(len(values) - 1, int(len(values) * fraction))]
        print(f"quality ratchet: {len(values)} packages, min {percent(values[0])}, p25 {percent(at(0.25))}, "
              f"median {percent(at(0.5))}, p75 {percent(at(0.75))}, max {percent(values[-1])}")


def main(argv=None):
    parser = argparse.ArgumentParser(description="Fail when the quality ratchet's numbers grow.")
    parser.add_argument("--root", type=Path, default=ROOT, help="the tree to measure (default: this repository)")
    parser.add_argument("--base", default=os.environ.get("BASE", ""),
                        help="the commit whose committed baseline must not be exceeded (default: $BASE)")
    parser.add_argument("--coverage", type=Path, default=None,
                        help="the go test -coverprofile profile to measure statement coverage from")
    parser.add_argument("--report-coverage", action="store_true",
                        help="print each package's coverage, lowest first, and exit")
    parser.add_argument("--write-baseline", action="store_true",
                        help="lower the static counts and raise coverage, keeping a higher coverage baseline; refuse "
                             "when a count grew, coverage fell beyond the tolerance, or the base baseline says no")
    args = parser.parse_args(argv)
    if args.report_coverage and args.coverage is None:
        parser.error("--report-coverage needs --coverage")

    root = args.root.resolve()
    path = root / BASELINE
    measured = measure(root, args.coverage)
    now = current(measured)
    if args.report_coverage:
        print_coverage(now["coverage"], measured)
        return 0
    base = load_base(root, args.base) if args.base else None

    if args.write_baseline:
        problems = []
        existing = read(path) if path.exists() else None
        if existing is not None:
            current_problems, _ = compare(now, existing)
            problems += current_problems
        elif base is not None:
            raise SystemExit(f"quality ratchet: {args.base} already has a baseline; restore {path} instead of "
                             "creating a weaker one")
        if base is not None:
            problems += base_problems(now, base)
        recorded = document(now, existing)
        if base is not None:
            problems += baseline_problems(recorded, base, now)
        if problems:
            return report(problems, [])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(recorded, indent=2) + "\n", encoding="utf-8")
        print(f"quality ratchet: wrote {path}")
        return 0

    if not path.exists():
        raise SystemExit(f"quality ratchet: {path} is missing; run this script with --write-baseline to create it")
    baseline = load(path)
    if now["coverage"] is not None and baseline["coverage"] is None:
        raise SystemExit(f"quality ratchet: {path} has no coverage section; run this script with --coverage and "
                         "--write-baseline to add it")
    problems, fallen = compare(now, baseline)
    if args.base:
        if base is None:
            print(f"quality ratchet: {args.base} has no baseline; treating this as its first introduction")
        else:
            problems += base_problems(now, base)
            problems += baseline_problems(baseline, base, now)
    status = report(problems, fallen)
    if not status:
        print(summary(now))
    return status


if __name__ == "__main__":
    sys.exit(main())
