import unittest

import ci_skips


def unreferenced(path):
    return False


class OnlyMarkdown(unittest.TestCase):
    def test_unreferenced_markdown_skips_the_build(self):
        self.assertTrue(ci_skips.only_md(["docs/unread.md", "README.md"], unreferenced))

    def test_markdown_read_by_code_runs_the_build(self):
        # tools/checkmodules and internal/platform/pg read this path; it is a code-check input.
        self.assertFalse(ci_skips.only_md(["docs/architecture.md"]))
        self.assertTrue(ci_skips.referenced_by_code("docs/architecture.md"))
        self.assertFalse(ci_skips.only_md(["docs/architecture.md"], lambda path: True))

    def test_other_files_run_the_build(self):
        for paths in ([], ["README.md", "main.go"], ["docs/a.md", "notes.txt"], ["web/a.md", "web/a.ts"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.only_md(paths, unreferenced))


class NeedsBenchmark(unittest.TestCase):
    def test_markdown_and_web_only_changes_skip_it(self):
        for paths in (["docs/unread.md"], ["README.md"], ["web/site/src/app.ts"], ["web/pnpm-lock.yaml"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.needs_benchmark(paths, unreferenced))

    def test_everything_else_runs_it(self):
        for path in ("main.go", "internal/x/x_test.go", "internal/x/testdata/sample.sse", "migrations/1.sql",
                     "api/console.yaml", "go.mod", "tools/go.sum"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_benchmark([path], unreferenced))
        self.assertTrue(ci_skips.needs_benchmark(["web/a.ts", "docs/a.md"], unreferenced))

    def test_architecture_document_is_not_markdown_only(self):
        self.assertTrue(ci_skips.needs_benchmark(["docs/architecture.md"]))


if __name__ == "__main__":
    unittest.main()
