import io
import os
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import changes
from changes import code_changed, lanes


class ChangesTest(unittest.TestCase):
    def test_only_docs_and_managed_files_skip_the_slow_jobs(self):
        cases = {
            "docs": (["README.md", "docs/domains/billing.md"], (), (), False),
            "an upgrade": (["AGENTS.md", ".github/workflows/check-pr.yml", "scripts/workflow/check_pr.py"], (), (), False),
            "the upgrade that retires the workflows from before the modules": (
                [".github/workflows/workflow.yml", ".github/workflows/workflow-pr.yml", ".github/workflows/check-docs.yml"], (), (), False),
            "code": (["docs/a.md", "internal/billing/quote.go"], (), (), True),
            "a prompt the code embeds": (["internal/prompts/system.md"], (), (), True),
            "the project's own scripts beside the managed ones": (["scripts/workflow/deploy.py"], (), (), True),
            "the project's own CI": ([".github/workflows/ci.yml"], (), (), True),
            "empty diff": ([], (), (), True),
            "a light pattern the project adds": (["LICENSE"], ("LICENSE",), (), False),
            "docs the project marks as code": (["docs/api/openapi.md"], (), ("docs/api/*",), True),
        }
        for name, (paths, light, code, expected) in cases.items():
            with self.subTest(name):
                self.assertEqual(code_changed(paths, light, code), expected)

    def test_lanes_come_from_the_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.assertEqual(lanes(root), ((), ()))  # no config
            (root / ".github").mkdir()
            config = root / ".github/project-workflow.toml"
            config.write_text('[lanes]\nlight = ["LICENSE"]\ncode = ["docs/api/*"]\n', encoding="utf-8")
            self.assertEqual(lanes(root), (("LICENSE",), ("docs/api/*",)))
            for malformed in ('[lanes]\nlight = "docs/*"\n', 'lanes = "docs/*"\n', '[lanes]\nlite = ["x"]\n'):
                config.write_text(malformed, encoding="utf-8")  # main() turns this into code=true
                with self.subTest(malformed), self.assertRaises(ValueError):
                    lanes(root)

    def test_a_rename_and_any_failure_run_the_slow_jobs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            git = lambda *args: subprocess.run(["git", "-C", tmp, "-c", "user.name=t", "-c", "user.email=t@t", *args],
                                               check=True, capture_output=True)
            git("init", "-q", "-b", "main")
            (root / "quote.go").write_text("package billing\n" * 20, encoding="utf-8")
            git("add", "."), git("commit", "-q", "-m", "a")
            git("update-ref", "refs/remotes/origin/main", "HEAD")
            (root / "docs").mkdir()
            git("mv", "quote.go", "docs/quote.md"), git("commit", "-q", "-m", "move code into docs")
            cases = {"code moved into docs": ({"BASE_REF": "main"}, "code=true"),
                     "a push": ({"BASE_REF": ""}, "code=true"),
                     "an unknown base": ({"BASE_REF": "nope"}, "code=true")}
            cwd = os.getcwd()
            os.chdir(tmp)  # the script diffs the repository it runs in
            try:
                for name, (env, expected) in cases.items():
                    with (self.subTest(name), mock.patch.dict(os.environ, env), mock.patch.object(changes, "ROOT", root),
                          redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO())):
                        changes.main()
                        self.assertEqual(out.getvalue().strip(), expected)
            finally:
                os.chdir(cwd)


if __name__ == "__main__":
    unittest.main()
