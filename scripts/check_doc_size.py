"""Enforce CONTRIBUTING's document caps and reject new domain-document history.

Run from the repository root. BASE is CI's PR/train base; locally use origin/main.
Compare the worktree with the merge base so staged and unstaged edits count too.
On main compare with its first parent. Module READMEs belong to internal/<module>,
not the module's nested implementation packages. Archives are outside both checks.
"""

import difflib
import os
from pathlib import Path
import re
import subprocess
import sys

DOMAIN = re.compile(r"docs/domains/[^/]+\.md")
MODULE = re.compile(r"internal/[^/]+/README\.md")
HISTORY = re.compile(r"(?<![0-9])[0-9]{4}-[0-9]{2}-[0-9]{2}(?![0-9])|「负责人[^」]*」|(?:new-api|sub2api)/[^\s`「」]+:[0-9]+")


def cap(path):
    if DOMAIN.fullmatch(path):
        return 30000
    if MODULE.fullmatch(path) or path == "docs/architecture.md":
        return 12000
    return None


def problems(path, text, previous):
    limit = cap(path)
    if limit is None:
        return []
    found = []
    if len(text) > limit:
        found.append(f"{path}: {len(text)} characters exceeds cap {limit} by {len(text) - limit} characters")
    if DOMAIN.fullmatch(path):
        lines = text.splitlines()
        for tag, _, _, start, end in difflib.SequenceMatcher(None, previous.splitlines(), lines, autojunk=False).get_opcodes():
            if tag in ("insert", "replace"):
                for index in range(start, end):
                    if HISTORY.search(lines[index]):
                        found.append(f"{path}:{index + 1}: history belongs in docs/archive/: {lines[index]}")
    return found


def git(*args):
    return subprocess.check_output(["git", *args]).decode("utf-8")


def merged_onto(base):
    """Whether HEAD merges something onto a first parent that already contains base."""
    parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()[1:]
    return len(parents) == 2 and subprocess.run(["git", "merge-base", "--is-ancestor", base, parents[0]]).returncode == 0


def main():
    head = git("rev-parse", "HEAD").strip()
    base = git("merge-base", os.environ.get("BASE") or "origin/main", head).strip()
    if base == head:
        base = git("rev-parse", "HEAD^").strip()
    elif merged_onto(base):
        # A pull request's build checks out its merge into the current base, while BASE is the base as of the PR's last
        # push: the base's own changes since then would count as the PR's new history (#784, run 43924805578).
        base = git("rev-parse", "HEAD^1").strip()
    previous_paths = set(git("ls-tree", "-r", "--name-only", "-z", base).split("\0"))
    paths = git("ls-files", "--cached", "--others", "--exclude-standard", "-z").split("\0")
    found = []
    checked = 0
    for path in sorted(set(paths)):
        if cap(path) is None or not Path(path).is_file():
            continue
        checked += 1
        text = Path(path).read_bytes().decode("utf-8")
        previous = git("show", f"{base}:{path}") if DOMAIN.fullmatch(path) and path in previous_paths else ""
        found.extend(problems(path, text, previous))
    if found:
        print("\n".join(found))
        return 1
    print(f"doc size/history: {checked} documents within caps; no new history lines")
    return 0


if __name__ == "__main__":
    sys.exit(main())
