import os
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path
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


class LightOnly(unittest.TestCase):
    def test_lane_decisions(self):
        upgrade = [".github/project-workflow.toml", "scripts/workflow/check_pr.py",
                   "scripts/workflow/test_check_pr.py"]
        cases = [(upgrade, True), ([], False), (["README.md"], True), (["notes.MD"], True),
                 (["scripts/check_scope.py", "scripts/test_ci_skips.py"], True)]
        cases += [(upgrade + [path], False) for path in (
            "internal/billing/ledger.go", "web/site/src/app.ts", "deploy/compose.yaml", "go.mod", "justfile",
            "scripts/web_cache.py", "scripts/ci/runner-host.sh", "scripts/ci/bench-compare.sh")]
        for paths, light in cases:
            with self.subTest(paths=paths), mock.patch.object(ci_skips, "changed_paths", return_value=paths):
                self.assertEqual(ci_skips.main(["light-only", "base"]), 0 if light else 1)
        for config in ('[lanes]\nlight = "scripts/*"', '[lanes]\nunknown = []', '[lanes]\nlight = [1]',
                       '[lanes]\nlight = [', '[lanes]\ncode = ["*.md"]'):
            with self.subTest(config=config), mock.patch.object(ci_skips, "changed_paths", return_value=["README.md"]), \
                    mock.patch("pathlib.Path.read_text", return_value=config):
                self.assertEqual(ci_skips.main(["light-only", "base"]), 1)
        for error in (OSError("git missing"), subprocess.CalledProcessError(1, ["git"])):
            with self.subTest(error=error), mock.patch.object(ci_skips, "changed_paths", side_effect=error):
                self.assertEqual(ci_skips.main(["light-only", "base"]), 1)

    def test_workflow_changes_outputs(self):
        workflow = (ci_skips.ROOT / ".github/workflows/ci.yml").read_text()
        decision = workflow.split("      - id: decide\n        run: |\n", 1)[1].split("\n\n", 1)[0]
        upgrade = [".github/project-workflow.toml", "scripts/workflow/check_pr.py",
                   "scripts/workflow/test_check_pr.py"]
        for event, base, paths, light in (
            ("workflow_dispatch", "base", upgrade, True), ("pull_request", "base", upgrade, True),
            ("push", "", upgrade, False), ("workflow_dispatch", "", upgrade, False),
            ("workflow_dispatch", "base", [], False),
            ("workflow_dispatch", "base", ["docs/architecture.md", "scripts/test_ci_skips.py"], True),
            ("workflow_dispatch", "base", ["internal/gateway/README.md", "scripts/test_ci_skips.py"], True),
            ("workflow_dispatch", "base", ["scripts/check_scope.py", "scripts/check_artifacts.py"], True),
            ("workflow_dispatch", "base", ["scripts/ci/bench-compare.sh"], False),
            ("workflow_dispatch", "base", upgrade + ["scripts/web_cache.py"], False),
        ):
            # Run the workflow's actual shell and CLI decisions with a controlled diff.
            cli = (f"import sys; sys.path.insert(0, {str(ci_skips.ROOT / 'scripts')!r}); import ci_skips; "
                   f"ci_skips.changed_paths = lambda *args: {paths!r}; sys.exit(ci_skips.main(sys.argv[1:]))")
            script = decision.replace("python3 scripts/ci_skips.py", shlex.join([sys.executable, "-c", cli]))
            script = script.replace("${{ github.event_name }}", event)
            with self.subTest(event=event, base=base, paths=paths), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "output"
                subprocess.run(["bash", "-e", "-c", script], check=True, capture_output=True, text=True,
                               env={**os.environ, "BASE": base, "GITHUB_OUTPUT": str(output)})
                result = dict(line.split("=", 1) for line in output.read_text().splitlines())
                self.assertEqual(result["light"], str(light).lower())
                if paths == ["scripts/ci/bench-compare.sh"]:
                    self.assertEqual(result["bench"], "true")
                if light or not base:
                    for job in ("deploy", "bench", "web", "vitals"):
                        self.assertEqual(result[job], str(not light).lower())


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


