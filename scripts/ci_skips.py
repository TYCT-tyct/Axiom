"""Decide which CI work a pull request can skip from the files base..head changed.

The decisions are content-based: a path list from git, never a guess about what a checkout contains. A file list is
empty on a failed diff, so md-only fails closed (run the build) and bench-needed fails open (run the comparison).
"""

import subprocess
import sys


def changed_paths(base: str, head: str = "HEAD") -> list[str]:
    out = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", f"{base}...{head}"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [line for line in out.splitlines() if line]


def only_md(paths: list[str]) -> bool:
    """A pull request that changes only Markdown files needs no build."""
    return bool(paths) and all(path.lower().endswith(".md") for path in paths)


def needs_benchmark(paths: list[str]) -> bool:
    """A benchmark comparison is worth its minutes only when production Go or a module file changed."""
    for path in paths:
        name = path.rsplit("/", 1)[-1]
        if name in ("go.mod", "go.sum"):
            return True
        if path.endswith(".go") and not path.endswith("_test.go"):
            return True
    return False


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print("usage: ci_skips.py md-only|bench-needed BASE [HEAD]", file=sys.stderr)
        return 2
    action, base = argv[0], argv[1]
    head = argv[2] if len(argv) == 3 else "HEAD"
    try:
        paths = changed_paths(base, head)
    except subprocess.CalledProcessError as error:
        print(f"cannot diff {base}...{head}: {error.stderr.strip()}", file=sys.stderr)
        return 1 if action == "md-only" else 0
    if action == "md-only":
        return 0 if only_md(paths) else 1
    if action == "bench-needed":
        return 0 if needs_benchmark(paths) else 1
    print(f"unknown action {action}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
