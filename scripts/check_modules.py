#!/usr/bin/env python3
"""Fail when Go code crosses a module boundary that docs/architecture.md does not allow.

The table under the heading TABLE_HEADING in docs/architecture.md is the only statement of which module may import
which; this script reads it, checks it has no cycle, and checks every import of every package (tests included)
against it. Go's internal/ rule already stops one module from importing another's implementation, so only imports
between the modules' public packages are checked here. Run from the repository root; needs Python 3.11+ and go.
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
SHARED = "platform"  # every module may import it; the table says so once instead of on every row


def read_table(text):
    """Return {module: allowed modules} from the dependency table that follows TABLE_HEADING."""
    lines = text.splitlines()
    try:
        start = lines.index(TABLE_HEADING)
    except ValueError:
        raise SystemExit(f"docs/architecture.md: no heading {TABLE_HEADING!r}") from None
    table = {}
    for line in lines[start + 1:]:
        if line.startswith("#"):
            break
        row = ROW.match(line)
        if row:
            table[row.group(1)] = set(NAME.findall(row.group(2)))
    if not table:
        raise SystemExit(f"docs/architecture.md: no module rows under {TABLE_HEADING!r}")
    return table


def table_problems(table):
    problems = [f"{module} may import {name}, which has no row" for module, allowed in table.items() for name in sorted(allowed - table.keys())]
    problems += [f"{module} lists itself" for module, allowed in table.items() if module in allowed]
    cycle = find_cycle(table)
    if cycle:
        problems.append("the table has a cycle: " + " -> ".join(cycle))
    return problems


def find_cycle(table):
    state = {}  # module -> "open" while on the current path, "done" after

    def visit(module, path):
        state[module] = "open"
        for name in sorted(table.get(module, ())):
            if state.get(name) == "open":
                return path[path.index(name):] + [name]
            if name not in state:
                found = visit(name, path + [name])
                if found:
                    return found
        state[module] = "done"
        return None

    for module in sorted(table):
        if module not in state:
            found = visit(module, [module])
            if found:
                return found
    return None


def module_of(package):
    """Return the module a package belongs to, "" for the composition root, or None for packages outside the layout."""
    if package.startswith(COMPOSITION_ROOT):
        return ""
    parts = package.split("/")
    if len(parts) >= 3 and parts[0] == MODULE_PATH and parts[1] == "internal":
        return parts[2]
    return None


def import_problems(table, packages):
    """packages: {package path: imported package paths}. Return every import the table does not allow."""
    problems = []
    for package, imports in sorted(packages.items()):
        module = module_of(package)
        if module is None:
            problems.append(f"{package}: outside cmd/ and internal/<module>/; place it in a module or extend this check")
            continue
        if module and module not in table:
            problems.append(f"{package}: module {module} has no row in the table under {TABLE_HEADING!r}")
            continue
        for imported in sorted(imports):
            target = module_of(imported) if imported.startswith(MODULE_PATH + "/") else None
            if target and module and target not in (module, SHARED) and target not in table.get(module, ()):
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
        packages[package] = set(imports) - {package}
    return packages


def main():
    table = read_table(Path("docs/architecture.md").read_text(encoding="utf-8"))
    problems = table_problems(table) or import_problems(table, go_packages())
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
