"""run_published (scripts/ci/publish.sh) against a fake docker: which ports it asks for and when it tries again."""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "scripts/ci/publish.sh"
DOCKER = '''#!{python}
import json, os, sys
with open(os.environ["CALLS"], "a") as log:
    log.write(json.dumps(sys.argv[1:]) + "\\n")
if sys.argv[1] == "run":
    with open(os.environ["CALLS"]) as log:
        runs = sum(json.loads(line)[0] == "run" for line in log)
    failures = os.environ["FAILURES"].split("|") if os.environ["FAILURES"] else []
    if runs <= len(failures):
        print(failures[runs - 1], file=sys.stderr)
        sys.exit(125)
    print("container-id")
'''
LOST = ("docker: Error response from daemon: failed to set up container networking: driver failed programming external "
        "connectivity on endpoint x: failed to bind host port 0.0.0.0:46182/tcp: address already in use")


@unittest.skipUnless(sys.platform == "linux", "the CI scripts run on Linux")
class RunPublished(unittest.TestCase):
    def run_published(self, *failures):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            docker = root / "docker"
            docker.write_text(DOCKER.format(python=sys.executable), encoding="utf-8")
            docker.chmod(0o755)
            calls = root / "calls.jsonl"
            calls.touch()
            result = subprocess.run(
                ["bash", "-c", f'. "{SCRIPT}"; run_published registry 5000 --label x registry:3.0.0'],
                env=dict(os.environ, PATH=f"{root}{os.pathsep}{os.environ['PATH']}", CALLS=str(calls),
                         FAILURES="|".join(failures)),
                capture_output=True, text=True)
            return result, [json.loads(line) for line in calls.read_text().splitlines()]

    def test_publishes_on_loopback_below_the_ephemeral_range_and_prints_the_port(self):
        result, calls = self.run_published()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(calls), 1)
        run = calls[0]
        self.assertEqual(run[:4], ["run", "--detach", "--name", "registry"])
        self.assertEqual(run[-3:], ["--label", "x", "registry:3.0.0"])
        published = run[run.index("-p") + 1]
        host = int(result.stdout)
        self.assertEqual(published, f"127.0.0.1:{host}:5000")
        self.assertTrue(20000 <= host <= 29999, host)

    def test_tries_again_only_while_the_port_is_lost(self):
        for failures, ok, runs in (((LOST, LOST), True, 3), (("No such image",), False, 1), ((LOST,) * 8, False, 8)):
            with self.subTest(failures=failures[0][:20], tries=len(failures)):
                result, calls = self.run_published(*failures)
                self.assertEqual(result.returncode == 0, ok, result.stderr)
                self.assertEqual(sum(call[0] == "run" for call in calls), runs)
                # a lost try removes the container it created; another failure leaves the caller's cleanup to it
                self.assertEqual(sum(call[:2] == ["rm", "--force"] for call in calls),
                                 min(len(failures), runs) if failures[0] == LOST else 0)
                if not ok:
                    self.assertIn(failures[-1], result.stderr)


if __name__ == "__main__":
    unittest.main()
