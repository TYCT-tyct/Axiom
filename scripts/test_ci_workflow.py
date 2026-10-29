import json
import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

from test_runner_host import BASH


ROOT = Path(__file__).parents[1]
WORKFLOW = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
JUSTFILE = (ROOT / "justfile").read_text(encoding="utf-8")


def job(name):
    return re.search(rf"^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)", WORKFLOW, re.M | re.S).group(1)


def enabled(name, event, stress=False, draft=False, docs=False):
    condition = re.search(r"^    if: (.+)$", job(name), re.M).group(1)
    return expression(condition, event, stress, draft, docs)


def expression(condition, event="pull_request", stress=False, draft=False, docs=False, results=None):
    results = results or {}
    values = {
        "always()": True,
        "github.event_name": event,
        "github.event.pull_request.draft": draft,
        "inputs.gate_stress": stress,
        "needs.changes.result": results.get("changes", "success"),
        "needs.changes.outputs.docs_only": "true" if docs else "false",
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
        self.assertEqual(set(needs), {"changes", "static", "go-test", "web", "e2e", "bench", "deploy"})
        for name in ("web", "e2e"):
            self.assertRegex(job(name), r"(?m)^    needs: changes$")
            self.assertIn("bash scripts/ci/web-container.sh", job(name))
            self.assertRegex(job(name), r"- if: always\(\)\n        run: bash scripts/ci/web-container.sh cleanup")
            for event in ("pull_request", "push", "workflow_dispatch"):
                for docs in (False, True):
                    with self.subTest(job=name, event=event, docs=docs):
                        # the tests run once, in the train and on main; a pull request runs only the cheap checks
                        want = not docs and (name == "web" or event != "pull_request")
                        self.assertEqual(enabled(name, event, docs=docs), want)

    def test_go_tests_run_in_trains_and_main_not_on_pull_requests(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertEqual(enabled("go-test", event, docs=docs), not docs and event != "pull_request")

    def test_matrix_and_command_keep_index_and_count(self):
        matrix = re.search(r"shard: \$\{\{ (.+) \}\}", job("e2e")).group(1)
        count = re.search(r"E2E_SHARD_COUNT: \$\{\{ (.+) \}\}", job("e2e")).group(1)
        self.assertEqual(expression(matrix), [1, 2])
        self.assertEqual(expression(count), 2)
        self.assertEqual(expression(matrix, stress=True), [1])
        self.assertEqual(expression(count, stress=True), 1)
        self.assertIn("E2E_SHARD_INDEX: ${{ matrix.shard }}", job("e2e"))
        self.assertIn('just web-e2e "$E2E_SHARD_INDEX/$E2E_SHARD_COUNT"', job("e2e"))
        recipe = re.search(r"^web-e2e .*?\n(.*?)(?=^\S|\Z)", JUSTFILE, re.M | re.S).group(1)
        self.assertRegex(recipe, r"scripts/web_cache.py e2e .* -- .* e2e --shard=\{\{ quote\(shard\) \}\}")
        self.assertIn("fail-fast: false", job("e2e"))
        self.assertIn("name: e2e-test-results-${{ matrix.shard }}", job("e2e"))

    def test_build_rejects_non_successful_shards_except_docs_only_and_pull_requests(self):
        check = re.search(r"- if: (needs.changes.outputs.docs_only .+)\n        run: (.+)", job("build"))
        self.assertIsNotNone(check)
        for docs in (False, True):
            self.assertEqual(expression(check[1], event="workflow_dispatch", docs=docs), not docs)
            self.assertFalse(expression(check[1], event="pull_request", docs=docs))
            for result in ("success", "failure", "cancelled", "skipped"):
                with self.subTest(docs=docs, result=result):
                    command = check[2].replace("${{ needs.e2e.result }}", result)
                    status = subprocess.run(["bash", "-c", command], capture_output=True).returncode
                    self.assertEqual(status == 0, result == "success")
        guard = re.search(r"- if: (contains\(needs.\*.result.+)\n", job("build"))[1]
        for result in ("failure", "cancelled"):
            self.assertTrue(expression(guard, results={"e2e": result}))

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
        self.assertEqual(commands, ["typecheck", "lint", "limits", "unused", "duplicates", "test", "budget"])


class TestContainerPolicy(unittest.TestCase):
    def test_cleanup_brackets_tests_and_matches_job_timeout(self):
        steps = job("go-test")
        self.assertRegex(steps, r"(?m)^    timeout-minutes: 40$")
        before = steps.index("- name: Clean test containers before tests")
        tests = steps.index("- run: just test")
        after = steps.index("- name: Clean test containers after tests")
        self.assertLess(before, tests)
        self.assertLess(tests, after)
        self.assertIn("if: always()", steps[after:])
        self.assertEqual(steps.count("run: bash scripts/ci/clean-test-containers.sh"), 2)

    def test_policy_is_conditional_and_identifies_the_attempt_and_job(self):
        if not BASH:
            self.skipTest("Bash from Git for Windows or PATH is unavailable")
        policy = re.search(
            r"- name: Configure test container policy\n        run: \|\n(.*?)(?=^      -)",
            job("go-test"), re.M | re.S,
        ).group(1)
        for parent in (None, "", "axiom-ci.slice"):
            with self.subTest(parent=parent), tempfile.TemporaryDirectory() as tmp:
                output = Path(tmp) / "env"
                env = {**os.environ, "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2",
                       "GITHUB_JOB": "go-test", "GITHUB_ENV": output.as_posix()}
                env.pop("AXIOM_TEST_CGROUP_PARENT", None)
                if parent is not None:
                    env["AXIOM_TEST_CGROUP_PARENT"] = parent
                subprocess.run([BASH, "-euo", "pipefail", "-c", textwrap.dedent(policy)], env=env, check=True)
                expected = "AXIOM_TEST_JOB=123-2-go-test\n"
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
