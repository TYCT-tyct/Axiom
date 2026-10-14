import unittest
from pathlib import Path

from check_docs_impact import changed_files, explained, problems

TEMPLATE = (Path(__file__).resolve().parents[1] / ".github" / "pull_request_template.md").read_text(encoding="utf-8")
CODE = "internal/billing/internal/ledger/post.go"


def docs(text):
    return f"## Approach / 做法\n\n改了什么。\n\n## Docs / 文档影响\n\n{text}\n\n## Checklist / 检查\n\n- [x] CI 通过\n"


class Problems(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("domain doc updated", [CODE, "docs/domains/billing.md"], "", []),
            ("module README updated", [CODE, "internal/billing/README.md"], "", []),
            ("tests and testdata only", ["internal/billing/hold_test.go", "internal/billing/testdata/x.json"], "", []),
            ("code outside modules", ["cmd/axiom/main.go", "scripts/x.py"], "", []),
            ("no doc, explained", [CODE], docs("文档影响：无，因为只改了内部变量名。"), []),
            ("no doc, no explanation", [CODE], "", ["billing"]),
            ("the template's hint does not count", [CODE], TEMPLATE, ["billing"]),
            ("another module's doc does not count", [CODE, "docs/domains/routing.md"], "", ["billing"]),
            ("a move out of a module checks the old module too", [CODE, "internal/routing/post.go", "docs/domains/routing.md"],
             "", ["billing"]),
        ]
        for name, changed, body, modules in cases:
            with self.subTest(name):
                self.assertEqual([problem.split(":")[0] for problem in problems(changed, body)], modules)


class Explained(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("full-width punctuation", docs("文档影响：无，因为只改了内部变量名。"), True),
            ("half-width punctuation and spaces", docs("文档影响: 无, 因为 renamed a local variable"), True),
            ("reason on the next line", docs("文档影响：无，因为\n只改了内部变量名。"), True),
            ("placeholder ellipsis", docs("文档影响：无，因为……"), False),
            ("placeholder dots", docs("文档影响:无,因为..."), False),
            ("inside a closed comment", docs("<!-- 文档影响：无，因为只改了内部变量名。 -->"), False),
            ("inside an unclosed comment", docs("<!-- 文档影响：无，因为只改了内部变量名。"), False),
            ("outside the Docs section", "## Approach / 做法\n\n文档影响：无，因为只改了内部变量名。\n", False),
            ("CRLF line endings", docs("文档影响：无，因为只改了内部变量名。").replace("\n", "\r\n"), True),
            ("a quoted example of the format", docs("格式是 `文档影响：无，因为只改了内部变量名`。"), False),
            ("an angle-bracket placeholder", docs("文档影响：无，因为<reason>"), False),
            ("words in a later paragraph do not count", docs("文档影响：无，因为……\n\n关联 Issue #20"), False),
            ("a block quote", docs("> 文档影响：无，因为这是旧 PR 的示例。"), False),
            ("indented code", docs("    文档影响：无，因为这是旧 PR 的示例。"), False),
            ("a tilde fence", docs("~~~text\n文档影响：无，因为这是旧 PR 的示例。\n~~~"), False),
            ("a backtick fence", docs("```\n文档影响：无，因为这是旧 PR 的示例。\n```"), False),
            ("double-backtick inline code", docs("``文档影响：无，因为这是旧 PR 的示例。``"), False),
            ("a real declaration after a fenced example", docs("```\n文档影响：无，因为示例\n```\n\n文档影响：无，因为只改了内部变量名。"), True),
        ]
        for name, body, expected in cases:
            with self.subTest(name):
                self.assertEqual(explained(body), expected)


class ChangedFiles(unittest.TestCase):
    def test_nul_separated_paths_are_taken_verbatim(self):
        self.assertEqual(changed_files("internal/billing/计费.go\0docs/a b.md\0"), ["internal/billing/计费.go", "docs/a b.md"])


if __name__ == "__main__":
    unittest.main()