class NeedsGo(unittest.TestCase):
    def test_go_and_what_go_reads_run_it(self):
        for paths in ([], ["internal/billing/ledger.go"], ["go.mod"], ["api/console.yaml"], ["deploy/compose.yaml"],
                      ["justfile"], [".github/workflows/ci.yml"], ["scripts/ci/runner-host.sh"],
                      ["web/vite.shared.ts"], ["web/scripts/embed.ts"], ["web/site/src/a.ts", "cmd/axiom/main.go"]):
            with self.subTest(paths=paths):
                self.assertTrue(ci_skips.needs_go(paths))

    def test_frontend_and_markdown_skip_it(self):
        for paths in (["web/admin/src/nav.ts"], ["web/scripts/font-budget.ts", "docs/frontend.md"],
                      ["web/packages/boards/src/AxApp/regions/Keys.tsx"], ["README.md"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.needs_go(paths))


class NeedsE2E(unittest.TestCase):
    def test_frontend_and_its_serving_code_run_them(self):
        for paths in ([], ["web/site/src/a.ts"], ["api/admin.yaml"], ["internal/platform/webui/webui.go"],
                      [".github/workflows/ci.yml"], ["justfile"], ["scripts/ci/playwright.py"],
                      ["internal/platform/idem/testdata/same-request.json"]):
            with self.subTest(paths=paths):
                self.assertTrue(ci_skips.needs_e2e(paths))

    def test_other_go_skips_them(self):
        for paths in (["internal/billing/ledger.go"], ["cmd/axiom/admin_users.go", "internal/accounts/keys.go"],
                      ["docs/frontend.md"]):
            with self.subTest(paths=paths):
                self.assertFalse(ci_skips.needs_e2e(paths))


class NeedsVitals(unittest.TestCase):
    def test_page_inputs_run_the_timings(self):
        for path in ("web/site/src/app.ts", "web/pnpm-lock.yaml", "web/theme.js", "web/playwright.vitals.config.ts",
                     "web/packages/api/src/console.gen.ts", "web/packages/api/src/admin.gen.ts",
                     "web/e2e/vitals.spec.ts", "api/console.yaml", "api/admin.yaml"):
            with self.subTest(path=path):
                self.assertTrue(ci_skips.needs_vitals(["docs/a.md", path]))
        self.assertTrue(ci_skips.needs_vitals([]))

    def test_everything_else_skips_them(self):
        for path in ("internal/billing/ledger.go", "cmd/axiom/serve.go", "go.mod", "go.sum", "justfile",
                     "scripts/web_cache.py", "scripts/ci/web-container.sh", ".github/workflows/ci.yml",
                     "api/README.md", "website/src/app.ts", "web", "api"):
            with self.subTest(path=path):
                self.assertFalse(ci_skips.needs_vitals([path]))


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
                    self.assertEqual(ci_skips.main(["vitals-needed", "base"]), 0)
                    self.assertEqual(ci_skips.main(["md-only", "base"]), 1)

    def test_web_needed_codes(self):
        self.assertEqual(self.main("web-needed", ["internal/x/x.go"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("web-needed", ["api/console.yaml"]), 0)

    def test_vitals_needed_codes(self):
        self.assertEqual(self.main("vitals-needed", ["internal/x/x.go"]), ci_skips.NOT_NEEDED)
        self.assertEqual(self.main("vitals-needed", ["web/packages/api/src/console.gen.ts"]), 0)

    def test_md_only_codes(self):
        self.assertEqual(self.main("md-only", [".github/pull_request_template.md"]), 0)
        self.assertEqual(self.main("md-only", ["main.go"]), 1)


if __name__ == "__main__":
    unittest.main()
