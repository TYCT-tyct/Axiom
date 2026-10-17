"""Decide which CI work a pull request can skip from the files base..head changed.

The decisions are computed from content, not hand-kept lists: Markdown is skippable only when no tracked non-Markdown
file names it (a document can be an input to a code check), and a benchmark comparison is worth its minutes for
everything except Markdown-only and web/-only changes. A failed diff runs the build and the comparison.
"""

import subprocess
import sys


def changed_paths(base: str, head: str = "HEAD") -> list[str]:
    out = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", f"{base}...{head}"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [line for line in out.splitlines() if line]


def referenced_by_code(path: str) -> bool:
    """Whether a tracked non-Markdown file names path, as tools/checkmodules names docs/architecture.md."""
    result = subprocess.run(
        ["git", "grep", "-l", "-F", "-e", path, "--", ".", ":(exclude)*.md"],
        capture_output=True, text=True,
    )
    if result.returncode == 0:
        return bool(result.stdout.strip())
    if result.returncode == 1:
        return False
    return True  # a failed search must not skip the build


def all_md(paths: list[str]) -> bool:
    return bool(paths) and all(path.lower().endswith(".md") for path in paths)


def only_md(paths: list[str], referenced=referenced_by_code) -> bool:
    """A pull request that changes only Markdown files no code check reads needs no build."""
    return all_md(paths) and not any(referenced(path) for path in paths)


def web_only(paths: list[str]) -> bool:
    return bool(paths) and all(path.startswith("web/") for path in paths)


def needs_benchmark(paths: list[str]) -> bool:
    """Run the comparison for anything but Markdown-only and web/-only changes."""
    return not (all_md(paths) or web_only(paths))


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
