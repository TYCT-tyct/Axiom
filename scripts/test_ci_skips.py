import subprocess
import unittest
from unittest import mock

import ci_skips


class MarkdownOnly(unittest.TestCase):
    def test_markdown_paths_take_the_cheap_build_path(self):
        for paths in ([".github/pull_request_template.md"], ["docs/architecture.md"], ["README.md", "AGENTS.md"]):
            with self.subTest(paths=paths):
                self.assertTrue(ci_skips.all_md(paths))

    def test_other_files_take_the_full_build(self):
        for paths in ([], ["README.md", "main.go"], ["docs/a.md", "notes.txt"], ["web/a.md", "web/a.ts"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.all_md(paths))


class NeedsBenchmark(unittest.TestCase):
    def test_markdown_and_web_only_changes_skip_it(self):
        for paths in (["docs/unread.md"], ["docs/architecture.md"], [".github/pull_request_template.md"],
                      ["web/site/src/app.ts"], ["web/pnpm-lock.yaml"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.needs_benchmark(paths))

    def test_everything_else_runs_it(self):
        for path in ("main.go", "internal/x/x_test.go", "internal/x/testdata/sample.sse", "migrations/1.sql",
                     "api/console.yaml", "go.mod", "tools/go.sum"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_benchmark([path]))
        self.assertTrue(ci_skips.needs_benchmark(["web/a.ts", "docs/a.md"]))


class ExitCodes(unittest.TestCase):
    def main(self, action, paths):
        with mock.patch.object(ci_skips, "changed_paths", return_value=paths):
            return ci_skips.main([action, "base"])

    def test_bench_needed_uses_a_distinct_code_for_not_needed(self):
        self.assertEqual(self.main("bench-needed", ["docs/a.md"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("bench-needed", ["main.go"]), 0)

    def test_a_failed_diff_runs_both_checks(self):
        for error in (OSError("git missing"), subprocess.CalledProcessError(1, ["git"])):
            with self.subTest(error=error):
                with mock.patch.object(ci_skips, "changed_paths", side_effect=error):
                    self.assertEqual(ci_skips.main(["bench-needed", "base"]), 0)
                    self.assertEqual(ci_skips.main(["md-only", "base"]), 1)

    def test_md_only_codes(self):
        self.assertEqual(self.main("md-only", [".github/pull_request_template.md"]), 0)
        self.assertEqual(self.main("md-only", ["main.go"]), 1)


if __name__ == "__main__":
    unittest.main()
