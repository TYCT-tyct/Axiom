import unittest

from check_modules import TABLE_HEADING, import_problems, read_table, table_problems

DOC = f"""# 总体架构

{TABLE_HEADING}

| 模块 | 可以引用 |
|---|---|
| `platform` | — |
| `catalog` | `platform` |
| `billing` | `catalog`、`platform` |

## 下一节

| `gateway` | `billing` |
"""


class ReadTable(unittest.TestCase):
    def test_reads_rows_until_the_next_heading(self):
        self.assertEqual(read_table(DOC), {"platform": set(), "catalog": {"platform"}, "billing": {"catalog", "platform"}})

    def test_missing_heading_fails(self):
        with self.assertRaises(SystemExit):
            read_table("# 总体架构\n")


class TableProblems(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("valid", {"a": {"b"}, "b": set()}, []),
            ("unknown target", {"a": {"zz"}}, ["a may import zz, which has no row"]),
            ("self edge", {"a": {"a"}}, ["a lists itself", "the table has a cycle: a -> a"]),
            ("cycle", {"a": {"b"}, "b": {"c"}, "c": {"a"}}, ["the table has a cycle: a -> b -> c -> a"]),
        ]
        for name, table, expected in cases:
            with self.subTest(name):
                self.assertEqual(table_problems(table), expected)


class ImportProblems(unittest.TestCase):
    TABLE = {"platform": set(), "catalog": {"platform"}, "billing": {"catalog", "platform"}}

    def test_cases(self):
        cases = [
            ("allowed edge", {"axiom/internal/billing": {"axiom/internal/catalog", "fmt"}}, []),
            ("own implementation", {"axiom/internal/billing": {"axiom/internal/billing/internal/ledger"}}, []),
            ("composition root imports anything", {"axiom/cmd/axiom": {"axiom/internal/billing"}}, []),
            ("every module may import platform", {"axiom/internal/catalog": {"axiom/internal/platform/ids"}}, []),
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
