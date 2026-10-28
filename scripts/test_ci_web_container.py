"""Exercise the CI container boundary without starting Docker or touching host cgroups."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "scripts/ci/web-container.sh"
SHIM = '''#!{python}
import json, os, pathlib, sys
command = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ["CALLS"], "a") as log:
    log.write(json.dumps([command, *args]) + "\\n")
if command == "go":
    print(os.environ["FAKE_CACHE"] + "/" + args[-1])
elif command == "pnpm":
    print(os.environ["FAKE_CACHE"] + "/pnpm")
elif command == "docker":
    if args[0] == "inspect":
        print(os.environ.get("FAKE_PID", "123"))
    elif args[0] == "wait":
        print(os.environ.get("FAKE_EXIT", "0"))
    elif args[:2] == ["container", "inspect"]:
        sys.exit(int(os.environ.get("MISSING", "0")))
elif command == "grep":
    sys.exit(int(os.environ.get("WRONG_CGROUP", "0")))
elif command == "node":
    print(os.environ.get("FAKE_CPUS", "8"))
'''


@unittest.skipUnless(sys.platform == "linux", "CI containers require Linux")
class WebContainer(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.calls = self.root / "calls.jsonl"
        for command in ("docker", "go", "pnpm", "cat", "grep", "node", "git"):
            shim = self.root / command
            shim.write_text(SHIM.format(python=sys.executable), encoding="utf-8")
            shim.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.root) + os.pathsep + os.environ["PATH"],
                        CALLS=str(self.calls), FAKE_CACHE=str(self.root / "cache"),
                        AXIOM_CACHE_DIR=str(self.root / "web-cache"), GITHUB_RUN_ID="100", GITHUB_RUN_ATTEMPT="2",
                        GITHUB_JOB="e2e", E2E_SHARD_INDEX="1", GITHUB_WORKSPACE=str(self.root),
                        RUNNER_TEMP=str(self.root), RUNNER_TOOL_CACHE=str(self.root), WEB_IMAGE="browser@sha256:test")

    def run_script(self, *command, **env):
        return subprocess.run(["bash", str(SCRIPT), *command], env=dict(self.env, **env), capture_output=True, text=True)

    def commands(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_container_keeps_isolation_mounts_and_command_arguments(self):
        self.assertEqual(self.run_script("just", "web-e2e", "1/2").returncode, 0)
        create = next(args for args in self.commands() if args[:2] == ["docker", "create"])
        for option, value in (("--cpus", "8"), ("--user", f"{os.getuid()}:{os.getgid()}")):
            self.assertEqual(create[create.index(option) + 1], value)
        self.assertIn("--cgroup-parent=axiom-ci.slice", create)
        self.assertIn("--cgroupns=private", create)
        self.assertNotIn("--network=host", create)
        self.assertIn(f"{self.root}:{self.root}", create)
        self.assertIn(f"{self.root / 'web-cache'}:/ci/axiom", create)
        self.assertEqual(create[-4:], ["bash", "just", "web-e2e", "1/2"])
        self.assertIn(["grep", "-q", "/axiom.slice/axiom-ci.slice/", "/proc/123/cgroup"], self.commands())
        self.assertIn(["docker", "rm", "--force", "axiom-web-100-2-e2e-1"], self.commands())
        bootstrap = create[create.index("-c") + 1]
        for cpus, expected in (("8", 0), ("9", 1)):
            with self.subTest(cpus=cpus):
                result = subprocess.run(["bash", "-e", "-c", bootstrap, "bash", "true"],
                                        env=dict(self.env, HOME=str(self.root), FAKE_CPUS=cpus), capture_output=True)
                self.assertEqual(result.returncode, expected)

    def test_failed_or_exited_container_and_wrong_cgroup_fail_and_cleanup(self):
        for env, status in (({"FAKE_EXIT": "17"}, 17), ({"FAKE_PID": "0"}, 1), ({"WRONG_CGROUP": "1"}, 1)):
            with self.subTest(env=env):
                self.calls.write_text("")
                self.assertEqual(self.run_script("just", "web-e2e", "1/2", **env).returncode, status)
                self.assertEqual(self.commands()[-1], ["docker", "rm", "--force", "axiom-web-100-2-e2e-1"])
                if "FAKE_EXIT" not in env:
                    self.assertFalse(any(args[:2] == ["docker", "wait"] for args in self.commands()))

    def test_cleanup_only_names_its_attempt_job_and_shard(self):
        for job, shard in (("web", ""), ("e2e", "1"), ("e2e", "2")):
            with self.subTest(job=job, shard=shard):
                self.calls.write_text("")
                self.assertEqual(self.run_script("cleanup", GITHUB_JOB=job, E2E_SHARD_INDEX=shard).returncode, 0)
                name = f"axiom-web-100-2-{job}-{shard or 'checks'}"
                self.assertEqual(self.commands(), [["docker", "container", "inspect", name],
                                                   ["docker", "rm", "--force", name]])
        self.calls.write_text("")
        self.assertEqual(self.run_script("cleanup", MISSING="1").returncode, 0)
        self.assertEqual(len(self.commands()), 1)
