#!/usr/bin/env python3
"""Fail if a Markdown file has a broken relative link, or a doc under docs/ is missing from the docs map."""

import os
import re
import sys
from pathlib import Path
from urllib.parse import unquote

SKIP_DIRS = {".git", "node_modules", ".venv"}

INLINE_LINK = re.compile(r"\]\(\s*(<[^>]*>|[^)\s]+)")
REFERENCE_DEF = re.compile(r"^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)")
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
CODE_SPAN = re.compile(r"(`+).*?\1")
SCHEME = re.compile(r"^[a-z][a-z0-9+.-]*:", re.IGNORECASE)


def markdown_files(root):
    for directory, subdirs, files in os.walk(root):
        subdirs[:] = [name for name in subdirs if name not in SKIP_DIRS]
        for name in files:
            if name.endswith(".md"):
                yield Path(directory, name)


def link_targets(line):
    reference = REFERENCE_DEF.match(line)
    if reference:
        yield reference.group(1)
    yield from INLINE_LINK.findall(CODE_SPAN.sub("", line))


def relative_links(path, root):
    """Yield (line number, resolved target) for relative links outside fenced code blocks."""
    fence = None
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if fence:
            # A fence closes only with the same character, at least as long, and nothing else on the line.
            stripped = line.strip()
            if stripped and set(stripped) == {fence[0]} and len(stripped) >= len(fence):
                fence = None
            continue
        opening = FENCE.match(line)
        if opening:
            fence = opening.group(1)
            continue
        for target in link_targets(line):
            target = target.strip("<>")
            if SCHEME.match(target) or target.startswith("#"):
                continue  # external URL, mailto, or same-page anchor
            target = unquote(target.split("#", 1)[0])
            base = root if target.startswith("/") else path.parent  # "/x" is repo-root-relative on GitHub
            yield number, (base / target.lstrip("/")).resolve()


def check(root):
    root = root.resolve()
    rel = lambda path: os.path.relpath(path, root).replace(os.sep, "/")
    docs_map = root / "docs" / "README.md"
    errors = []

    for path in markdown_files(root):
        for number, target in relative_links(path, root):
            if not target.exists():
                errors.append(f"{rel(path)}:{number}: broken link to {rel(target)}")

    mapped = {target for _, target in relative_links(docs_map, root)} if docs_map.exists() else set()
    for path in sorted((root / "docs").rglob("*.md")):
        path = path.resolve()
        if path != docs_map and path not in mapped:
            errors.append(f"{rel(path)}: not listed in docs/README.md")
    return errors


def main():
    errors = check(Path(__file__).parent.parent)
    for error in errors:
        print(error)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
