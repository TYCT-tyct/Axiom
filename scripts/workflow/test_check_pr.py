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
from check_pr import change, closing, describe, review, unfinished, unticked_acceptance, waiver_allowed

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


RENDERED = {  # body_html as GitHub renders each description: gh api -X POST markdown -f mode=gfm, verbatim
    "the issue's example": (
        '\n<h3>验收标准</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> unfinished</li>\n</ul>'
    ),
    "a marker in an inline code span": (
        '<h3>验收</h3>\n<p>Explain <code class="notranslate">&lt;!--</code> here.</p>'
        '\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "a marker in an indented code block": (
        '<h3>验收</h3>\n<pre class="notranslate"><code class="notranslate">```\n&lt;!--\n```\n</code></pre>'
        '\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "a comment inside a heading": (
        '<h3> 验收标准</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "several comments on one heading": (
        '<h3> 验收标准</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "a tilde fence after a comment": (
        '<h3>验收</h3>\n<p>~~~</p>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "an unmatched backtick before a comment": (
        '<h3>Other ` </h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> unrelated</li>\n</ul>'
    ),
    "a box inside a comment": (
        '<h3>验收</h3>\n\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> b</li>\n</ul>'
    ),
    "a comment marker inside a fence": (
        '<h3>验收</h3>\n<div class="highlight highlight-text-md"><pre class="notranslate"><span class="pl-c">'
        '<span class="pl-c">&lt;!--</span></span>\n<span class="pl-c">- [ ] in code</span></pre></div>'
        '\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> b</li>\n</ul>'
    ),
    "a heading and a box inside a fence": (
        '<h3>验收</h3>\n<div class="highlight highlight-text-md"><pre class="notranslate"><span class="pl-mh">'
        '### <span class="pl-en">验收标准</span></span>\n<span class="pl-v">-</span> <span class="pl-k">[ ]</span>'
        ' in code</pre></div>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> real</li>\n</ul>'
    ),
    "subheadings and a checked box": (
        '<h3>验收标准</h3>\n<h4>功能</h4>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> a</li>\n</ul>\n<h4>其他</h4>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Completed task"'
        ' checked=""> b</li>\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> c</li>\n</ul>\n<h3>下一步</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> not acceptance</li>\n</ul>'
    ),
    "a second acceptance section": (
        '<h3>验收标准</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Completed task"'
        ' checked=""> a</li>\n</ul>\n<h3>其他</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> no</li>\n</ul>\n<h3>验收标准（补充）</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> b</li>\n</ul>\n<ol class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> c</li>\n</ol>'
    ),
    "no acceptance": (
        '<h3>要解决什么问题</h3>\n<ul class="contains-task-list">\n<li class="task-list-item">'
        '<input type="checkbox" id="" disabled="" class="task-list-item-checkbox" aria-label="Incomplete task'
        '"> a task</li>\n</ul>'
    ),
}


class ClosingTest(unittest.TestCase):
    def test_unticked_boxes_in_the_rendered_description(self):
        cases = {
            "the issue's example": 1,
            "a marker in an inline code span": 1,
            "a marker in an indented code block": 1,
            "a comment inside a heading": 1,
            "several comments on one heading": 1,
            "a tilde fence after a comment": 1,
            "an unmatched backtick before a comment": 0,
            "a box inside a comment": 1,
            "a comment marker inside a fence": 1,
            "a heading and a box inside a fence": 1,
            "subheadings and a checked box": 2,
            "a second acceptance section": 2,
            "no acceptance": 0,
        }
        for name, count in cases.items():
            with self.subTest(name):
                self.assertEqual(unticked_acceptance(RENDERED[name]), count)

    def test_a_closed_issue_needs_its_acceptance_boxes_ticked(self):
        issues = {
            ("owner/repo", 1): {"body_html": RENDERED["subheadings and a checked box"]},
            ("owner/repo", 2): {"body_html": RENDERED["an unmatched backtick before a comment"]},
            ("owner/repo", 3): {"body_html": RENDERED["no acceptance"]},
            ("other/repo", 4): {"body_html": RENDERED["the issue's example"]},  # in another repository, or linked in the sidebar
        }
        self.assertEqual(unfinished(list(issues), lambda repo, number: issues[repo, number]), [
            "owner/repo#1 still has 2 unticked acceptance boxes: tick each with its evidence, or stop closing it (write "
            "Refs owner/repo#1, or unlink it in the PR's sidebar); then rerun the pr check",
            "other/repo#4 still has 1 unticked acceptance boxes: tick each with its evidence, or stop closing it (write "
            "Refs other/repo#4, or unlink it in the PR's sidebar); then rerun the pr check"])

    def test_an_issue_read_without_rendered_html_is_a_problem(self):
        self.assertEqual(unfinished([("owner/repo", 1)], lambda repo, number: {"body_html": None}), [])  # no description
        self.assertEqual(unfinished([("owner/repo", 1)], lambda repo, number: {}),
                         ["could not read owner/repo#1 as GitHub renders it: no body_html in the reply"])

    def test_the_closing_references_reply(self):
        def reply(nodes, more=False):
            return {"data": {"repository": {"pullRequest": {"closingIssuesReferences": {"pageInfo": {"hasNextPage": more}, "nodes": nodes}}}}}
        issue = {"number": 1, "repository": {"nameWithOwner": "owner/repo"}}
        self.assertEqual(closing(reply([])), ([], []))  # the usual case: the PR closes nothing
        self.assertEqual(closing(reply([issue])), ([("owner/repo", 1)], []))
        self.assertEqual(closing(reply([issue], more=True)),
                         ([("owner/repo", 1)], ["this PR closes more than 100 issues; the check reads only the first 100"]))

    def test_the_issues_come_from_github_and_a_failed_request_fails(self):
        graphql = b'{"data":{"repository":{"pullRequest":{"closingIssuesReferences":{"pageInfo":{"hasNextPage":false},"nodes":[{"number":1,"repository":{"nameWithOwner":"owner/repo"}}]}}}}}'
        replies = [subprocess.CompletedProcess([], 0, graphql), subprocess.CompletedProcess([], 0, b'{}')]
        env = {"PR_TITLE": "Bad title", "PR_BODY": BODY, "PR_AUTHOR": "someone", "REPO": "owner/repo", "NUMBER": "9"}
        with (mock.patch.dict(os.environ, env), mock.patch.object(sys, "argv", ["check_pr.py", "describe"]),
              mock.patch("subprocess.run", side_effect=replies) as run, redirect_stdout(io.StringIO()) as out):
            self.assertEqual(check_pr.main(), 1)
        query, read = (call.args[0] for call in run.call_args_list)
        self.assertEqual(query[:3], ["gh", "api", "graphql"])
        self.assertTrue({"owner=owner", "name=repo", "number=9"} <= set(query))
        self.assertIn("Accept: application/vnd.github.html+json", read)  # the reply must carry body_html
        self.assertEqual(read[-1], "repos/owner/repo/issues/1")
        self.assertIn("could not read owner/repo#1 as GitHub renders it", out.getvalue())
        error = subprocess.CalledProcessError(1, ["gh"], stderr=b"HTTP 502\n")
        with (mock.patch.dict(os.environ, env), mock.patch.object(sys, "argv", ["check_pr.py", "describe"]),
              mock.patch("subprocess.run", side_effect=error), redirect_stdout(io.StringIO()) as out):
            self.assertEqual(check_pr.main(), 1)
        self.assertIn("title 'Bad title'", out.getvalue())  # the other problems are kept
        self.assertIn("could not read the issues this PR closes: HTTP 502", out.getvalue())


