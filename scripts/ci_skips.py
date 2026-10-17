"""Decide which CI work a pull request can skip from the files base..head changed.

Markdown-only changes run the cheap checks that read Markdown instead of the whole build; a benchmark comparison is
skipped only for Markdown-only and web/-only changes. The decisions come from the file list, never a guess about what
reads what. A failed diff runs the build and the comparison; bench-needed uses exit code 10 for "not needed", so any
other outcome runs the comparison.
"""

import subprocess
import sys

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


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print("usage: ci_skips.py md-only|bench-needed BASE [HEAD]", file=sys.stderr)
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
    print(f"unknown action {action}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
