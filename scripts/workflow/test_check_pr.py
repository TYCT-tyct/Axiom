import unittest

from check_pr import describe, review

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


if __name__ == "__main__":
    unittest.main()
