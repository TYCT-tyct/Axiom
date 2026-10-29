import unittest

import justrepo

OLD = "a/migrations/00001_old.sql"
NEW = "a/migrations/00002_new.sql"


def migrations(after, base="HEAD~1"):
    """Run the real `just migrations` on a repository whose base commit holds one migration and whose head applies
    `after` ({path: content, None to delete})."""
    with justrepo.repository() as repo:
        justrepo.commit(repo, {OLD: "CREATE TABLE a.old ();\n", "README.md": "x\n"})
        justrepo.commit(repo, after)
        return justrepo.just(repo, "migrations", base)


class Migrations(unittest.TestCase):
    def test_cases(self):
        cases = [
            ("out of order below a previously applied version", {"a/migrations/00000_late.sql": "SELECT 1;\n"}, "HEAD~1", True),
            ("duplicate version", {"a/migrations/00001_duplicate.sql": "SELECT 1;\n"}, "HEAD~1", True),
            ("independent module", {"b/migrations/00001_first.sql": "SELECT 1;\n"}, "HEAD~1", False),
            ("a migration added", {NEW: "CREATE TABLE a.new ();\n"}, "HEAD~1", False),
            ("other files changed", {"README.md": "y\n", "a/other.sql": "SELECT 1;\n"}, "HEAD~1", False),
            ("a migration changed", {OLD: "CREATE TABLE a.old (x int);\n"}, "HEAD~1", True),
            ("a migration deleted", {OLD: None}, "HEAD~1", True),
            ("a migration renamed", {OLD: None, NEW: "CREATE TABLE a.old ();\n"}, "HEAD~1", True),
            ("a base that does not exist", {NEW: "CREATE TABLE a.new ();\n"}, "no-such-base", True),
        ]
        for name, after, base, fails in cases:
            with self.subTest(name):
                result = migrations(after, base)
                self.assertEqual(result.returncode != 0, fails, result.stdout)

    def test_a_base_without_history_in_common_fails(self):
        """A shallow clone, or a base from another history, has no merge base: the check cannot tell, so it fails."""
        with justrepo.repository() as repo:
            justrepo.commit(repo, {OLD: "CREATE TABLE a.old ();\n"})
            justrepo.git(repo, "tag", "first")
            justrepo.git(repo, "checkout", "--quiet", "--orphan", "other")
            justrepo.commit(repo, {NEW: "CREATE TABLE a.new ();\n"})
            result = justrepo.just(repo, "migrations", "first")
            self.assertNotEqual(result.returncode, 0, result.stdout)


if __name__ == "__main__":
    unittest.main()
