import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from check_doc_size import problems

SCRIPT = Path(__file__).with_name("check_doc_size.py")


class DocSize(unittest.TestCase):
    def test_character_caps(self):
        for path, limit in (("docs/domains/billing.md", 30000), ("internal/billing/README.md", 12000),
                            ("docs/architecture.md", 12000)):
            with self.subTest(path=path):
                self.assertEqual(problems(path, "中" * limit, ""), [])
                self.assertEqual(problems(path, "中" * (limit + 3), ""),
                                 [f"{path}: {limit + 3} characters exceeds cap {limit} by 3 characters"])

    def test_only_new_domain_history_is_rejected(self):
        for line in ("日期2026-11-16确认", "「负责人说先做检查」", "参考 new-api/model/a.go:123",
                     "参考 sub2api/internal/a.go:42"):
            with self.subTest(line=line):
                self.assertEqual(problems("docs/domains/billing.md", line + "\n规则\n", line + "\n"), [])
                found = problems("docs/domains/billing.md", "规则\n" + line, "规则\n")
                self.assertEqual(len(found), 1)
                self.assertTrue(found[0].startswith("docs/domains/billing.md:2:"))
                for path in ("docs/archive/billing.md", "docs/security.md", "internal/billing/README.md"):
                    self.assertEqual(problems(path, line, ""), [])
        self.assertEqual(problems("docs/archive/billing.md", "中" * 40000, ""), [])
        self.assertEqual(problems("docs/domains/billing.md", "参考 new-api/model/a.go；负责人决定规则。", ""), [])

    def test_cli_checks_committed_staged_unstaged_and_untracked_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")

            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, env=env, stderr=subprocess.DEVNULL).decode().strip()

            def write(path, text):
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(text, encoding="utf-8")

            def check():
                return subprocess.run([sys.executable, str(SCRIPT)], cwd=root, env=env, capture_output=True, text=True)

            git("init")
            write("docs/domains/test.md", "旧日期 2020-01-01\n规则\n")
            git("add", ".")
            git("commit", "-m", "baseline")
            env["BASE"] = git("rev-parse", "HEAD")
            write("docs/archive/test.md", "中" * 40000 + "2026-11-16")
            git("add", ".")
            git("commit", "-m", "archive")
            self.assertEqual(check().returncode, 0)
            for state in ("untracked", "staged", "committed", "unstaged"):
                write("docs/domains/new.md", "规则\nnew-api/a.go:123\n" + "中" * 30000)
                if state in ("staged", "committed"):
                    git("add", ".")
                if state == "committed":
                    git("commit", "-m", "new history")
                if state == "unstaged":
                    write("docs/domains/new.md", "规则\nsub2api/b.go:42\n" + "中" * 30000)
                with self.subTest(state=state):
                    result = check()
                    self.assertEqual(result.returncode, 1, result.stderr)
                    self.assertIn("docs/domains/new.md:2:", result.stdout)
                    self.assertIn("exceeds cap 30000 by", result.stdout)
            env["BASE"] = git("rev-parse", "HEAD")
            self.assertEqual(check().returncode, 1)  # main's first-parent diff

    def test_a_pull_request_merge_is_checked_against_the_current_base_not_a_stale_one(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = dict(os.environ, GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.com",
                       GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.com")

            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root, env=env, stderr=subprocess.DEVNULL).decode().strip()

            def write(path, text):
                (root / path).parent.mkdir(parents=True, exist_ok=True)
                (root / path).write_text(text, encoding="utf-8")

            git("init", "-b", "main")
            write("docs/domains/test.md", "规则\n")
            git("add", ".")
            git("commit", "-m", "base")
            env["BASE"] = git("rev-parse", "HEAD")  # the base as of the PR's last push
            git("checkout", "-q", "-b", "pr")
            write("web/a.txt", "x\n")
            git("add", ".")
            git("commit", "-m", "pr")
            git("checkout", "-q", "main")
            write("docs/domains/test.md", "规则\n日期 2026-11-16\n")  # main moved on, history and all
            git("add", ".")
            git("commit", "-m", "main moves on")
            git("merge", "-q", "--no-ff", "pr", "-m", "merge pr into the current main")  # what CI checks out
            result = subprocess.run([sys.executable, str(SCRIPT)], cwd=root, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout)
            git("checkout", "-q", "pr")
            write("docs/domains/test.md", "规则\n参考 new-api/a.go:1\n")  # the PR's own history still fails
            git("commit", "-qam", "pr adds history")
            git("checkout", "-q", "main")
            git("merge", "-q", "--no-ff", "-X", "theirs", "pr", "-m", "merge again")
            result = subprocess.run([sys.executable, str(SCRIPT)], cwd=root, env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stdout)
            self.assertIn("new-api/a.go:1", result.stdout)


if __name__ == "__main__":
    unittest.main()
