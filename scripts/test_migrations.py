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


class BaselineMigrations(unittest.TestCase):
    def test_baseline_is_append_only_after_landing(self):
        baseline = "a/migrations/00001_baseline.sql"
        for after, fails in [({NEW: "SELECT 1;"}, False),
                             ({baseline: "SELECT 2;"}, True),
                             ({baseline: None}, True),
                             ({NEW: "SELECT 1;", "a/migrations/00002_duplicate.sql": "SELECT 2;"}, True)]:
            with self.subTest(after=after), justrepo.repository() as repo:
                justrepo.commit(repo, {baseline: "SELECT 1;"})
                justrepo.commit(repo, after)
                result = justrepo.just(repo, "migrations", "HEAD~1")
                self.assertEqual(result.returncode != 0, fails, result.stdout)

    def test_squash_requires_the_entire_frozen_history(self):
        from migration_order import is_squash
        history = {OLD: "old blob", "b/migrations/00009_later.sql": "later blob"}
        baselines = {"a/migrations/00001_baseline.sql": "a blob", "b/migrations/00001_baseline.sql": "b blob"}
        self.assertTrue(is_squash(history, history, baselines))
        for before, after in [(history | {NEW: "newly merged"}, baselines),
                              (history | {OLD: "changed"}, baselines),
                              (history, {"a/migrations/00001_baseline.sql": "a blob"}),
                              (baselines, baselines | {NEW: "new migration"})]:
            with self.subTest(before=before, after=after):
                self.assertFalse(is_squash(history, before, after))


if __name__ == "__main__":
    unittest.main()
