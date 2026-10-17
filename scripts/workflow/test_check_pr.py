import io
import os
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

import check_pr
from check_pr import describe, review, unfinished, waiver_allowed

TEMPLATE = "## Background / 背景\n<!-- why -->\n\n## Verification / 验证\n<!-- evidence -->\n"
BODY = "## Background / 背景\nbecause\n## Verification / 验证\nran it\n"
HEAD = "0123456789abcdef0123456789abcdef01234567"


def comment(first_line, association="OWNER"):
    return {"body": f"{first_line}\n\ndetails", "author_association": association}


def reviewed(blocking=0, should=0, sha=HEAD[:7]):
    return comment(f"独立评审 · 提交 {sha} · Sonnet 5.5 · 只读 · 阻断 {blocking} · 应修 {should} · 细节 1")


class DescribeTest(unittest.TestCase):
    def test_titles(self):
        cases = {
            "feat(skill): add a script": True,
            "chore!: drop the old layout": True,
            "fix: `manage.py` keeps uses lines": True,
            "fix(CI): any scope without spaces": True,
            "fix: a": True,
            "fix(two words): spaces in the scope": False,
            "Fix: capitalised": False,
            "fix: ends with a period.": False,
            "feature: unknown type": False,
            "docs: " + "x" * 70: False,
        }
        for title, ok in cases.items():
            with self.subTest(title):
                self.assertEqual(describe(title, BODY, "someone", TEMPLATE) == [], ok)

    def test_every_template_section_is_filled(self):
        self.assertEqual(describe("docs: x y", TEMPLATE, "someone", TEMPLATE), [
            "description: fill in the section ## Background / 背景",
            "description: fill in the section ## Verification / 验证",
        ])
        self.assertEqual(describe("docs: x y", "## Background / 背景\nbecause\n", "someone", TEMPLATE),
                         ["description: fill in the section ## Verification / 验证"])
        self.assertEqual(describe("Bump requests from 2.31.0 to 2.32.3", "Bumps requests.", "dependabot[bot]", TEMPLATE), [])

    def test_closing_keywords_only_start_a_line(self):
        cases = {
            "Closes #12. Adds the check.": True,
            "- fixes owner/repo#12": True,
            "> Resolved: #3": True,
            "1. closes #4": True,
            "Refs #12. It does not close #12.": False,
            "This Fixes: #3 as well": False,
            "No longer resolves owner/repo#7": False,
            "Closes https://github.com/owner/repo/issues/8": True,
            "This does not close https://github.com/owner/repo/issues/8": False,
            "`It does not` close #12.": False,
            "Code counts: `does not close #24`": False,
            "<!-- does not close #24 -->": False,
            "disclosed #5, unfixed #6, closes #N": True,
        }
        for text, ok in cases.items():
            with self.subTest(text):
                self.assertEqual(describe("docs: x y", BODY + text + "\n", "someone", TEMPLATE) == [], ok)


