#!/usr/bin/env python3
"""Print code=true or code=false, for $GITHUB_OUTPUT: whether a pull request changes anything outside the light paths, so
a project's slow jobs (build, tests) run only for changes that can break them.

Managed by project-workflow: changed only by upgrades.
A path is light when it matches DOCS, MANAGED or a [lanes] light pattern in .github/project-workflow.toml, and no
[lanes] code pattern. A pattern is an fnmatch pattern on the path from the repository root; * also crosses
directories. Everything else is code. Any doubt runs the build: without BASE_REF (a push), on an empty diff, and when git or
the config fails, it prints code=true; a mistake costs a build, never a skipped one. A skipped required job counts as passed,
which is what lets a light PR merge without its build; the changes job itself is required, so its failure still blocks.
"""

import fnmatch
import os
import subprocess
import sys
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
CONFIG = ".github/project-workflow.toml"
DOCS = ("README.md", "CONTRIBUTING.md", "docs/*")
# The managed files, which the docs job tests on every PR; project-workflow's CI keeps this equal to SKILL.md's list
MANAGED = ("AGENTS.md", "CLAUDE.md", ".github/pull_request_template.md", ".github/workflows/workflow.yml",
           ".github/workflows/workflow-pr.yml", ".github/workflows/review-rerun.yml", "scripts/workflow/check_docs.py",
           "scripts/workflow/test_check_docs.py", "scripts/workflow/check_pr.py", "scripts/workflow/test_check_pr.py",
           "scripts/workflow/changes.py", "scripts/workflow/test_changes.py", ".github/project-workflow.toml")


def lanes(root):
    """(light, code) patterns from the project's [lanes]; check_docs.py reports a malformed table on every PR."""
    path = root / CONFIG
    table = tomllib.loads(path.read_text(encoding="utf-8-sig")).get("lanes", {}) if path.exists() else {}
    # the same test check_docs.py applies, so a table it rejects never reads as light here
    if not isinstance(table, dict) or set(table) - {"light", "code"} or not all(
            isinstance(value, list) and all(isinstance(item, str) and item for item in value) for value in table.values()):
        raise ValueError(f"{CONFIG}: [lanes] takes only light and code, each a list of path patterns")
    return tuple(table.get("light", ())), tuple(table.get("code", ()))


def code_changed(paths, light=(), code=()):
    def matches(path, patterns):
        return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)
    return not paths or any(not matches(path, DOCS + MANAGED + light) or matches(path, code) for path in paths)


def main():
    base = os.environ.get("BASE_REF")
    try:
        if not base:
            raise ValueError("no base branch: not a pull request")
        # --no-renames: a moved file shows its old path too, so moving code into docs/ still counts as code
        diff = ["git", "diff", "-z", "--no-renames", "--name-only", f"origin/{base}...HEAD"]
        paths = [path for path in subprocess.run(diff, capture_output=True, text=True, check=True).stdout.split("\0") if path]
        code = code_changed(paths, *lanes(ROOT))
    except (OSError, ValueError, subprocess.CalledProcessError, tomllib.TOMLDecodeError) as error:
        print(f"running the slow jobs: {error}", file=sys.stderr)
        code = True
    print(f"code={str(code).lower()}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
