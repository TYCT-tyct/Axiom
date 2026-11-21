import json
import contextlib
import importlib.util
import io
import os
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

from test_runner_host import BASH


ROOT = Path(__file__).parents[1]
WORKFLOW = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
JUSTFILE = (ROOT / "justfile").read_text(encoding="utf-8")


def ci_module(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / "scripts/ci" / f"{name}.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


browser = ci_module("playwright")
reports = ci_module("reports")
comparison = ci_module("runner-comparison")
train_result = ci_module("train_result")


def job(name):
    return re.search(rf"^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)", WORKFLOW, re.M | re.S).group(1)


def enabled(name, event, stress=False, draft=False, docs=False, vitals=True, light=False, boards=False, web=True, reuse=(),
            go=True, e2e=True):
    folded = re.search(r"^    if: >-\n((?:      .+\n)+)", job(name), re.M)
    if folded:
        condition = " ".join(line.strip() for line in folded.group(1).splitlines())
    else:
        condition = re.search(r"^    if: (.+)$", job(name), re.M).group(1)
    return expression(condition, event, stress, draft, docs, vitals=vitals, light=light, boards=boards, web=web, reuse=reuse,
                      go=go, e2e=e2e)


def expression(condition, event="pull_request", stress=False, draft=False, docs=False, results=None, base="", vitals=True, light=False, release=False, boards=False, web=True, reuse=(), go=True, e2e=True):
    results = results or {}
    values = {
        "always()": True,
        "github.event_name": event,
        "github.event.pull_request.draft": draft,
        "inputs.gate_stress": stress,
        "inputs.base": base,
        "inputs.release": release,
        "needs.changes.result": results.get("changes", "success"),
        "needs.changes.outputs.reuse": json.dumps(list(reuse)),
        "needs.changes.outputs.light": "true" if docs or light else "false",
        "needs.changes.outputs.web": "false" if light or not web else "true",
        "needs.changes.outputs.deploy": "false" if light else "true",
        "needs.changes.outputs.bench": "false" if light else "true",
        "needs.changes.outputs.vitals": "true" if vitals else "false",
        "needs.changes.outputs.boards": "true" if boards else "false",
        "needs.changes.outputs.go": "false" if docs or light or not go else "true",
        "needs.changes.outputs.e2e": "false" if docs or light or not e2e else "true",
    }
    for name, result in results.items():
        values["needs." + name + ".result"] = result
    condition = condition.replace("needs.*.result", repr(list(results.values())))
    for key, value in values.items():
        condition = condition.replace(key, repr(value))
    condition = condition.replace("&&", " and ").replace("||", " or ")
    condition = re.sub(r"!(?!=)", " not ", condition)
    return eval(condition.strip(), {"__builtins__": {}}, {
        "contains": lambda values, value: value in values,
        "fromJSON": json.loads,
    })


class BuildChecks(unittest.TestCase):
    def test_every_job_has_a_wall_clock_limit(self):
        for name in re.findall(r"^  ([\w-]+):$", WORKFLOW.split("jobs:\n", 1)[1], re.M):
            self.assertRegex(job(name), r"(?m)^    timeout-minutes: [1-9]\d*$", name)

    def test_self_hosted_evidence_stays_on_disk_and_fallback_uploads(self):
        for name in ("boards", "e2e", "vitals", "journey"):
            steps = job(name)
            self.assertIn("if: failure() && runner.environment == 'github-hosted'", steps)
            self.assertIn("if: always() && runner.environment == 'self-hosted'", steps)
            self.assertIn("web/playwright-report/", steps)
            self.assertIn("python3 scripts/ci/reports.py prepare", steps)
            self.assertIn("python3 scripts/ci/reports.py collect", steps)
            self.assertLess(steps.index("web-container.sh cleanup"), steps.index("reports.py collect"))
        self.assertIn("scripts/web_cache.py vitals", JUSTFILE)
        self.assertIn("shard: [1, 2, 3, 4]", job("boards"))
        self.assertIn('"$E2E_SHARD_INDEX/$E2E_SHARD_COUNT"', job("boards"))
    def test_dispatch_runner_override_is_uniform_and_does_not_change_normal_pools(self):
        for name in re.findall(r"^  ([\w-]+):$", WORKFLOW.split("jobs:\n", 1)[1], re.M):
            labels = re.search(r"runs-on: \$\{\{ fromJSON\((.+)\) \}\}", job(name)).group(1)
            self.assertTrue(labels.startswith("inputs.runs_on || "), name)
            for override, base, expected in (("", "", "light" if name in ("changes", "build", "gate-stress") else "heavy"),
                                             ("", "base", "train"), ('"ubuntu-latest"', "", "ubuntu-latest"),
                                             ('"ubuntu-latest"', "base", "ubuntu-latest")):
                value = labels
                for key, replacement in {"inputs.runs_on": override, "inputs.base": base,
                                         "vars.RUNS_ON_TRAIN": '"train"', "vars.RUNS_ON_HEAVY": '"heavy"',
                                         "vars.RUNS_ON": '"light"'}.items():
                    value = value.replace(key, repr(replacement))
                self.assertEqual(json.loads(eval(value.replace("||", " or ").replace("&&", " and "))), expected)

    def test_normal_events_publish_the_literal_build_check(self):
        self.assertRegex(job("build"), r"(?m)^    name: build$")
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertTrue(enabled("build", event, docs=docs))
                    self.assertFalse(enabled("gate-stress", event, docs=docs))
        self.assertFalse(enabled("build", "pull_request", draft=True))

    def test_diagnostic_cannot_pass_or_skip_the_required_build(self):
        self.assertTrue(enabled("gate-stress", "workflow_dispatch", stress=True))
        for name in ("web", "e2e"):
            self.assertTrue(enabled(name, "workflow_dispatch", stress=True, docs=True))
        self.assertTrue(enabled("build", "workflow_dispatch", stress=True))
        # A skipped required check is accepted by GitHub; the diagnostic must explicitly fail it instead.
        self.assertRegex(job("build"), r"(?s)- if: inputs.gate_stress\n        run: \|\n.*?\n          exit 1")
        self.assertFalse(enabled("gate-stress", "pull_request", stress=True))

    def test_web_and_shards_run_independently_on_all_build_events(self):
        needs = re.search(r"^    needs: \[(.+)\]$", job("build"), re.M).group(1).split(", ")
        self.assertEqual(set(needs), {"changes", "static", "go-test", "web", "e2e", "boards", "vitals", "journey", "bench", "deploy", "signing"})
        for name in ("web", "e2e"):
            self.assertRegex(job(name), r"(?m)^    needs: changes$")
            self.assertIn("bash scripts/ci/web-container.sh", job(name))
            self.assertRegex(job(name), r"- if: always\(\)\n        run: bash scripts/ci/web-container.sh cleanup")
            for event in ("pull_request", "push", "workflow_dispatch"):
                for docs in (False, True):
                    with self.subTest(job=name, event=event, docs=docs):
                        # a pull request whose frontend inputs changed runs the e2e too, so a broken page is caught
                        # before its train; the train and main run them again
                        self.assertEqual(enabled(name, event, docs=docs), not docs)
        # a pull request or a train that changes neither the frontend nor the Go code serving it skips the e2e (the
        # e2e lane, scripts/ci_skips.py); main has no base, selects every lane and runs them
        self.assertFalse(enabled("e2e", "pull_request", e2e=False))
        self.assertFalse(enabled("e2e", "workflow_dispatch", e2e=False))
        self.assertTrue(enabled("e2e", "push"))

    def test_vitals_run_on_main_and_by_page_inputs_on_trains(self):
        self.assertRegex(job("vitals"), r"(?m)^    needs: changes$")
        self.assertIn("bash scripts/ci/web-container.sh", job("vitals"))
        self.assertRegex(job("vitals"), r"- if: always\(\)\n        run: bash scripts/ci/web-container.sh cleanup")
        for event in ("pull_request", "push", "workflow_dispatch"):
            for vitals in (False, True):
                with self.subTest(event=event, vitals=vitals):
                    # changes decides by path on PRs and trains and always says true on main; Markdown-only says false
                    self.assertEqual(enabled("vitals", event, vitals=vitals), vitals and event != "pull_request")

    def test_build_requires_vitals_exactly_when_they_are_scheduled(self):
        # A skipped job counts as passed on GitHub, so the aggregator must require the timing job whenever changes said
        # it was due, and only then.
        check = re.search(r"- if: (needs.changes.outputs.vitals .+)\n        run: (.+)", job("build"))
        self.assertIsNotNone(check)
        self.assertTrue(expression(check[1], event="workflow_dispatch", vitals=True))
        self.assertFalse(expression(check[1], event="pull_request", vitals=True))
        self.assertFalse(expression(check[1], vitals=False))
        for result in ("success", "failure", "cancelled", "skipped"):
            with self.subTest(result=result):
                command = check[2].replace("${{ needs.vitals.result }}", result)
                self.assertEqual(subprocess.run(["bash", "-c", command]).returncode == 0, result == "success")

    def test_boards_run_separately_when_selected_and_build_requires_success(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for selected in (False, True):
                self.assertEqual(enabled("boards", event, boards=selected), selected)
                self.assertFalse(enabled("boards", event, boards=selected, stress=True))
        self.assertIn("scripts/ci/playwright.py boards", job("boards"))
        self.assertNotIn("just web-e2e", job("boards"))
        self.assertIn("name: boards-test-results", job("boards"))
        self.assertRegex(job("boards"), r"- if: always\(\)\n        run: bash scripts/ci/web-container.sh cleanup")
        check = re.search(r"- if: (needs.changes.outputs.boards .+)\n        run: (.+)", job("build"))
        self.assertTrue(expression(check[1], boards=True))
        self.assertFalse(expression(check[1], boards=False))
        for result in ("success", "failure", "cancelled", "skipped"):
            command = check[2].replace("${{ needs.boards.result }}", result)
            self.assertEqual(subprocess.run(["bash", "-c", command]).returncode == 0, result == "success")
        regular = (ROOT / "web/playwright.config.ts").read_text()
        dedicated = (ROOT / "web/playwright.boards.config.ts").read_text()
        self.assertIn('testIgnore: ["vitals.spec.ts", "boards*.spec.ts"]', regular)
        self.assertIn('testMatch: "boards*.spec.ts"', dedicated)
        self.assertIn('webServer: undefined', dedicated)

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_boards_run_on_main_and_by_inputs_on_prs(self):
        decide = textwrap.dedent(re.search(
            r"- id: decide\n        run: \|\n(.*?)(?=^  #)", WORKFLOW, re.M | re.S)[1])
        stub = ("python3() { return 0; }\n"
                "git() { printf '%s\\n' \"$*\" > \"$DIFF_ARGS\"; return \"$DIFF_STATUS\"; }\n")
        for path in ("web/scripts/boards*", "web/packages/boards/**", "web/e2e/boards*.spec.ts",
                     ":(glob)web/*", "web/scripts/fonts/sources.json", "scripts/web_cache.py", "scripts/ci/**"):
            self.assertIn("'" + path + "'", decide)
        for event in ("pull_request", "workflow_dispatch", "push"):
            for status in (0, 1, 128):
                with self.subTest(event=event, status=status), tempfile.TemporaryDirectory() as tmp:
                    output, args = Path(tmp) / "output", Path(tmp) / "args"
                    env = {**os.environ, "BASE": "" if event == "push" else "base", "DIFF_STATUS": str(status),
                           "GITHUB_OUTPUT": output.as_posix(), "DIFF_ARGS": args.as_posix()}
                    script = stub + decide.replace("${{ github.event_name }}", event)
                    subprocess.run([BASH, "-euo", "pipefail", "-c", script], env=env, capture_output=True, check=True)
                    values = dict(line.split("=", 1) for line in output.read_text().splitlines())
                    # A train leaves the boards to main; a PR runs them when their inputs changed or the diff failed
                    expected = event == "push" or event == "pull_request" and status != 0
                    self.assertEqual(values["boards"], str(expected).lower())
                    if event == "push":
                        self.assertFalse(args.exists())
                    else:
                        self.assertTrue(args.read_text().startswith("diff --quiet base...HEAD -- "))

    def test_unrelated_pr_skips_web_and_build_passes(self):
        condition = re.search(r"^    if: (.+)$", job("web"), re.M).group(1)
        condition = condition.replace("needs.changes.outputs.web", "'false'")
        self.assertFalse(expression(condition))
        self.assertTrue(enabled("build", "pull_request"))
        guard = re.search(r"- if: (contains\(needs.\*.result.+)\n", job("build"))[1]
        self.assertFalse(expression(guard, results={"web": "skipped", "static": "success", "changes": "success"}))
        decide = re.search(r"- id: decide\n        run: \|\n(.*?)(?=^  #)", WORKFLOW, re.M | re.S)[1]
        self.assertIn('if [ "${{ github.event_name }}" = pull_request ] && [ -n "$BASE" ]; then', decide)
        self.assertIn('web=true', decide)

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_web_selection_runs_only_for_prs_and_fails_open(self):
        decide = textwrap.dedent(re.search(
            r"- id: decide\n        run: \|\n(.*?)(?=^  #)", WORKFLOW, re.M | re.S)[1])
        stub = "python3() { case \"$2\" in md-only|light-only) return 1;; web-needed) return \"$WEB_STATUS\";; *) return 0;; esac; }\n"
        stub += 'git() { return "$CI_DIFF_STATUS"; }\n'
        for event in ("pull_request", "push", "workflow_dispatch"):
            for status, diff in ((0, 0), (10, 0), (37, 0), (10, 1), (10, 128)):
                with self.subTest(event=event, status=status, diff=diff), tempfile.TemporaryDirectory() as tmp:
                    output = Path(tmp) / "output"
                    env = {**os.environ, "BASE": "" if event == "push" else "base", "WEB_STATUS": str(status), "CI_DIFF_STATUS": str(diff),
                           "GITHUB_OUTPUT": output.as_posix()}
                    script = stub + decide.replace("${{ github.event_name }}", event)
                    subprocess.run([BASH, "-euo", "pipefail", "-c", script], env=env, capture_output=True, check=True)
                    values = dict(line.split("=", 1) for line in output.read_text().splitlines())
                    self.assertEqual(values["web"], "false" if event == "pull_request" and status == 10 and diff == 0 else "true")

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_vitals_run_on_main_alone(self):
        decide = textwrap.dedent(re.search(
            r"- id: decide\n        run: \|\n(.*?)(?=^  #)", WORKFLOW, re.M | re.S)[1])
        stub = ("python3() { case \"$2\" in\n"
                "  light-only|md-only) return \"$MD_STATUS\";;\n"
                "  *) return 0;;\n"
                "esac; }\n")
        stub += 'git() { [ "$VITALS_STATUS" = 10 ]; }\n'
        cases = {
            "main on a push": ("push", "", "1", "10", "true"),
            "a PR touching the pages": ("pull_request", "base", "1", "0", "false"),
            "a train touching the pages": ("workflow_dispatch", "base", "1", "0", "false"),
            "Markdown-only": ("workflow_dispatch", "base", "0", "10", "false"),
        }
        for name, (event, base, md, vitals, expected) in cases.items():
            with self.subTest(name), tempfile.TemporaryDirectory() as tmp:
                output = Path(tmp) / "output"
                env = {**os.environ, "BASE": base, "MD_STATUS": md, "VITALS_STATUS": vitals,
                       "GITHUB_OUTPUT": output.as_posix()}
                script = stub + decide.replace("${{ github.event_name }}", event)
                subprocess.run([BASH, "-euo", "pipefail", "-c", script], env=env, capture_output=True, check=True)
                values = dict(line.split("=", 1) for line in output.read_text().splitlines())
                self.assertEqual(values["vitals"], expected)
                self.assertEqual(values["light"], "true" if md == "0" else "false")

    def test_go_tests_run_wherever_the_go_lane_is_selected(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertEqual(enabled("go-test", event, docs=docs), not docs)
        # a web/-only pull request or train leaves the Go packages as main has them
        for event in ("pull_request", "workflow_dispatch"):
            with self.subTest(event=event, go=False):
                self.assertFalse(enabled("go-test", event, go=False))

    def test_build_requires_go_tests_exactly_when_their_lane_is_selected(self):
        check = re.search(r"- if: (needs.changes.outputs.light .+)\n        run: (test '\$\{\{ needs.go-test.result \}\}' = success)",
                          job("build"))
        self.assertIsNotNone(check)
        for event in ("pull_request", "push", "workflow_dispatch"):
            with self.subTest(event=event):
                self.assertTrue(expression(check[1], event=event))
                self.assertFalse(expression(check[1], event=event, go=False))
                self.assertFalse(expression(check[1], event=event, docs=True))

    def test_journey_runs_on_trains_and_main_only(self):
        cases = (
            ("frontend PR", "pull_request", {"web": True}, False),
            ("backend-only PR", "pull_request", {"web": False}, False),
            ("docs-only PR", "pull_request", {"docs": True}, False),
            ("train", "workflow_dispatch", {"web": False}, True),
            ("light train", "workflow_dispatch", {"light": True}, False),
            ("main", "push", {}, True),
            ("verified train result on main", "push", {"reuse": ("journey",)}, False),
            ("gate stress", "workflow_dispatch", {"stress": True}, False),
        )
        for name, event, outputs, expected in cases:
            with self.subTest(name):
                self.assertEqual(enabled("journey", event, **outputs), expected)
        self.assertIn("withcluster bash scripts/ci/web-container.sh pnpm --dir web journey", job("journey"))

    def test_build_blocks_frontend_pull_requests_when_journey_fails_or_is_cancelled(self):
        guard = re.search(r"- if: (contains\(needs\.\*\.result.+)\n", job("build"))[1]
        for result in ("failure", "cancelled", "success"):
            with self.subTest(result=result):
                self.assertEqual(expression(guard, results={"journey": result}), result != "success")

    def test_signing_runs_where_a_release_can_and_the_build_requires_it(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertEqual(enabled("signing", event, docs=docs), not docs and event != "pull_request")
        self.assertIn("id-token: write", job("signing"))
        self.assertIn("id-token: write", job("deploy")) # keyless signing needs the workflow's OIDC token
        self.assertIn("bash scripts/ci/signing-check.sh", job("signing"))
        check = re.search(r"- if: ([^\n]+)\n        run: test '\$\{\{ needs\.signing\.result \}\}' = success", job("build"))
        self.assertIsNotNone(check)
        self.assertTrue(expression(check[1], event="push", docs=False))
        self.assertFalse(expression(check[1], event="pull_request", docs=False))
        self.assertFalse(expression(check[1], event="push", docs=True))

    def test_matrix_and_command_keep_index_and_count(self):
        matrix = re.search(r"shard: \$\{\{ (.+) \}\}", job("e2e")).group(1)
        count = re.search(r"E2E_SHARD_COUNT: \$\{\{ (.+) \}\}", job("e2e")).group(1)
        self.assertEqual(expression(matrix), [1, 2, 3, 4])
        self.assertEqual(expression(count), 4)
        self.assertEqual(expression(matrix, stress=True), [1])
        self.assertEqual(expression(count, stress=True), 1)
        self.assertIn("E2E_SHARD_INDEX: ${{ matrix.shard }}", job("e2e"))
        self.assertIn('just web-e2e "$E2E_SHARD_INDEX/$E2E_SHARD_COUNT"', job("e2e"))
        recipe = re.search(r"^web-e2e .*?\n(.*?)(?=^\S|\Z)", JUSTFILE, re.M | re.S).group(1)
        self.assertIn('scripts/ci/playwright.py e2e {{ quote(shard) }}', recipe)
        self.assertIn("fail-fast: false", job("e2e"))
        self.assertIn("name: e2e-test-results-${{ matrix.shard }}", job("e2e"))

    def test_build_rejects_non_successful_shards_except_light_and_pull_requests(self):
        check = re.search(r"- if: (needs.changes.outputs.light .+)\n        run: (test '\$\{\{ needs.e2e.result \}\}' = success)", job("build"))
        self.assertIsNotNone(check)
        for docs in (False, True):
            self.assertEqual(expression(check[1], event="workflow_dispatch", docs=docs), not docs)
            self.assertEqual(expression(check[1], event="pull_request", docs=docs), not docs)
            self.assertFalse(expression(check[1], event="pull_request", docs=docs, e2e=False))
            self.assertFalse(expression(check[1], event="workflow_dispatch", docs=docs, e2e=False))
            for result in ("success", "failure", "cancelled", "skipped"):
                with self.subTest(docs=docs, result=result):
                    command = check[2].replace("${{ needs.e2e.result }}", result)
                    status = subprocess.run(["bash", "-c", command], capture_output=True).returncode
                    self.assertEqual(status == 0, result == "success")
        guard = re.search(r"- if: (contains\(needs.\*.result.+)\n", job("build"))[1]
        for result in ("failure", "cancelled"):
            self.assertTrue(expression(guard, results={"e2e": result}))

    def test_light_lane_skips_heavy_jobs_and_accepts_their_skips(self):
        for event in ("pull_request", "workflow_dispatch"):
            with self.subTest(event=event):
                self.assertTrue(enabled("static", event, light=True))
                for name in ("go-test", "web", "e2e", "vitals", "journey", "bench", "deploy", "signing"):
                    self.assertFalse(enabled(name, event, light=True, vitals=False), name)
                self.assertTrue(enabled("build", event, light=True))
                for condition in re.findall(r"- if: (needs.changes.outputs.light .+)", job("build")):
                    self.assertFalse(expression(condition, event=event, light=True))
        self.assertIn("run: just md-checks scope", job("static"))

    @unittest.skipUnless(BASH and shutil.which("just"), "Bash or just is unavailable")
    def test_light_static_includes_checks_that_read_light_files(self):
        step = re.search(r"- if: (needs.changes.outputs.light == 'true')\n        run: (.+)", job("static"))
        self.assertIsNotNone(step)
        for event in ("pull_request", "workflow_dispatch"):
            with self.subTest(event=event):
                self.assertTrue(expression(step[1], event=event, light=True))
                command = re.sub(r"\$\{\{ (.+?) \}\}", lambda match: expression(match[1], event=event), step[2])
                result = subprocess.run([BASH, "-e", "-c", command.replace("just ", "just --dry-run ", 1)],
                                        cwd=ROOT, capture_output=True, text=True, check=True)
                for consumer in ("unittest discover -s scripts", "./checkdocnames", "./checkmodules",
                                 "check_artifacts.py", "check_scope.py", "TestModuleOrderFollowsArchitecture"):
                    self.assertIn(consumer, result.stderr)
                self.assertEqual("gitleaks" in result.stderr, event != "pull_request")

    def test_diagnostic_requires_web_and_unsharded_e2e(self):
        self.assertIn("needs: [changes, web, e2e]", job("gate-stress"))
        self.assertIn("test '${{ needs.e2e.result }}' = success", job("gate-stress"))
        self.assertIn("--repeat-each=50 --max-failures=1 --reporter=line", job("e2e"))
        self.assertIn("web: web-checks web-e2e", JUSTFILE)
        self.assertIn("web-checks: web-embed", JUSTFILE)
        self.assertIn('web-e2e shard="1/1": web-embed', JUSTFILE)

    def test_web_checks_keep_every_non_browser_check(self):
        recipe = re.search(r"^web-checks: web-embed\n(.*?)(?=^\S|\Z)", JUSTFILE, re.M | re.S).group(1)
        commands = re.findall(r"^    \{\{ web \}\} (.+)$", recipe, re.M)
        proofs = ("scripts/boards.test.ts", "packages/boards/src/regions.test.ts")
        self.assertEqual(commands, [
            "typecheck", "lint", "limits", "unused", "duplicates",
            f"test --exclude {proofs[0]} --exclude {proofs[1]}",
            f"test --maxWorkers=1 {' '.join(proofs)}",
            "budget",
        ])


def browser_report(names, status="passed"):
    return {"suites": [{"title": "boards.spec.ts", "line": 0, "specs": [
        {"title": name, "file": "boards.spec.ts", "tests": [{
            "projectName": "chromium", "status": "expected" if status == "passed" else "unexpected",
            "results": [{"status": status, "duration": 10}],
        }]} for name in names
    ]}], "errors": []}


class TrainReuse(unittest.TestCase):
    def test_only_completed_train_jobs_on_the_exact_verified_commit_are_reused(self):
        run = dict(id=12, head_sha="head", head_branch="train/main/12", event="workflow_dispatch",
                   status="completed", conclusion="success", html_url="https://example/train/12")
        jobs = [dict(name=name, conclusion="success", status="completed")
                for names in train_result.JOBS.values() for name in names]
        jobs += [dict(name=name, conclusion="success", status="completed") for name in ("changes", "build")]
        def find(statuses=None, runs=None, tree="tree", job_list=None):
            selected = jobs if job_list is None else job_list
            with mock.patch.object(train_result, "api", side_effect=[
                [{"context": "train/verified", "state": "success"}] if statuses is None else statuses,
                {"workflow_runs": [run] if runs is None else runs}, {"tree": {"sha": tree}},
                {"total_count": len(selected), "jobs": selected},
            ]):
                return train_result.find("head", "tree")[0]
        self.assertEqual(set(find()), set(train_result.JOBS))
        self.assertEqual(find(statuses=[]), [])
        self.assertEqual(find(statuses=[{"context": "train/verified", "state": "failure"}]), [])
        self.assertEqual(find(runs=[]), [])
        for field, value in (("head_sha", "other"), ("head_branch", "topic"), ("head_branch", "train/main/land"),
                             ("conclusion", "failure"), ("event", "push"), ("status", "in_progress")):
            with self.subTest(field=field, value=value):
                self.assertEqual(find(runs=[{**run, field: value}]), [])
        self.assertEqual(find(tree="other-tree"), [])
        self.assertEqual(find(job_list=[j for j in jobs if j["name"] != "build"]), [])
        partial = [dict(j, conclusion="skipped") if j["name"] in ("vitals", "e2e (2/4)") else j for j in jobs]
        self.assertEqual(set(find(job_list=partial)), set(train_result.JOBS) - {"vitals", "e2e"})

    def test_lookup_errors_fall_back_to_the_full_build(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.dict(os.environ, {
            "GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": "head",
            "GITHUB_OUTPUT": tmp + "/out",
        }), mock.patch.object(train_result, "find", side_effect=ValueError("bad API response")):
            train_result.main()
            self.assertEqual(Path(tmp + "/out").read_text(), 'reuse=[]\n')

    def test_only_main_push_exports_reuse_and_vitals_source(self):
        for event, ref in (("push", "refs/heads/main"), ("workflow_dispatch", "refs/heads/main"),
                           ("pull_request", "refs/pull/1/merge"), ("push", "refs/heads/topic")):
            with self.subTest(event=event, ref=ref), tempfile.TemporaryDirectory() as tmp, \
                    mock.patch.dict(os.environ, {"GITHUB_EVENT_NAME": event, "GITHUB_REF": ref,
                        "GITHUB_SHA": "head", "GITHUB_OUTPUT": tmp + "/out", "GITHUB_STEP_SUMMARY": tmp + "/summary"}), \
                    mock.patch.object(train_result, "find", return_value=(["static", "vitals"], "https://example/run")) as find:
                train_result.main()
                reuse = json.loads(Path(tmp + "/out").read_text().split("=", 1)[1])
                if event == "push" and ref == "refs/heads/main":
                    find.assert_called_once()
                    self.assertEqual(reuse, ["static", "vitals"])
                    self.assertIn("vitals | success (train)", Path(tmp + "/summary").read_text())
                    self.assertIn("https://example/run", Path(tmp + "/summary").read_text())
                else:
                    find.assert_not_called()
                    self.assertEqual(reuse, [])

    def test_workflow_skips_only_reused_jobs_and_build_still_requires_every_other_success(self):
        reused = tuple(train_result.JOBS)
        # A train runs the Go tests without the race detector, so main reruns them instead of reusing that run
        self.assertNotIn("go-test", reused)
        for name in reused:
            self.assertTrue(enabled(name, "push", boards=True), name)
            self.assertFalse(enabled(name, "push", boards=True, reuse=reused), name)
        self.assertTrue(enabled("changes", "push", reuse=reused))
        self.assertTrue(enabled("build", "push", reuse=reused))
        self.assertTrue(enabled("vitals", "push", reuse=tuple(n for n in reused if n != "vitals")))
        # Execute the actual aggregator assertions, with all reused jobs skipped.
        for failed in (None, "vitals"):
            results = {"go-test": "success", **{name: "skipped" for name in reused}}
            active = reused if failed is None else tuple(n for n in reused if n != failed)
            commands = re.findall(r"(?:- if: ([^\n]+)\n        run: |      - run: )(test [^\n]+)", job("build"))
            statuses = []
            for condition, command in commands:
                if condition and not expression(condition.replace("github.ref", repr("refs/heads/main")),
                                                event="push", results=results, boards=True, reuse=active):
                    continue
                command = re.sub(r"\$\{\{ needs\.([\w-]+)\.result \}\}",
                                 lambda m: "success" if m[1] == "changes" else results[m[1]], command)
                statuses.append(subprocess.run(["bash", "-c", command]).returncode)
            self.assertEqual(all(code == 0 for code in statuses), failed is None)
        self.assertIn("actions: read", job("changes"))
        self.assertIn("inputs.release", job("deploy"))


class BrowserSelection(unittest.TestCase):
    def test_longest_first_balances_measured_work_and_keeps_serial_files_whole(self):
        found = {name: {"file": file} for name, file in (("a", "one"), ("b", "two"), ("c", "three"),
                                                        ("d", "four"), ("serial-1", "one"))}
        with contextlib.redirect_stdout(io.StringIO()):
            shards = browser.partition(found, {"a": 90, "b": 80, "c": 20, "d": 10, "serial-1": 1}, 2, "boards")
            self.assertEqual([sum({"a": 90, "b": 80, "c": 20, "d": 10, "serial-1": 1}[n] for n in s)
                              for s in shards], [101, 100])
            shards = browser.partition(found, {"a": 90, "b": 80, "c": 20, "d": 10, "serial-1": 1}, 2, "e2e")
        self.assertEqual(sorted(n for s in shards for n in s), sorted(found))
        self.assertTrue(any("a" in s and "serial-1" in s for s in shards))

    def test_new_tests_are_selected_and_title_paths_match_playwright_lists(self):
        report = browser_report(["AxApp light", "Site light", "new board"])
        report["suites"][0]["suites"] = [{"title": "nested suite", "line": 12,
                                           "specs": report["suites"][0]["specs"][:1]}]
        found = browser.tests(report)
        self.assertIn("[chromium] › boards.spec.ts › nested suite › AxApp light", found)
        with contextlib.redirect_stdout(io.StringIO()):
            shards = browser.partition(found, {}, 4, "boards")
        self.assertEqual(sorted(n for s in shards for n in s), sorted(found))
        self.assertEqual(len(shards), 4)

    def test_parallel_file_can_balance_across_shards(self):
        found = {name: {"file": "gate.spec.ts"} for name in ("slow-a", "slow-b", "fast-a", "fast-b")}
        weights = {"slow-a": 90, "slow-b": 80, "fast-a": 20, "fast-b": 10}
        with contextlib.redirect_stdout(io.StringIO()):
            shards = browser.partition(found, weights, 2, "e2e", {"gate.spec.ts"})
        self.assertEqual([sum(weights[n] for n in shard) for shard in shards], [100, 100])

    @unittest.skipUnless(shutil.which("node"), "Node is unavailable")
    def test_real_manifest_evaluation_changes_only_its_board(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            package = root / "web/packages/boards"
            (package / "src").mkdir(parents=True)
            (package / "catalog.ts").write_text('export const boardNames = ["AxApp", "AxAppPhone", "Site"];')
            (package / "src/demo.ts").write_text('export const boards = {}; export const slices = {};')
            regions = package / "regions.ts"
            regions.write_text('export const regions = ["AxApp", "AxAppPhone"].map(board => ({board, name:"Shell"}));')
            def read():
                result = subprocess.run(["node", str(ROOT / "scripts/ci/boards-inputs.mjs")], cwd=root,
                                        text=True, capture_output=True, check=True)
                return json.loads(result.stdout)["inputs"]
            before = read()
            regions.write_text(regions.read_text().replace('name:"Shell"', 'name:"Overview"'))
            after = read()
            self.assertEqual(before["Site"], after["Site"])
            for board in ("AxApp", "AxAppPhone"):
                self.assertNotEqual(before[board], after[board])

    def test_a_partial_or_skipped_board_report_cannot_certify_the_selection(self):
        full = browser_report(["AxApp light", "AxApp dark"])
        selected = list(browser.tests(full))
        self.assertTrue(browser.green(full, selected))
        for report in (browser_report(["AxApp light"]), browser_report(["AxApp light", "AxApp dark"], "skipped")):
            self.assertFalse(browser.green(report, selected))

    def test_cache_hits_skip_browsers_and_input_read_failures_run_every_selected_test(self):
        with tempfile.TemporaryDirectory() as tmp:
            root, cache = Path(tmp), Path(tmp) / "cache"
            (root / "scripts/ci").mkdir(parents=True)
            (root / "scripts/ci/playwright-durations.json").write_text('{"boards":{}}')
            names = ["AxApp light", "Site light"]
            report = browser_report(names)
            played = []
            def run(command, **kwargs):
                if "--list" in command:
                    return subprocess.CompletedProcess(command, 0, stdout=json.dumps(report))
                if command[0] == "node":
                    return subprocess.CompletedProcess(command, 0, stdout='{"boards":["AxApp","Site"],"inputs":{}}')
                listing = next(arg.split("=", 1)[1] for arg in command if arg.startswith("--test-list="))
                played.append(Path(listing).read_text().splitlines())
                chosen = [item.rsplit(" › ", 1)[1] for item in played[-1]]
                measured = {**report, "suites": [{**report["suites"][0], "specs": [
                    spec for spec in report["suites"][0]["specs"] if spec["title"] in chosen]}]}
                Path(kwargs["env"]["PLAYWRIGHT_JSON_OUTPUT_FILE"]).write_text(json.dumps(measured))
                return subprocess.CompletedProcess(command, 0)
            with mock.patch.object(browser.subprocess, "run", side_effect=run), \
                    mock.patch.object(browser.web_cache, "toolchain", return_value="tools"), \
                    mock.patch.object(browser.web_cache, "boards_key", side_effect=lambda root, board, *args: board), \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(browser.execute(root, cache, "boards", "1/1", ""), 0)
                self.assertEqual(browser.execute(root, cache, "boards", "1/1", ""), 0)
                self.assertEqual(len(played), 1)
                (cache / "boards/AxApp/AxApp").unlink()
                self.assertEqual(browser.execute(root, cache, "boards", "1/1", ""), 0)
                self.assertEqual(played[-1], ["[chromium] › boards.spec.ts › AxApp light"])
                with mock.patch.object(browser.web_cache, "boards_key", side_effect=OSError("unreadable")):
                    self.assertEqual(browser.execute(root, cache, "boards", "1/1", ""), 0)
                self.assertEqual(played[0], played[-1])
                # A failed batch must not certify even the board that passed before another one failed.
                shutil.rmtree(cache)
                report["suites"][0]["specs"][1]["tests"][0]["results"][0]["status"] = "failed"
                self.assertEqual(browser.execute(root, cache, "boards", "1/1", ""), 1)
                self.assertFalse(cache.exists())


class RunnerComparison(unittest.TestCase):
    def test_queue_setup_and_four_core_estimate_use_measured_intervals(self):
        job = {"created_at": "2026-11-18T00:00:00Z", "started_at": "2026-11-18T00:00:10Z",
               "completed_at": "2026-11-18T00:01:30Z", "steps": [
                   {"name": "Run ./.github/actions/setup", "started_at": "2026-11-18T00:00:10Z",
                    "completed_at": "2026-11-18T00:00:25Z"},
                   {"name": "Post Run ./.github/actions/setup", "started_at": "2026-11-18T00:01:25Z",
                    "completed_at": "2026-11-18T00:01:30Z"}]}
        self.assertEqual(comparison.timings(job), (80, 10, 20))
        for busy, projected in ((0, 80), (60, 65), (120, 50)):
            with self.subTest(cpu_seconds=busy):
                self.assertEqual(comparison.estimate(80, {"seconds": 60, "cpu_seconds": busy, "cores": 2}), projected)


class RunnerEvidence(unittest.TestCase):
    def test_retention_is_two_days_and_shards_attempts_and_runs_are_isolated(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            cache = root / "cache"
            old, recent = cache / "reports/old", cache / "reports/recent"
            old.mkdir(parents=True)
            recent.mkdir()
            now = 1_000_000
            os.utime(old, (now - 172800, now - 172800))
            os.utime(recent, (now - 172799, now - 172799))
            env = {"GITHUB_RUN_ID": "123", "GITHUB_JOB": "boards", "GITHUB_RUN_ATTEMPT": "1",
                   "E2E_SHARD_INDEX": "1", "RUNNER_NAME": "test-host", "GITHUB_STEP_SUMMARY": str(root / "summary"),
                   "GITHUB_ENV": str(root / "env")}
            with mock.patch.object(reports.time, "time", return_value=now), contextlib.redirect_stdout(io.StringIO()):
                first = reports.prepare(cache, env)
                second = reports.prepare(cache, {**env, "E2E_SHARD_INDEX": "2"})
                retry = reports.prepare(cache, {**env, "GITHUB_RUN_ATTEMPT": "2"})
            self.assertFalse(old.exists())
            self.assertTrue(recent.exists())
            self.assertEqual(len({first, second, retry}), 3)
            for name in ("test-results/trace.zip", "test-results/diff.png", "playwright-report/report.json"):
                path = root / "web" / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(name)
            reports.collect(first, root, failed=True)
            reports.collect(second, root, failed=False)
            self.assertTrue((first / "test-results/trace.zip").is_file())
            self.assertTrue((first / "test-results/diff.png").is_file())
            self.assertFalse((second / "test-results").exists())
            self.assertTrue((second / "playwright-report/report.json").is_file())
            self.assertIn(str(first), (root / "summary").read_text())
            self.assertIn("test-host", (root / "summary").read_text())
            self.assertIn("AXIOM_TEST_RESULTS=/ci/axiom/reports/123/boards/1-1/test-results", (root / "env").read_text())
            self.assertIn("AXIOM_PLAYWRIGHT_REPORTS=/ci/axiom/reports/123/boards/1-1/playwright-report", (root / "env").read_text())


class TestContainerPolicy(unittest.TestCase):
    def test_cleanup_brackets_tests_and_matches_job_timeout(self):
        steps = job("go-test")
        self.assertRegex(steps, r"(?m)^    timeout-minutes: 30$")
        before = steps.index("- name: Clean test containers before tests")
        tests = steps.index("- run: just test")
        after = steps.index("- name: Clean test containers after tests")
        self.assertLess(before, tests)
        self.assertLess(tests, after)
        self.assertIn("if: always()", steps[after:])
        self.assertEqual(steps.count("run: bash scripts/ci/clean-test-containers.sh"), 2)

    def test_every_job_that_starts_test_containers_has_the_same_policy(self):
        # bench starts PostgreSQL and Valkey too and lacked the policy: Ryuk timed out on a loaded runner (#411's
        # mechanism, train build 41569176679)
        policy = lambda name: re.search(
            r"- name: Configure test container policy\n        run: \|\n(.*?)(?=^      -)", job(name), re.M | re.S)
        for name in ("journey", "bench"):
            with self.subTest(job=name):
                self.assertIsNotNone(policy(name))
                self.assertEqual(policy(name).group(1), policy("go-test").group(1))
                self.assertEqual(job(name).count("run: bash scripts/ci/clean-test-containers.sh"), 2)

    def test_policy_is_conditional_and_identifies_the_attempt_and_job(self):
        if not BASH:
            self.skipTest("Bash from Git for Windows or PATH is unavailable")
        policy = re.search(
            r"- name: Configure test container policy\n        run: \|\n(.*?)(?=^      -)",
            job("go-test"), re.M | re.S,
        ).group(1)
        # shards of one job share GITHUB_JOB, so each needs its own label; an unsharded job has none
        for parent, shard in ((None, None), ("", None), ("axiom-ci.slice", None), (None, "2")):
            with self.subTest(parent=parent, shard=shard), tempfile.TemporaryDirectory() as tmp:
                output = Path(tmp) / "env"
                env = {**os.environ, "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2",
                       "GITHUB_JOB": "go-test", "GITHUB_ENV": output.as_posix()}
                for name, value in (("AXIOM_TEST_CGROUP_PARENT", parent), ("AXIOM_TEST_SHARD", shard)):
                    env.pop(name, None)
                    if value is not None:
                        env[name] = value
                subprocess.run([BASH, "-euo", "pipefail", "-c", textwrap.dedent(policy)], env=env, check=True)
                expected = "AXIOM_TEST_JOB=123-2-go-test" + (f"-{shard}" if shard else "") + "\n"
                if parent:
                    expected += "TESTCONTAINERS_RYUK_DISABLED=true\n"
                self.assertEqual(output.read_text(), expected)

    def run_cleanup(self, job_id="123-2-go-test", fail_list=False):
        if not BASH:
            self.skipTest("Bash from Git for Windows or PATH is unavailable")
        script = (Path(__file__).parent / "ci" / "clean-test-containers.sh").read_text()
        # Deterministic Docker inventory and clock: no real containers or wall-clock waits in these tests.
        stubs = r'''
    docker() {
      printf '%s\n' "$*" >> "$DOCKER_LOG"
      case "$*" in
        'ps -aq --filter label=axiom.ci.job=123-2-go-test')
          [ "$FAIL_LIST" != true ] || return 37
          printf '%s\n' own-new own-stopped already-gone ;;
        'ps -aq --filter label=axiom.ci.job')
          printf '%s\n' old boundary recent future vanished invalid ;;
        'inspect --format {{.Created}} old') echo 7500 ;;
        'inspect --format {{.Created}} boundary') echo 7600 ;;
        'inspect --format {{.Created}} recent') echo 7601 ;;
        'inspect --format {{.Created}} future') echo 10001 ;;
        'inspect --format {{.Created}} vanished') return 1 ;;
        'inspect --format {{.Created}} invalid') echo invalid ;;
        'rm -fv already-gone') return 1 ;;
        'rm -fv own-new'|'rm -fv own-stopped'|'rm -fv old'|'rm -fv boundary') ;;
        *) echo "unexpected Docker call: $*" >&2; return 99 ;;
      esac
    }
    date() {
      if [ "$*" = '+%s' ]; then echo 10000
      elif [ "$2" = invalid ]; then return 1
      else echo "$2"; fi
    }
'''
        with tempfile.TemporaryDirectory() as tmp:
            log = Path(tmp) / "docker"
            env = {**os.environ, "AXIOM_TEST_JOB": job_id, "DOCKER_LOG": log.as_posix(),
                   "FAIL_LIST": str(fail_list).lower()}
            result = subprocess.run([BASH, "-c", stubs + script], env=env, text=True, capture_output=True)
            calls = log.read_text().splitlines() if log.exists() else []
        return result, calls

    def test_cleanup_owns_only_current_attempt_and_expired_labelled_containers(self):
        result, calls = self.run_cleanup()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call for call in calls if call.startswith("rm ")], [
            "rm -fv own-new", "rm -fv own-stopped", "rm -fv already-gone", "rm -fv old", "rm -fv boundary",
        ])
        self.assertEqual([call for call in calls if call.startswith("ps ")], [
            "ps -aq --filter label=axiom.ci.job=123-2-go-test", "ps -aq --filter label=axiom.ci.job",
        ])

    def test_cleanup_requires_an_identity_and_reports_inventory_failure(self):
        result, calls = self.run_cleanup(job_id="")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])
        result, calls = self.run_cleanup(fail_list=True)
        self.assertEqual(result.returncode, 37)
        self.assertEqual(len(calls), 1)


class ReleaseChecks(unittest.TestCase):
    def step_condition(self, name):
        return re.search(r"- name: " + name + r"\n        if: (.+)", job("deploy"))[1]

    def runs(self, name, ref, base="", release=False):
        return expression(self.step_condition(name).replace("github.ref", repr(ref)), base=base, release=release)

    def test_every_build_rehearses_deployment_once_and_trains_build_releases(self):
        deploy = job("deploy")
        for ref, base, release, rehearsal, retains in (
            ("refs/heads/main", "", False, "release", False),  # pushes build without retaining
            ("refs/heads/main", "", True, "release", True),    # explicit releases retain the same files
            ("refs/heads/main", "base", True, "release", False),
            ("refs/heads/train/main/1", "base", True, "release", False),  # a train exercises the release step too
            ("refs/heads/topic", "", True, "deploy", False),          # a manual branch dispatch with no base
        ):
            with self.subTest(ref=ref, base=base):
                self.assertEqual(self.runs("Rehearse train deployment", ref, base), rehearsal == "deploy")
                self.assertEqual(self.runs("Build and rehearse release by digest", ref, base), rehearsal == "release")
                self.assertEqual(self.runs("Retain release image, signature and provenance", ref, base, release), retains)
        self.assertIn("if-no-files-found: error", deploy)
        self.assertIn("retention-days: 90", deploy)
        self.assertNotIn("secrets.", deploy)

    def test_main_cannot_pass_without_a_release(self):
        check = re.search(r"- if: (github.ref .+)\n        run: (.+)", job("build"))
        self.assertIsNotNone(check)
        for ref in ("refs/heads/main", "refs/heads/train/main/1"):
            self.assertEqual(expression(check[1].replace("github.ref", repr(ref))), ref == "refs/heads/main")
        for result in ("success", "failure", "cancelled", "skipped"):
            command = check[2].replace("${{ needs.deploy.result }}", result)
            self.assertEqual(subprocess.run(["bash", "-c", command]).returncode == 0, result == "success")

    def test_release_check_signs_and_verifies_the_digest_before_deploying(self):
        script = (ROOT / "scripts" / "ci" / "release-check.sh").read_text()
        for command in ("cosign sign", "cosign attest", "cosign save", "verify-release.sh"):
            self.assertIn(command, script)
        self.assertLess(script.index("cosign sign"), script.index("cosign attest"))
        self.assertLess(script.index("cosign attest"), script.index("verify-release.sh"))
        self.assertLess(script.index("verify-release.sh"), script.index("docker pull"))
        self.assertIn("CI_DEPLOY_RELEASE", script)


class FetchReleaseBinary(unittest.TestCase):
    SCRIPT = ROOT / "scripts" / "ci" / "fetch-release-binary.sh"

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(subprocess.run, ["rm", "-rf", self.tmp.as_posix()])
        src = self.tmp / "src"
        src.mkdir()
        (src / "tool").write_text("#!/bin/sh\necho ok\n")
        self.tarball = self.tmp / "tool.tar.gz"
        subprocess.run(["tar", "-czf", self.tarball, "-C", src, "tool"], check=True)
        self.sha = subprocess.run(["sha256sum", self.tarball], capture_output=True, text=True,
                                  check=True).stdout.split()[0]
        # A curl that logs each URL and serves the "release" from the API path only when BROWSER_DOWN is set; the
        # network is never touched
        bin_dir = self.tmp / "bin"
        bin_dir.mkdir()
        curl = bin_dir / "curl"
        curl.write_text(textwrap.dedent("""\
            #!/bin/sh
            for last; do :; done
            echo "$last" >> "$CALLS"
            out=
            while [ $# -gt 0 ]; do [ "$1" = -o ] && out=$2; shift; done
            case "$last" in
              https://github.com/*) [ -z "$BROWSER_DOWN" ] || exit 22; cp "$SERVED" "$out";;
              https://api.github.com/*) echo '{"assets":[{"name":"tool.tar.gz","url":"https://api.example/asset"}]}';;
              https://api.example/asset) cp "$SERVED" "$out";;
            esac
            """))
        curl.chmod(0o755)
        self.cache = self.tmp / "cache"
        self.env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "CALLS": (self.tmp / "calls").as_posix(),
                    "SERVED": self.tarball.as_posix(), "RUNNER_TOOL_CACHE": self.cache.as_posix(), "BROWSER_DOWN": ""}

    def fetch(self, sha=None, **env):
        dest = Path(tempfile.mkdtemp(dir=self.tmp))
        result = subprocess.run([BASH, self.SCRIPT, "tool", "1.0", sha or self.sha, "o/r", "v1", "tool.tar.gz", dest],
                                env={**self.env, **env}, capture_output=True, text=True)
        return result, dest

    def calls(self):
        path = self.tmp / "calls"
        return path.read_text().splitlines() if path.exists() else []

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_downloads_once_then_reuses_the_verified_copy(self):
        for _ in range(2):
            result, dest = self.fetch()
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertTrue((dest / "tool").exists())
        self.assertEqual(len(self.calls()), 1)

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_a_tampered_cached_copy_is_downloaded_again(self):
        self.fetch()
        (self.cache / "tool" / "1.0" / "tool.tar.gz").write_bytes(b"swapped")
        result, dest = self.fetch()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((dest / "tool").exists())
        self.assertEqual(len(self.calls()), 2)

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_the_api_serves_the_asset_when_the_download_url_is_down(self):
        result, dest = self.fetch(BROWSER_DOWN="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((dest / "tool").exists())
        self.assertTrue(any(c.startswith("https://api.github.com/repos/o/r/releases/tags/v1") for c in self.calls()))

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_a_swapped_download_fails_on_either_path_and_is_not_cached(self):
        for down in ("", "1"):
            with self.subTest(browser_down=down):
                result, dest = self.fetch(sha="0" * 64, BROWSER_DOWN=down)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("checksum mismatch", result.stderr)
                self.assertFalse((dest / "tool").exists())
                self.assertEqual(list((self.cache / "tool" / "1.0").iterdir()), [])
