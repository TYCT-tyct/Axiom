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


class NeedsWeb(unittest.TestCase):
    def test_frontend_and_build_inputs_run_it(self):
        for path in ("web/site/src/app.ts", "web/pnpm-lock.yaml", "web/.node-version", "api/console.yaml",
                     "api/admin.yaml", "scripts/web_cache.py", "scripts/ci/web-container.sh", "scripts/ci/runner-host.sh", "justfile",
                     ".github/workflows/ci.yml", ".github/actions/setup/action.yml", "go.mod", "go.sum"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_web(["docs/a.md", path]))
        self.assertTrue(ci_skips.needs_web([]))

    def test_unrelated_changes_skip_it(self):
        self.assertFalse(ci_skips.needs_web(["internal/billing/ledger.go", "docs/a.md", "scripts/ci_skips.py", "scripts/ci/bench-compare.sh"]))


class NeedsDeployCheck(unittest.TestCase):
    def test_anything_the_container_is_built_from_runs_it(self):
        for path in ("deploy/compose.yaml", "deploy/postgres/roles.sh", "Dockerfile.dev", "go.mod", "go.sum",
                     "cmd/axiom/serve.go", "cmd/axiom/healthcheck.go", "cmd/axiom/keysettings.go",
                     "internal/platform/config/config.go", "internal/billing/migrations/00001_x.sql",
                     "web/admin/vite.config.ts", "web/site/src/app.ts", "web/pnpm-lock.yaml", "justfile",
                     "scripts/web_cache.py", "scripts/ci/deploy-check.sh", "scripts/deploy_name.py", "tools/go.mod"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_deploy_check([path]))
        self.assertTrue(ci_skips.needs_deploy_check(["docs/a.md", "cmd/axiom/serve.go"]))
        self.assertTrue(ci_skips.needs_deploy_check([]))

    def test_only_clearly_irrelevant_files_skip_it(self):
        for paths in (["docs/a.md"], ["README.md", "AGENTS.md"], ["internal/x/x_test.go"], ["cmd/axiom/admin_test.go"],
                      ["internal/x/testdata/sample.sse"], [".github/workflows/ci.yml"],
                      ["scripts/test_ci_skips.py", "scripts/ci_skips.py", "scripts/train.py", "scripts/check_scope.py",
                       "scripts/workflow/check_docs.py", "scripts/ci/runner-host.sh", "scripts/ci/bench-compare.sh"],
                      ["web/e2e/admin-access.spec.ts", "web/playwright.config.ts", "web/journey/journey.spec.ts",
                       "web/playwright.journey.config.ts"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.needs_deploy_check(paths))


class ExitCodes(unittest.TestCase):
    def main(self, action, paths):
        with mock.patch.object(ci_skips, "changed_paths", return_value=paths):
            return ci_skips.main([action, "base"])

    def test_bench_needed_uses_a_distinct_code_for_not_needed(self):
        self.assertEqual(self.main("bench-needed", ["docs/a.md"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("bench-needed", ["main.go"]), 0)

    def test_deploy_needed_uses_the_same_code_for_not_needed(self):
        self.assertEqual(self.main("deploy-needed", ["docs/a.md"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("deploy-needed", ["deploy/compose.yaml"]), 0)

    def test_a_failed_diff_runs_every_check(self):
        for error in (OSError("git missing"), subprocess.CalledProcessError(1, ["git"])):
            with self.subTest(error=error):
                with mock.patch.object(ci_skips, "changed_paths", side_effect=error):
                    self.assertEqual(ci_skips.main(["bench-needed", "base"]), 0)
                    self.assertEqual(ci_skips.main(["deploy-needed", "base"]), 0)
                    self.assertEqual(ci_skips.main(["web-needed", "base"]), 0)
                    self.assertEqual(ci_skips.main(["md-only", "base"]), 1)

    def test_web_needed_codes(self):
        self.assertEqual(self.main("web-needed", ["internal/x/x.go"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("web-needed", ["api/console.yaml"]), 0)

    def test_md_only_codes(self):
        self.assertEqual(self.main("md-only", [".github/pull_request_template.md"]), 0)
        self.assertEqual(self.main("md-only", ["main.go"]), 1)


if __name__ == "__main__":
    unittest.main()
