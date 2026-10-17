import tempfile
import unittest
from datetime import date
from pathlib import Path

from check_docs import check


def run_check(files, today=None):
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        for name, text in files.items():
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_text(text, encoding="utf-8", newline="")
        return check(root, today)


def lesson(title, evidence="证据：#1\n", layer="哪一层没拦住：缺检查\n", seen="出现：2026-09-30\n"):
    return f"## {title}\n{evidence}{layer}{seen}\n"


class CheckDocsTest(unittest.TestCase):
    def test_valid_links_and_anchors_pass(self):
        errors, warnings = run_check({
            "README.md": "",
            "docs/a b.md": "# Background / 背景\n## Same\n## Same\n## `snake_case` **x**\n## Foo_bar_\n",
            "docs/README.md": (
                "# Map\n"
                "[rel](<a b.md>) [root](/README.md) [web](https://example.com) [mail](mailto:x@example.com)\n"
                "[a](a%20b.md#background--背景) [b](<a b.md#same-1>) [c](<a b.md#snake_case-x>) [d](<a b.md#foo_bar_>) [self](#map)\n"
            ),
        })
        self.assertEqual((errors, warnings), ([], []))

    def test_links_in_code_are_ignored(self):
        errors, _ = run_check({
            "docs/README.md": (
                "`[a](missing.md)` and ``[b](missing.md)``\n"
                "````\n```\n[c](missing.md)\n```\n[d](missing.md)\n````\n"
                "~~~\n# not a heading\n[e](missing.md#x)\n~~~\n"
            ),
        })
        self.assertEqual(errors, [])

    def test_broken_links_anchors_and_unmapped_docs_fail(self):
        errors, _ = run_check({
            "docs/README.md": "[inline](missing.md)\n[anchor](#nowhere)\n\n[ref]: gone.md\n",
            "docs/orphan.md": "# Orphan\n",
        })
        self.assertEqual(errors, [
            "docs/README.md:1: broken link to docs/missing.md",
            "docs/README.md:2: no heading for #nowhere in docs/README.md",
            "docs/README.md:4: broken link to docs/gone.md",
            "docs/orphan.md: not listed in docs/README.md",
        ])

    def test_budgets_only_warn_and_projects_can_move_them(self):
        files = {"AGENTS.md": "x\n" * 100, "CONTRIBUTING.md": "x\n" * 51, "docs/README.md": "x\n" * 301}
        self.assertEqual(run_check(files), ([], [
            "docs/README.md: 301 lines, over 300; split it by subtopic",
            "AGENTS.md + CONTRIBUTING.md: 151 lines, over 150; both load in every session",
        ]))
        config = '\ufeffsource = "' + "a" * 40 + '"\n[limits]\nalways_loaded_lines = 200 # why\n'
        self.assertEqual(run_check({**files, ".github/project-workflow.toml": config}), ([], [
            "docs/README.md: 301 lines, over 300; split it by subtopic",
        ]))

    def test_config_mistakes_fail_instead_of_falling_back(self):
        config = ".github/project-workflow.toml"
        cases = {
            "misspelled table": ("[limit]\ndoc_lines = 10\n", [f"{config}: unknown key limit; known: lanes, limits, merge, review, source"]),
            "lanes as a string": ('[lanes]\nlight = "docs/*"\n', [f"{config}: [lanes] takes only light and code, each a list of path patterns"]),
            "strict as a string": ('[merge]\nstrict = "false"\n', [f"{config}: [merge] takes only strict = true or false"]),
            "waiver as a string": ('[review]\nwaiver = "false"\n', [f"{config}: [review] takes only waiver = true or false"]),
            "unknown review key": ("[review]\nwaivers = false\n", [f"{config}: [review] takes only waiver = true or false"]),
            "waiver switched off": ("[review]\nwaiver = false\n", []),
            "bad values": ('source = "abc"\n[limits]\ndoc_lines = 0\ndoc_line = 4\n', [
                f"{config}: source must be a full 40-character commit hash",
                f"{config}: limit doc_lines must be a positive whole number, not 0",
                f"{config}: unknown limit doc_line; known: always_loaded_lines, doc_lines, lesson_days",
            ]),
            "not a table": ("limits = 5\n", [f"{config}: limits must be a table: [limits]"]),
        }
        for name, (text, expected) in cases.items():
            with self.subTest(name):
                self.assertEqual(run_check({config: text})[0], expected)
        errors, _ = run_check({config: "[limits\n"})
        self.assertTrue(errors[0].startswith(f"{config}: not valid UTF-8 TOML:"), errors)

    def test_lessons_without_their_evidence_fail(self):
        lessons = "上次复盘：2026-10-01\n\n" + "".join([
            lesson("fresh"),
            lesson("no evidence", evidence=""),
            lesson("no date", layer="哪一层没拦住:缺检查\n", seen="出现：上周\n"),
            lesson("impossible date", seen="出现：2026-02-30\n"),
            "```\n",  # a stray fence must not hide what follows
            lesson("after the fence", evidence=""),
        ])
        errors, warnings = run_check({"docs/README.md": "[l](lessons.md)\n", "docs/lessons.md": lessons}, today=date(2026, 10, 1))
        self.assertEqual(errors, [
            "docs/lessons.md:8: lesson 'no evidence' is missing 证据; 出现 lists dates as YYYY-MM-DD",
            "docs/lessons.md:12: lesson 'no date' is missing 出现; 出现 lists dates as YYYY-MM-DD",
            "docs/lessons.md:17: lesson 'impossible date' has an impossible date in 出现",
            "docs/lessons.md:23: lesson 'after the fence' is missing 证据; 出现 lists dates as YYYY-MM-DD",
        ])
        self.assertEqual(warnings, [])
        for first in ("", "上次复盘：2026-13-45\n"):
            with self.subTest(first=first):
                errors, _ = run_check({"docs/README.md": "[l](lessons.md)\n", "docs/lessons.md": first + lesson("x")})
                self.assertEqual(errors, ["docs/lessons.md:1: the first line must be 上次复盘：<YYYY-MM-DD>"])

    def test_lessons_due_to_leave_warn(self):
        lessons = "\ufeff上次复盘：2026-10-01\n\n" + "".join([
            lesson("stale", seen="出现：2026-08-01\n"),
            lesson("kept longer by the project", seen="出现：2026-08-01\n"),
            lesson("repeated", seen="出现：2026-09-20、2026-09-28\n"),
            lesson("same day twice", seen="出现：2026-09-30、2026-09-30\n"),
        ])
        files = {"docs/README.md": "[l](lessons.md)\n", "docs/lessons.md": lessons}
        self.assertEqual(run_check(files, today=date(2026, 10, 1)), ([], [
            "docs/lessons.md:3: lesson 'stale' not seen for 61 days, over 30; drop it",
            "docs/lessons.md:8: lesson 'kept longer by the project' not seen for 61 days, over 30; drop it",
            "docs/lessons.md:13: lesson 'repeated' happened 2 times; turn it into a check or a rule, then drop it",
        ]))
        files[".github/project-workflow.toml"] = "[limits]\nlesson_days = 90\n"
        self.assertEqual(run_check(files, today=date(2026, 10, 1))[1], [
            "docs/lessons.md:13: lesson 'repeated' happened 2 times; turn it into a check or a rule, then drop it",
        ])


if __name__ == "__main__":
    unittest.main()
