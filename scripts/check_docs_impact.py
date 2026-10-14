#!/usr/bin/env python3
"""Fail when a pull request changes a module's code but neither that module's domain doc nor its README.

A pull request that really leaves the docs untouched says why in the Docs section of its description:
文档影响：无，因为<reason>. HTML comments, such as the template's hints, are ignored, an unclosed one up to the end.
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
NO_IMPACT = re.compile(r"文档影响\s*[：:]\s*无\s*[，,]\s*因为(.*)", re.DOTALL)
REASON = re.compile(r"\w")  # a reason has words in it; "……" or "..." is the template's placeholder, not a reason


def explained(body):
    """Whether the Docs section of the description says why the docs did not change."""
    section = DOCS_SECTION.search(COMMENT.sub("", body))
    declared = section and NO_IMPACT.search(section.group(1))
    return bool(declared and REASON.search(declared.group(1)))


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
