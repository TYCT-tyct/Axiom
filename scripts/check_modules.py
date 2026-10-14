#!/usr/bin/env python3
"""Fail when Go code crosses a module boundary that docs/architecture.md does not allow.

The table under TABLE_HEADING in docs/architecture.md is the only statement of which module may import which. Rows go
from the bottom layer up: a row may list only modules in rows above it, which keeps the table acyclic by construction,
and the first row (platform) may be imported by every module without being listed. Every import of every package,
tests included, is checked against the table. Go's internal/ rule already stops one module from importing another's
implementation, so only imports between the modules' public packages matter here. Run from the repository root.
"""

import re
import subprocess
import sys
from pathlib import Path

MODULE_PATH = "axiom"
TABLE_HEADING = "### 模块之间的依赖"
ROW = re.compile(r"^\|\s*`([a-z]+)`\s*\|([^|]*)\|")
NAME = re.compile(r"`([a-z]+)`")
COMPOSITION_ROOT = f"{MODULE_PATH}/cmd/"


def read_table(text):
    """Return ({module: allowed modules}, problems) from the dependency table that follows TABLE_HEADING."""
    lines = text.splitlines()
    if TABLE_HEADING not in lines:
        return {}, [f"docs/architecture.md: no heading {TABLE_HEADING!r}"]
    table, problems = {}, []
    for line in lines[lines.index(TABLE_HEADING) + 1:]:
        if line.startswith("#"):
            break
        row = ROW.match(line)
        if not row:
            continue
        module, allowed = row.group(1), set(NAME.findall(row.group(2)))
        problems += [f"{module} may import {name}, which is not a row above it" for name in sorted(allowed - table.keys())]
        table[module] = allowed | set(list(table)[:1])  # the first row is the shared base
    if not table:
        problems.append(f"docs/architecture.md: no module rows under {TABLE_HEADING!r}")
    return table, problems


def module_of(package):
    """Return the module an axiom package belongs to, or None for any other package."""
    parts = package.split("/")
    if len(parts) >= 3 and parts[0] == MODULE_PATH and parts[1] == "internal":
        return parts[2]
    return None


def import_problems(table, packages):
    """packages: {package path: imported package paths}. Return every import the table does not allow."""
    problems = []
    for package, imports in sorted(packages.items()):
        if package.startswith(COMPOSITION_ROOT):  # cmd/axiom wires every module together
            continue
        module = module_of(package)
        if module is None:
            problems.append(f"{package}: outside cmd/ and internal/<module>/; place it in a module or extend this check")
            continue
        if module not in table:
            problems.append(f"{package}: module {module} has no row in the table under {TABLE_HEADING!r}")
            continue
        for imported in sorted(imports):
            target = module_of(imported)
            if target and target != module and target not in table[module]:
                problems.append(f"{package} imports {imported}: {module} may not import {target}")
    return problems


def go_packages():
    listing = subprocess.run(
        ["go", "list", "-f", "{{.ImportPath}} {{join .Imports \" \"}} {{join .TestImports \" \"}} {{join .XTestImports \" \"}}", "./..."],
        check=True, capture_output=True, text=True,
    ).stdout
    packages = {}
    for line in listing.splitlines():
        package, *imports = line.split()
        packages[package] = set(imports)
    return packages


def main():
    table, problems = read_table(Path("docs/architecture.md").read_text(encoding="utf-8"))
    problems = problems or import_problems(table, go_packages())
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
