#!/usr/bin/env python3
"""Fail when a pull request changes a module's code but neither that module's domain doc nor its README.

Markdown documents, generated Go, tests and testdata do not count as module code.
A pull request that really leaves the docs untouched says why as the first line of the Docs section of its
description: 文档影响：无，因为<reason>, the reason on the same line. The rule is about a line, not about Markdown, so
nothing has to be parsed beyond it: HTML comments (an unclosed one up to the end) and fenced code blocks are removed
first, and a first line that is quoted, indented, code or a <placeholder> does not start with the declaration or has no
reason, so it does not pass.
Reads PR_BODY from the environment and the changed files from git, with renames split into a deletion and an addition
so that the module code moves out of is checked too.
usage: check_docs_impact.py <base commit>   (run from the repository root)
"""

import os
import re
import subprocess
import sys

MODULE_CODE = re.compile(r"^internal/([a-z]+)/(?!(?:.*/)?testdata/|.*_test\.go$|.*\.gen\.go$|.*\.md$).+")
COMMENT = re.compile(r"<!--.*?(?:-->|\Z)", re.DOTALL)
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
DOCS_HEADING = "## Docs / 文档影响"
DECLARATION = re.compile(r"文档影响\s*[：:]\s*无\s*[，,]\s*因为(.*)")
NOT_A_REASON = re.compile(r"`+[^`]*`+|<[^>]*>")  # code and <placeholders> on the line
REASON = re.compile(r"\w")  # a reason has words in it; "……" or "..." is the template's placeholder, not a reason


def outside_fences(lines):
    """The lines that are not inside a fenced code block."""
    fence = None
    for line in lines:
        if fence:
            if re.match(rf"^ {{0,3}}{re.escape(fence[0])}{{{len(fence)},}}[ \t]*$", line):
                fence = None
        elif opening := FENCE.match(line):
            fence = opening.group(1)
        else:
            yield line


def explained(body):
    """Whether the first line of the Docs section declares, with a reason, that the docs did not change."""
    lines = list(outside_fences(COMMENT.sub("", body.replace("\r\n", "\n")).split("\n")))
    headings = [index for index, line in enumerate(lines) if line.rstrip() == DOCS_HEADING]
    if not headings:
        return False
    first = next((line for line in lines[headings[-1] + 1:] if line.strip()), "")
    declared = DECLARATION.match(first)
    return bool(declared and REASON.search(NOT_A_REASON.sub("", declared.group(1))))


def problems(changed, body):
    """changed: changed paths; body: the PR description. Return the modules whose docs were left untouched."""
    if explained(body):
        return []
    modules = {found.group(1) for path in changed if (found := MODULE_CODE.match(path))}
    return [f"{module}: code changed, but neither docs/domains/{module}.md nor internal/{module}/README.md; "
            "update the doc, or start the Docs section of the description with 文档影响：无，因为<reason>"
            for module in sorted(modules) if not {f"docs/domains/{module}.md", f"internal/{module}/README.md"} & set(changed)]


def changed_files(git_output):
    """Paths from `git diff --name-only -z`: NUL-separated and never quoted."""
    return [path for path in git_output.split("\0") if path]


def main():
    diff = subprocess.run(["git", "diff", "--name-only", "--no-renames", "-z", f"{sys.argv[1]}...HEAD"],
                          check=True, stdout=subprocess.PIPE, text=True, encoding="utf-8")
    found = problems(changed_files(diff.stdout), os.environ.get("PR_BODY", ""))
    for problem in found:
        print(problem)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
