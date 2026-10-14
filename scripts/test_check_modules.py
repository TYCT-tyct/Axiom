import tempfile
import unittest
from pathlib import Path

from check_modules import TABLE_HEADING, import_problems, imports_of, package_imports, read_table


def doc(*rows):
    return "\n".join([f"# 总体架构\n\n{TABLE_HEADING}\n\n| 模块 | 可以引用 | 为什么 |\n|---|---|---|", *rows, "\n## 下一节\n\n| `gateway` | `billing` | |\n"])


class ReadTable(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("rows up to the next heading; the first row is shared",
             doc("| `platform` | — | |", "| `catalog` | — | |", "| `billing` | `catalog` | |"),
             {"platform": set(), "catalog": {"platform"}, "billing": {"catalog", "platform"}}, []),
            ("a row lists a module below it", doc("| `platform` | — | |", "| `catalog` | `billing` | |", "| `billing` | — | |"),
             None, ["catalog may import billing, which is not a row above it"]),
            ("a row lists itself", doc("| `platform` | — | |", "| `catalog` | `catalog` | |"),
             None, ["catalog may import catalog, which is not a row above it"]),
            ("a second row cannot reopen a module",
             doc("| `platform` | — | |", "| `catalog` | — | |", "| `billing` | `catalog` | |", "| `catalog` | `billing` | |"),
             None, ["catalog has two rows"]),
            ("the first row is platform", doc("| `catalog` | — | |"), None, ["the first row must be platform, not catalog"]),
            ("no heading", "# 总体架构\n", None, [f"docs/architecture.md: no heading {TABLE_HEADING!r}"]),
        ]
        for name, text, table, problems in cases:
            with self.subTest(name):
                found_table, found_problems = read_table(text)
                self.assertEqual(found_problems, problems)
                if table is not None:
                    self.assertEqual(found_table, table)


class ImportsOf(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("single", 'package a\n\nimport "fmt"\n\nfunc f() {}\n', ["fmt"]),
            ("grouped with names and comments", 'package a\n\nimport (\n\t"fmt" // "not/this"\n\t_ "embed"\n\tx `raw/path`\n\t/* "nor/this" */ . "dot"\n)\n', ["fmt", "embed", "raw/path", "dot"]),
            ("several declarations and a build constraint", '//go:build race\n\n// Package a imports "nothing/here".\npackage a\n\nimport "a/b"\nimport y "c/d"\n\nvar s = "import \\"e/f\\""\n', ["a/b", "c/d"]),
            ("no imports", "package a\n\nconst x = 1\n", []),
        ]
        for name, source, expected in cases:
            with self.subTest(name):
                self.assertEqual(imports_of(source), expected)


class PackageImports(unittest.TestCase):
    def test_reads_files_whatever_their_build_constraints(self):
        with tempfile.TemporaryDirectory() as root:
            package = Path(root, "internal", "catalog")
            package.mkdir(parents=True)
            Path(package, "catalog.go").write_text('package catalog\n\nimport "fmt"\n', encoding="utf-8")
            Path(package, "race_test.go").write_text('//go:build race\n\npackage catalog\n\nimport "axiom/internal/billing"\n', encoding="utf-8")
            Path(package, "testdata").mkdir()
            Path(package, "testdata", "x.go").write_text('package x\n\nimport "axiom/internal/gateway"\n', encoding="utf-8")
            self.assertEqual(package_imports(root), {"axiom/internal/catalog": {"fmt", "axiom/internal/billing"}})


class ImportProblems(unittest.TestCase):
    TABLE = {"platform": set(), "catalog": {"platform"}, "billing": {"catalog", "platform"}}

    def test_cases(self):
        cases = [
            ("allowed edge", {"axiom/internal/billing": {"axiom/internal/catalog", "fmt"}}, []),
            ("own implementation", {"axiom/internal/billing": {"axiom/internal/billing/internal/ledger"}}, []),
            ("composition root imports anything", {"axiom/cmd/axiom": {"axiom/internal/billing"}}, []),
            ("platform imports a module", {"axiom/internal/platform/db": {"axiom/internal/catalog"}},
             ["axiom/internal/platform/db imports axiom/internal/catalog: platform may not import catalog"]),
            ("upward edge", {"axiom/internal/catalog/x": {"axiom/internal/billing"}},
             ["axiom/internal/catalog/x imports axiom/internal/billing: catalog may not import billing"]),
            ("a module reaches into cmd/", {"axiom/internal/catalog": {"axiom/cmd/bridge"}},
             ["axiom/internal/catalog imports axiom/cmd/bridge: only cmd/ may import packages outside internal/<module>/"]),
            ("module without a row", {"axiom/internal/gateway": set()},
             [f"axiom/internal/gateway: module gateway has no row in the table under {TABLE_HEADING!r}"]),
        ]
        for name, packages, expected in cases:
            with self.subTest(name):
                self.assertEqual(import_problems(self.TABLE, packages), expected)


if __name__ == "__main__":
    unittest.main()
