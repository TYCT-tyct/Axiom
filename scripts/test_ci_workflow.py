import json
import os
import re
import shutil
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


def enabled(name, event, stress=False, draft=False, docs=False, vitals=True, light=False):
    folded = re.search(r"^    if: >-\n((?:      .+\n)+)", job(name), re.M)
    if folded:
        condition = " ".join(line.strip() for line in folded.group(1).splitlines())
    else:
        condition = re.search(r"^    if: (.+)$", job(name), re.M).group(1)
    return expression(condition, event, stress, draft, docs, vitals=vitals, light=light)


def expression(condition, event="pull_request", stress=False, draft=False, docs=False, results=None, base="", vitals=True, light=False, release=False):
    results = results or {}
    values = {
        "always()": True,
        "github.event_name": event,
        "github.event.pull_request.draft": draft,
        "inputs.gate_stress": stress,
        "inputs.base": base,
        "inputs.release": release,
        "needs.changes.result": results.get("changes", "success"),
        "needs.changes.outputs.light": "true" if docs or light else "false",
        "needs.changes.outputs.web": "false" if light else "true",
        "needs.changes.outputs.deploy": "false" if light else "true",
        "needs.changes.outputs.bench": "false" if light else "true",
        "needs.changes.outputs.vitals": "true" if vitals else "false",
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
        self.assertEqual(set(needs), {"changes", "static", "go-test", "web", "e2e", "vitals", "journey", "bench", "deploy", "signing"})
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

    def test_vitals_run_on_main_and_by_page_inputs_on_prs_and_trains(self):
        self.assertRegex(job("vitals"), r"(?m)^    needs: changes$")
        self.assertIn("bash scripts/ci/web-container.sh", job("vitals"))
        self.assertRegex(job("vitals"), r"- if: always\(\)\n        run: bash scripts/ci/web-container.sh cleanup")
        for event in ("pull_request", "push", "workflow_dispatch"):
            for vitals in (False, True):
                with self.subTest(event=event, vitals=vitals):
                    # changes decides by path on PRs and trains and always says true on main; Markdown-only says false
                    self.assertEqual(enabled("vitals", event, vitals=vitals), vitals)

    def test_build_requires_vitals_exactly_when_they_are_scheduled(self):
        # A skipped job counts as passed on GitHub, so the aggregator must require the timing job whenever changes said
        # it was due, and only then.
        check = re.search(r"- if: (needs.changes.outputs.vitals == 'true')\n        run: (.+)", job("build"))
        self.assertIsNotNone(check)
        self.assertTrue(expression(check[1], vitals=True))
        self.assertFalse(expression(check[1], vitals=False))
        for result in ("success", "failure", "cancelled", "skipped"):
            with self.subTest(result=result):
                command = check[2].replace("${{ needs.vitals.result }}", result)
                self.assertEqual(subprocess.run(["bash", "-c", command]).returncode == 0, result == "success")

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
        for event in ("pull_request", "push", "workflow_dispatch"):
            for status in (0, 10, 37):
                with self.subTest(event=event, status=status), tempfile.TemporaryDirectory() as tmp:
                    output = Path(tmp) / "output"
                    env = {**os.environ, "BASE": "" if event == "push" else "base", "WEB_STATUS": str(status),
                           "GITHUB_OUTPUT": output.as_posix()}
                    script = stub + decide.replace("${{ github.event_name }}", event)
                    subprocess.run([BASH, "-euo", "pipefail", "-c", script], env=env, capture_output=True, check=True)
                    values = dict(line.split("=", 1) for line in output.read_text().splitlines())
                    self.assertEqual(values["web"], "false" if event == "pull_request" and status == 10 else "true")

    @unittest.skipUnless(BASH, "Bash is unavailable")
    def test_vitals_selection_covers_prs_and_trains_and_never_skips_main(self):
        decide = textwrap.dedent(re.search(
            r"- id: decide\n        run: \|\n(.*?)(?=^  #)", WORKFLOW, re.M | re.S)[1])
        stub = ("python3() { case \"$2\" in\n"
                "  light-only|md-only) return \"$MD_STATUS\";;\n"
                "  vitals-needed) return \"$VITALS_STATUS\";;\n"
                "  *) return 0;;\n"
                "esac; }\n")
        cases = {
            "main on a push": ("push", "", "1", "10", "true"),
            "a PR touching the pages": ("pull_request", "base", "1", "0", "true"),
            "a PR touching other files": ("pull_request", "base", "1", "10", "false"),
            "a train touching the pages": ("workflow_dispatch", "base", "1", "0", "true"),
            "a train touching other files": ("workflow_dispatch", "base", "1", "10", "false"),
            "a failed diff runs them": ("workflow_dispatch", "base", "1", "37", "true"),
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

    def test_go_tests_run_in_trains_and_main_not_on_pull_requests(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertEqual(enabled("go-test", event, docs=docs), not docs and event != "pull_request")

    def test_journey_runs_in_trains_and_main_not_on_pull_requests(self):
        for event in ("pull_request", "push", "workflow_dispatch"):
            for docs in (False, True):
                with self.subTest(event=event, docs=docs):
                    self.assertEqual(enabled("journey", event, docs=docs), not docs and event != "pull_request")
        self.assertIn("withcluster bash scripts/ci/web-container.sh pnpm --dir web journey", job("journey"))

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

    def test_build_rejects_non_successful_shards_except_light_and_pull_requests(self):
        check = re.search(r"- if: (needs.changes.outputs.light .+)\n        run: (.+)", job("build"))
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
