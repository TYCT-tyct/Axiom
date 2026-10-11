#!/usr/bin/env python3
"""Fail if a Markdown file has a broken relative link, or a doc under docs/ is missing from the docs map."""

import os
import re
import sys
from pathlib import Path
from urllib.parse import unquote

ROOT = Path(__file__).resolve().parent.parent
DOCS_DIR = ROOT / "docs"
DOCS_MAP = DOCS_DIR / "README.md"
SKIP_DIRS = {".git", "node_modules"}

LINK = re.compile(r"\[[^\]]*\]\(([^)\s]+)[^)]*\)")
FENCE = re.compile(r"^\s*(```|~~~)")
INLINE_CODE = re.compile(r"`[^`]*`")
SCHEME = re.compile(r"^[a-z][a-z0-9+.-]*:", re.IGNORECASE)


def markdown_files():
    for path in ROOT.rglob("*.md"):
        if not SKIP_DIRS.intersection(path.relative_to(ROOT).parts):
            yield path


def relative_links(path):
    """Yield (line number, resolved target) for relative links outside code blocks and code spans."""
    in_fence = False
    for number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if FENCE.match(line):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        for target in LINK.findall(INLINE_CODE.sub("", line)):
            if SCHEME.match(target) or target.startswith("#"):
                continue  # external URL, mailto, or same-page anchor
            target = unquote(target.split("#", 1)[0])
            yield number, (path.parent / target).resolve()


def rel(path):
    return os.path.relpath(path, ROOT).replace(os.sep, "/")


def main():
    errors = []
    for path in markdown_files():
        for number, target in relative_links(path):
            if not target.exists():
                errors.append(f"{rel(path)}:{number}: broken link to {rel(target)}")

    mapped = {target for _, target in relative_links(DOCS_MAP)}
    for path in DOCS_DIR.rglob("*.md"):
        if path != DOCS_MAP and path.resolve() not in mapped:
            errors.append(f"{rel(path)}: not listed in {rel(DOCS_MAP)}")

    for error in errors:
        print(error)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
