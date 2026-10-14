#!/usr/bin/env python3
"""Fail when a pull request changes a module's code but neither that module's domain doc nor its README.

A pull request that really leaves the docs untouched says why in the Docs section of its description, on a line of its
own: 文档影响：无，因为<reason>, the reason in the same paragraph. Only plain paragraph text counts: HTML comments (an
unclosed one up to the end), fenced and inline code, block quotes, indented code and <placeholders> do not, so neither
the template's hint nor a quoted example of the format passes.
Reads PR_BODY from the environment and the changed files from git, with renames split into a deletion and an addition
so that the module code moves out of is checked too.
usage: check_docs_impact.py <base commit>   (run from the repository root)
"""

import os
import re
import subprocess
import sys

MODULE_CODE = re.compile(r"^internal/([a-z]+)/(?!(?:.*/)?testdata/|.*_test\.go$).+")
COMMENT = re.compile(r"<!--.*?(?:-->|\Z)", re.DOTALL)
DOCS_SECTION = re.compile(r"^## Docs / 文档影响[ \t]*$(.*?)(?=^## |\Z)", re.DOTALL | re.MULTILINE)
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
INLINE_CODE = re.compile(r"(`+).+?\1")
PLACEHOLDER = re.compile(r"<[^>\n]*>")
# At the start of the line, so a quoted (>) or indented (code) declaration does not match.
DECLARATION = re.compile(r"文档影响\s*[：:]\s*无\s*[，,]\s*因为(.*)")
REASON = re.compile(r"\w")  # a reason has words in it; "……" or "..." is the template's placeholder, not a reason


def prose_lines(text):
    """The lines of text outside fenced code blocks, with inline code removed."""
    fence = None
    for line in text.split("\n"):
        if fence:
            if re.match(rf"^ {{0,3}}{re.escape(fence[0])}{{{len(fence)},}}[ \t]*$", line):
                fence = None
            continue
        opening = FENCE.match(line)
        if opening:
            fence = opening.group(1)
            continue
        yield INLINE_CODE.sub("", line)


def explained(body):
    """Whether the Docs section of the description says why the docs did not change."""
    section = DOCS_SECTION.search(COMMENT.sub("", body.replace("\r\n", "\n")))
    lines = list(prose_lines(section.group(1))) if section else []
    for index, line in enumerate(lines):
        declared = DECLARATION.match(line)
        if not declared:
            continue
        paragraph = [declared.group(1)]
        for following in lines[index + 1:]:
            if not following.strip():
                break
            paragraph.append(following)
        if REASON.search(PLACEHOLDER.sub("", " ".join(paragraph))):
            return True
    return False


def problems(changed, body):
    """changed: changed paths; body: the PR description. Return the modules whose docs were left untouched."""
    if explained(body):
        return []
    modules = {found.group(1) for path in changed if (found := MODULE_CODE.match(path))}
    return [f"{module}: code changed, but neither docs/domains/{module}.md nor internal/{module}/README.md; "
            "update the doc, or write 文档影响：无，因为<reason> in the Docs section of the description"
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
