import unittest

from check_modules import TABLE_HEADING, import_problems, read_table


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
            ("no heading", "# 总体架构\n", None, [f"docs/architecture.md: no heading {TABLE_HEADING!r}"]),
        ]
        for name, text, table, problems in cases:
            with self.subTest(name):
                found_table, found_problems = read_table(text)
                self.assertEqual(found_problems, problems)
                if table is not None:
                    self.assertEqual(found_table, table)


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
            ("module without a row", {"axiom/internal/gateway": set()},
             [f"axiom/internal/gateway: module gateway has no row in the table under {TABLE_HEADING!r}"]),
            ("package outside the layout", {"axiom/tools": set()},
             ["axiom/tools: outside cmd/ and internal/<module>/; place it in a module or extend this check"]),
        ]
        for name, packages, expected in cases:
            with self.subTest(name):
                self.assertEqual(import_problems(self.TABLE, packages), expected)


if __name__ == "__main__":
    unittest.main()
