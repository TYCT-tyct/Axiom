#!/usr/bin/env python3
"""Fail when Go code crosses a module boundary that docs/architecture.md does not allow.

The table under TABLE_HEADING in docs/architecture.md is the only statement of which module may import which. Rows go
from the bottom layer up: a row may list only modules in rows above it, so the table cannot have a cycle, and the first
row, platform, may be imported by every module without being listed. The check reads the imports of every .go file
under cmd/ and internal/ directly, whatever its build constraints, so code selected only by a tag or an OS is checked
too. Packages under cmd/ assemble the program and may import any module; nothing else may import them. Go's internal/
rule already stops one module from importing another's implementation. Run from the repository root.
"""

import re
import sys
from pathlib import Path

MODULE_PATH = "axiom"
TABLE_HEADING = "### 模块之间的依赖"
SHARED = "platform"
ROW = re.compile(r"^\|\s*`([a-z]+)`\s*\|([^|]*)\|")
NAME = re.compile(r"`([a-z]+)`")
# Comments, string literals, parentheses and words: enough to read a Go file's package clause and import declarations.
TOKEN = re.compile(r'//[^\n]*|/\*.*?\*/|"(?:\\.|[^"\\\n])*"|`[^`]*`|[()]|[^\s()"`/]+|/', re.DOTALL)


def read_table(text):
    """Return ({module: allowed modules}, problems) from the dependency table that follows TABLE_HEADING."""
    lines = text.splitlines()
    if TABLE_HEADING not in lines:
        return {}, [f"docs/architecture.md: no heading {TABLE_HEADING!r}"]
    table, problems = {}, []
    for line in lines[lines.index(TABLE_HEADING) + 1:]:
        if line.startswith("#"):
            break
        if not (row := ROW.match(line)):
            continue
        module, allowed = row.group(1), set(NAME.findall(row.group(2)))
        if not table and module != SHARED:
            problems.append(f"the first row must be {SHARED}, not {module}")
        if module in table:
            problems.append(f"{module} has two rows")
            continue
        problems += [f"{module} may import {name}, which is not a row above it" for name in sorted(allowed - table.keys())]
        table[module] = allowed | ({SHARED} if table else set())
    if not table:
        problems.append(f"docs/architecture.md: no module rows under {TABLE_HEADING!r}")
    return table, problems


def imports_of(source):
    """Return the import paths declared by a Go source file."""
    tokens = [t for t in TOKEN.findall(source) if not t.startswith(("//", "/*"))]
    paths, i = [], tokens.index("package") + 2 if "package" in tokens else len(tokens)
    while i < len(tokens) and tokens[i] in ("import", ";"):
        if tokens[i] == ";":
            i += 1
            continue
        group = tokens[i + 1] == "("
        end = tokens.index(")", i) if group else next(j for j in range(i + 1, len(tokens)) if tokens[j][0] in '"`')
        paths += [t[1:-1] for t in tokens[i + 1:end + 1] if t[0] in '"`']
        i = end + 1
    return paths


def package_imports(root):
    """Return {package path: imported paths} for every directory with .go files under cmd/ and internal/."""
    packages = {}
    for top in ("cmd", "internal"):
        for path in sorted(Path(root, top).rglob("*.go")):
            if "testdata" in path.parts:
                continue
            package = MODULE_PATH + "/" + path.parent.relative_to(root).as_posix()
            packages.setdefault(package, set()).update(imports_of(path.read_text(encoding="utf-8")))
    return packages


def module_of(package):
    """Return the module an axiom/internal package belongs to, or None for any other package."""
    parts = package.split("/")
    return parts[2] if len(parts) >= 3 and parts[0] == MODULE_PATH and parts[1] == "internal" else None


def import_problems(table, packages):
    """packages: {package path: imported package paths}. Return every import the table does not allow."""
    problems = []
    for package, imports in sorted(packages.items()):
        if package.startswith(f"{MODULE_PATH}/cmd/"):
            continue  # the composition root wires every module together
        module = module_of(package)
        if module not in table:
            problems.append(f"{package}: module {module} has no row in the table under {TABLE_HEADING!r}")
            continue
        for imported in sorted(imports):
            target = module_of(imported)
            if imported.startswith(MODULE_PATH + "/") and target is None:
                problems.append(f"{package} imports {imported}: only cmd/ may import packages outside internal/<module>/")
            elif target and target != module and target not in table[module]:
                problems.append(f"{package} imports {imported}: {module} may not import {target}")
    return problems


def main():
    table, problems = read_table(Path("docs/architecture.md").read_text(encoding="utf-8"))
    problems = problems or import_problems(table, package_imports("."))
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
