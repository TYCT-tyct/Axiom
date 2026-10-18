"""Decide which CI work a pull request can skip from the files base..head changed.

Markdown-only changes run the cheap checks that read Markdown instead of the whole build; a benchmark comparison is
skipped only for Markdown-only and web/-only changes, and the deployment check runs only for changes that can move
what it deploys. The decisions come from the file list, never a guess about what reads what. A failed diff runs the
build, the comparison and the deployment check; bench-needed and deploy-needed use exit code 10 for "not needed", so
any other outcome runs them.
"""

import subprocess
import sys
from pathlib import PurePosixPath

NOT_NEEDED = 10


def changed_paths(base: str, head: str = "HEAD") -> list[str]:
    out = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", f"{base}...{head}"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [line for line in out.splitlines() if line]


def all_md(paths: list[str]) -> bool:
    return bool(paths) and all(path.lower().endswith(".md") for path in paths)


def web_only(paths: list[str]) -> bool:
    return bool(paths) and all(path.startswith("web/") for path in paths)


def needs_benchmark(paths: list[str]) -> bool:
    """Run the comparison for anything but Markdown-only and web/-only changes."""
    return not (all_md(paths) or web_only(paths))


def moves_deployment(path: str) -> bool:
    """Whether a changed file can alter what scripts/ci/deploy-check.sh deploys or how it checks it."""
    parts = PurePosixPath(path).parts
    name = parts[-1]
    if parts[0] == "deploy" or "migrations" in parts[:-1] or name.startswith("Dockerfile"):
        return True
    if path in ("scripts/ci/deploy-check.sh", "scripts/deploy_name.py", "justfile"):  # the script and the `up` it runs
        return True
    return parts[:2] == ("cmd", "axiom") and not name.endswith("_test.go") and any(
        word in name for word in ("config", "env", "variables")
    )


def needs_deploy_check(paths: list[str]) -> bool:
    return any(moves_deployment(path) for path in paths)


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print("usage: ci_skips.py md-only|bench-needed|deploy-needed BASE [HEAD]", file=sys.stderr)
        return 2
    action, base = argv[0], argv[1]
    head = argv[2] if len(argv) == 3 else "HEAD"
    try:
        paths = changed_paths(base, head)
    except (subprocess.CalledProcessError, OSError) as error:
        print(f"cannot diff {base}...{head}: {error}", file=sys.stderr)
        return 1 if action == "md-only" else 0
    if action == "md-only":
        return 0 if all_md(paths) else 1
    if action == "bench-needed":
        return 0 if needs_benchmark(paths) else NOT_NEEDED
    if action == "deploy-needed":
        return 0 if needs_deploy_check(paths) else NOT_NEEDED
    print(f"unknown action {action}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
