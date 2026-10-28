import os
import re
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

from test_runner_host import BASH


WORKFLOW = (Path(__file__).parents[1] / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")


def job(name):
    return re.search(rf"^  {name}:\n(.*?)(?=^  [\w-]+:|\Z)", WORKFLOW, re.M | re.S).group(1)


def enabled(name, event, stress=False, draft=False, docs=False):
    condition = re.search(r"^    if: (.+)$", job(name), re.M).group(1)
    values = {
        "always()": True,
        "github.event_name": event,
        "github.event.pull_request.draft": draft,
        "inputs.gate_stress": stress,
        "needs.changes.result": "success",
        "needs.changes.outputs.docs_only": "true" if docs else "false",
    }
    for key, value in values.items():
        condition = condition.replace(key, repr(value))
    condition = condition.replace("&&", " and ").replace("||", " or ")
    condition = re.sub(r"!(?!=)", " not ", condition)
    return eval(condition, {"__builtins__": {}}, {})


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
        self.assertTrue(enabled("web", "workflow_dispatch", stress=True, docs=True))
        self.assertTrue(enabled("build", "workflow_dispatch", stress=True))
        # A skipped required check is accepted by GitHub; the diagnostic must explicitly fail it instead.
        self.assertRegex(job("build"), r"(?s)- if: inputs.gate_stress\n        run: \|\n.*?\n          exit 1")
        self.assertFalse(enabled("gate-stress", "pull_request", stress=True))


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
