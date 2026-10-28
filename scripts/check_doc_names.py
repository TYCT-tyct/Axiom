#!/usr/bin/env python3
"""Fail when a Markdown file names, in code formatting, a Go test or a Go declaration that does not exist.

Docs that cite a test as the evidence for a rule, or a function as where a rule lives, rot silently when the code is
renamed: three blocking review findings in one batch (#371, #375, #377) were exactly that. Two forms are checked, the
ones that cannot be anything else:
  - a test, benchmark, fuzz or example name (`TestX`, `TestX/sub`, `BenchmarkY`) must be a function in some Go file;
    a trailing * (`TestDiffHarness*`) matches any function with that prefix
  - `pkg.Name` with an exported Name, where pkg is the name of one of our Go packages, must be declared in that package
Everything else in backticks (SQL objects, planned designs, other languages' names) is left alone: a lowercase
`schema.table` may be a design not built yet, and telling it apart would take reading the prose.
usage: check_doc_names.py   (run from the repository root)
"""

import collections
import re
import subprocess
import sys

CODE = re.compile(r"`([^`\n]+)`")
TEST = re.compile(r"\b((?:Test|Benchmark|Fuzz|Example)[A-Z_]\w*)(\*?)")
QUALIFIED = re.compile(r"(\w+)\.([A-Z]\w*)(?:\(.*\))?")
PACKAGE = re.compile(r"^package (\w+)", re.M)
# Top-level declarations, and the names inside a var/const/type block, which gofmt indents by one tab.
DECLARED = re.compile(r"^func (?:\([^)]*\) )?(\w+)|^(?:type|var|const) (\w+)|^\t(\w+)\b", re.M)


def git_files(*patterns):
    return subprocess.run(["git", "ls-files", "-z", "--", *patterns], capture_output=True, text=True, check=True).stdout.split("\0")[:-1]


def declarations(sources):
    """{package name: declared names} from {path: Go source}; an external test package counts as its package."""
    found = collections.defaultdict(set)
    for text in sources.values():
        package = PACKAGE.search(text)
        if package:
            found[package.group(1).removesuffix("_test")].update(next(filter(None, m.groups())) for m in DECLARED.finditer(text))
    return found


def problems(docs, declared):
    """One line per unknown name, from {path: Markdown text} and the declarations."""
    every = set().union(*declared.values()) if declared else set()
    out = []
    for path, text in docs.items():
        for number, line in enumerate(text.splitlines(), 1):
            for code in CODE.findall(line):
                if not code.isascii():  # a placeholder such as 包路径.TestName
                    continue
                for name, star in TEST.findall(code):
                    if not (any(d.startswith(name) for d in every) if star else name in every):
                        out.append(f"{path}:{number}: `{code}` names test {name}{star}, which no Go file declares")
                qualified = QUALIFIED.fullmatch(code)
                if qualified and qualified.group(1) in declared and qualified.group(2) not in declared[qualified.group(1)]:
                    out.append(f"{path}:{number}: `{code}`: package {qualified.group(1)} declares no {qualified.group(2)}")
    return out


def main():
    sources = {path: open(path, encoding="utf-8").read() for path in git_files("*.go")}
    docs = {path: open(path, encoding="utf-8").read() for path in git_files("*.md")}
    found = problems(docs, declarations(sources))
    for problem in found:
        print(problem)
    if found:
        print("rename the reference to what the code declares now, or drop the code formatting for a name that is only planned")
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main())
