#!/usr/bin/env python3
"""Fail when a pull request changes a module's code but neither that module's domain doc nor its README.

A pull request that really leaves the docs untouched says so in its description: 文档影响：无，因为…… (HTML comments,
such as the template's hints, are ignored). Reads PR_BODY from the environment and the changed files from git.
usage: check_docs_impact.py <base commit>   (run from the repository root)
"""

import os
import re
import subprocess
import sys

MODULE_CODE = re.compile(r"^internal/([a-z]+)/(?!(?:.*/)?testdata/|.*_test\.go$).+")
COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
NO_IMPACT = re.compile(r"文档影响[：:]\s*无[，,]\s*因为\S")


def problems(changed, body):
    """changed: changed paths; body: the PR description. Return the modules whose docs were left untouched."""
    if NO_IMPACT.search(COMMENT.sub("", body)):
        return []
    modules = {found.group(1) for path in changed if (found := MODULE_CODE.match(path))}
    return [f"{module}: code changed, but neither docs/domains/{module}.md nor internal/{module}/README.md; "
            "update the doc, or write 文档影响：无，因为…… in the description"
            for module in sorted(modules) if not {f"docs/domains/{module}.md", f"internal/{module}/README.md"} & set(changed)]


def main():
    changed = subprocess.run(["git", "diff", "--name-only", f"{sys.argv[1]}...HEAD"],
                             check=True, capture_output=True, text=True).stdout.split()
    found = problems(changed, os.environ.get("PR_BODY", ""))
    for problem in found:
        print(problem)
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