class ReviewTest(unittest.TestCase):
    def test_a_missing_review_says_how_to_write_one(self):
        # AGENTS.md only points at this check, so its message alone must yield a first line the check accepts
        problem, = review("abcdef1234", "", "someone", [])
        line = problem.split("first line is ")[1].split(" (")[0].replace("<model>", "m").replace("<n>", "0")
        self.assertEqual(review("abcdef1234", "", "someone", [{"body": line, "author_association": "OWNER"}]), [])

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

    def test_a_review_holds_while_later_commits_only_merge_the_base_in(self):
        same = lambda commit: commit == "fedcba9"
        older = [reviewed(should=1, sha="fedcba9"), comment("作者回应 · 提交 fedcba9")]
        self.assertEqual(review(HEAD, BODY, "someone", older, same_change=same), [])
        self.assertNotEqual(review(HEAD, BODY, "someone", older[:1], same_change=same), [])  # its should-fix still needs the answer
        self.assertNotEqual(review(HEAD, BODY, "someone", [reviewed(blocking=1, sha="fedcba9")], same_change=same), [])
        self.assertNotEqual(review(HEAD, BODY, "someone", [reviewed(sha="1234567")], same_change=same), [])  # another change

    def test_change_ignores_base_merges_but_not_the_prs_own_lines(self):
        with tempfile.TemporaryDirectory() as tmp:
            git = lambda *args: subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
                                                "-c", "core.autocrlf=false", "-C", tmp, *args],
                                               capture_output=True, check=True, text=True).stdout.strip()
            def commit(files, message):
                for name, content in files.items():
                    Path(tmp, name).write_bytes(content)
                git("add", "-A")
                git("commit", "-q", "-m", message)
                return git("rev-parse", "HEAD")
            far = b"\n\n\n\n"
            git("init", "-q", "-b", "main")
            commit({"a.py": b"import os" + far + b"x = 1\n", "b.py": b"foo\n" + far + far + b"foo\n", "c.bin": b"\0aaaa"}, "base")
            git("checkout", "-q", "-b", "pr")
            reviewed_commit = commit({"a.py": b"import os" + far + b"x = 2\n", "b.py": b"bar\n" + far + far + b"foo\n",
                                      "c.bin": b"\0bbbb"}, "the PR")
            git("checkout", "-q", "main")
            commit({"a.py": b"import os\nimport sys" + far + b"x = 1\n"}, "main moves on")  # shifts the PR's line
            git("checkout", "-q", "pr")
            git("merge", "-q", "--no-edit", "main")
            merged = git("rev-parse", "HEAD")
            later = {  # each edit after the merge, made on its own from the merged commit
                "whitespace": {"a.py": b"import os\nimport sys" + far + b"x  = 2\n"},
                "line ending": {"a.py": b"import os\nimport sys" + far + b"x = 2\r\n"},
                "the same edit moved": {"b.py": b"foo\n" + far + far + b"bar\n"},
                "binary content": {"c.bin": b"\0cccc"},  # same size, so only --binary tells them apart
            }
            edited = {}
            for name, files in later.items():
                git("checkout", "-q", merged)
                edited[name] = commit(files, name)
            cwd = os.getcwd()
            os.chdir(tmp)  # the review job runs git in the checkout
            try:
                mine = change("main", reviewed_commit)
                self.assertIsNotNone(mine)
                self.assertEqual(change("main", merged), mine)  # merging the base in keeps the change
                for name, sha in edited.items():
                    with self.subTest(name):
                        self.assertNotEqual(change("main", sha), mine)
                self.assertIsNone(change("main", "0" * 40))
            finally:
                os.chdir(cwd)

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
