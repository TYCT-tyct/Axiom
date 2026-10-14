import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

JUSTFILE = Path(__file__).resolve().parents[1] / "justfile"
GIT = ["git", "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "core.autocrlf=false"]


def scope(files, commit):
    """Run the real `just scope` in a fresh repository holding files ({path: content}), committed or left untracked."""
    with tempfile.TemporaryDirectory() as repo:
        subprocess.run([*GIT, "init", "--quiet", repo], check=True)
        for path, content in files.items():
            (Path(repo) / path).parent.mkdir(parents=True, exist_ok=True)
            (Path(repo) / path).write_text(content, encoding="utf-8", newline="\n")
        if commit:
            subprocess.run([*GIT, "-C", repo, "add", "."], check=True)
            subprocess.run([*GIT, "-C", repo, "commit", "--quiet", "-m", "plant"], check=True)
        return subprocess.run([shutil.which("just"), "--justfile", str(JUSTFILE), "--working-directory", repo, "scope"],
                              stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8")


class Scope(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("escaped quotes around the dot in Go", "internal/plant/plant.go",
             'package plant\n\nconst q = "SET LOCAL \\"axiom\\" . \\"scope_platform\\" = \'on\'"\n', False, True),
            ("a line break inside the name in SQL, in another case", "internal/plant/plant.sql",
             "SET LOCAL axiom\n. SCOPE_Sites = '1';\n", False, True),
            ("set_config in any case", "cmd/plant/main.go", 'package main\n\nconst q = "SELECT Set_Config($1, $2, true)"\n',
             False, True),
            ("a committed file", "internal/plant/plant.sql", "SET LOCAL axiom.scope_accounts = '1';\n", True, True),
            ("the pg package may", "internal/platform/pg/scope.sql",
             "SELECT set_config('axiom.scope_accounts', '1', true);\n", False, False),
            ("other file types are not code", "docs/plant.md", "axiom.scope_platform\n", False, False),
        ]
        for name, path, content, commit, fails in cases:
            with self.subTest(name):
                result = scope({path: content}, commit)
                self.assertEqual(result.returncode != 0, fails, result.stdout)
                if fails:
                    self.assertIn(path, result.stdout)


if __name__ == "__main__":
    unittest.main()
