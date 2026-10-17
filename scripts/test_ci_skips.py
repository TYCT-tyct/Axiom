import unittest

import ci_skips


class OnlyMarkdown(unittest.TestCase):
    def test_only_markdown(self):
        self.assertTrue(ci_skips.only_md(["docs/a.md", "README.md", "internal/x/README.md"]))

    def test_other_files_run_the_build(self):
        for paths in ([], ["README.md", "main.go"], ["docs/a.md", "notes.txt"], ["web/a.md", "web/a.ts"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.only_md(paths))


class NeedsBenchmark(unittest.TestCase):
    def test_production_go_and_module_files_need_it(self):
        for path in ("main.go", "internal/protocol/relay.go", "go.mod", "go.sum", "tools/go.mod", "tools/go.sum"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_benchmark(["docs/a.md", path]))

    def test_tests_docs_and_frontend_do_not(self):
        for path in ("internal/protocol/relay_test.go", "internal/protocol/relay_meter_test.go", "docs/a.md",
                     "web/site/src/app.ts", "README.md"):
            with self.subTest(path=path):
                self.assertFalse(ci_skips.needs_benchmark([path]))


if __name__ == "__main__":
    unittest.main()