class ClosingTest(unittest.TestCase):
    def test_a_closed_issue_needs_its_acceptance_boxes_ticked(self):
        issues = {
            1: {"body": "### 要解决什么问题\n- [ ] not acceptance\n\n### 验收标准\n\n- [x] done\n- [ ] not yet\n- [ ] nor this\n"},
            2: {"body": "### 验收标准\r\n\r\n- [x] done\r\n"},
            3: {"body": "no acceptance section\n- [ ] a task\n"},
            4: {"body": "### 验收标准\n- [ ] a PR's own checklist\n", "pull_request": {}},
        }
        fetched = []

        def issue(number):
            fetched.append(number)
            return issues[number]
        body = ("Closes #1\nCloses #1\nFixes #2\nResolves #3\nCloses #4\nCloses other/repo#5\n"
                "Closes https://github.com/Owner/Repo/issues/1\n<!-- Closes #6 -->\nRefs #7\n")
        self.assertEqual(unfinished(body, "owner/repo", issue), [
            "#1 still has 2 unticked acceptance boxes: tick each with its evidence, or write Refs #1 instead; then edit this "
            "description or rerun the pr check"])
        self.assertEqual(fetched, [1, 2, 3, 4])  # each issue once; other repositories, comments and Refs are not fetched

    def test_an_unreadable_issue_fails_with_the_reason_and_keeps_the_other_problems(self):
        error = subprocess.CalledProcessError(1, ["gh"], stderr=b"HTTP 404: Not Found\n")
        env = {"PR_TITLE": "Bad title", "PR_BODY": BODY + "Closes #1\n", "PR_AUTHOR": "someone", "REPO": "owner/repo"}
        with (mock.patch.dict(os.environ, env), mock.patch.object(sys, "argv", ["check_pr.py", "describe"]),
              mock.patch("subprocess.run", side_effect=error), redirect_stdout(io.StringIO()) as out):
            self.assertEqual(check_pr.main(), 1)
        self.assertIn("title 'Bad title'", out.getvalue())
        self.assertIn("could not read an issue this PR closes: HTTP 404: Not Found", out.getvalue())


class ReviewTest(unittest.TestCase):
    def test_the_head_commit_needs_a_passing_answered_review(self):
        answer = comment(f"作者回应 · 提交 {HEAD[:7]} · 修改在 abc1234")
        cases = {
            "no review": ([], False),
            "review of another commit": ([reviewed(sha="fedcba9")], False),
            "blocking finding": ([reviewed(blocking=1), answer], False),
            "should-fix not answered": ([reviewed(should=2)], False),
            "answered before the review": ([answer, reviewed(should=2)], False),
            "should-fix answered": ([reviewed(should=2), answer], True),
            "clean review, full hash": ([reviewed(sha=HEAD)], True),
            "outsider's review": ([{**reviewed(), "author_association": "NONE"}], False),
            "re-review clears the findings": ([reviewed(should=2), reviewed()], True),
        }
        for name, (comments, ok) in cases.items():
            with self.subTest(name):
                self.assertEqual(review(HEAD, BODY, "someone", comments) == [], ok)

    def test_waivers(self):
        self.assertEqual(review(HEAD, BODY + "免评审：只改了措辞\n", "someone", []), [])
        self.assertEqual(review(HEAD, BODY, "dependabot[bot]", []), [])
        self.assertNotEqual(review(HEAD, BODY + "<!--\n免评审：模板里的提示\n-->\n", "someone", []), [])
        self.assertNotEqual(review(HEAD, "免评审：\n" + BODY, "someone", []), [])  # an empty reason does not borrow the next line
        self.assertNotEqual(review(HEAD, BODY + "免评审：只改了措辞\n", "someone", [reviewed(blocking=1)]), [])  # cannot overrule a review

    def test_a_project_can_refuse_waivers(self):
        waived = BODY + "免评审：只改了措辞\n"
        self.assertEqual(review(HEAD, waived, "someone", [], allow_waiver=False),
                         ["this project does not accept 免评审 ([review] waiver = false in .github/project-workflow.toml): get an independent review of 0123456"])
        self.assertEqual(review(HEAD, waived, "someone", [reviewed()], allow_waiver=False), [])  # a review still passes
        self.assertEqual(review(HEAD, waived, "dependabot[bot]", [], allow_waiver=False), [])
        cases = {"no config": (None, True), "switched off": ("[review]\nwaiver = false\n", False),
                 "limits only": ("[limits]\ndoc_lines = 400\n", True), "malformed, reported by check_docs": ("[review\n", True)}
        for name, (config, expected) in cases.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                if config is not None:
                    (root / ".github").mkdir()
                    (root / ".github/project-workflow.toml").write_text(config, encoding="utf-8")
                self.assertEqual(waiver_allowed(root), expected)


if __name__ == "__main__":
    unittest.main()
