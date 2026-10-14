import unittest
from pathlib import Path

from check_docs_impact import problems

TEMPLATE = (Path(__file__).resolve().parents[1] / ".github" / "pull_request_template.md").read_text(encoding="utf-8")
CODE = "internal/billing/internal/ledger/post.go"


class Problems(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("domain doc updated", [CODE, "docs/domains/billing.md"], "", []),
            ("module README updated", [CODE, "internal/billing/README.md"], "", []),
            ("tests and testdata only", ["internal/billing/hold_test.go", "internal/billing/testdata/x.json"], "", []),
            ("code outside modules", ["cmd/axiom/main.go", "scripts/x.py"], "", []),
            ("no doc, explained", [CODE], "## Docs / 文档影响\n\n文档影响：无，因为只改了内部变量名。", []),
            ("no doc, no explanation", [CODE], "", ["billing"]),
            ("the template's hint does not count", [CODE], TEMPLATE, ["billing"]),
            ("another module's doc does not count", [CODE, "docs/domains/routing.md"], "", ["billing"]),
        ]
        for name, changed, body, modules in cases:
            with self.subTest(name):
                self.assertEqual([problem.split(":")[0] for problem in problems(changed, body)], modules)


if __name__ == "__main__":
    unittest.main()
