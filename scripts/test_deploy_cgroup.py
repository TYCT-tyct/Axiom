import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

ROOT = Path(__file__).parents[1]


@unittest.skipUnless(shutil.which("docker"), "Docker Compose is unavailable")
class DeploymentCgroup(unittest.TestCase):
    def test_compose_set_unset_and_empty_cover_every_rehearsal_service(self):
        for parent in (None, "", "axiom-ci.slice"):
            with self.subTest(parent=parent):
                env = {**os.environ, "AXIOM_POSTGRES_PASSWORD": "fixture", "AXIOM_APP_PASSWORD": "fixture",
                       "AXIOM_MIGRATOR_PASSWORD": "fixture"}
                env.pop("AXIOM_TEST_CGROUP_PARENT", None)
                if parent is not None:
                    env["AXIOM_TEST_CGROUP_PARENT"] = parent
                result = subprocess.run(["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                                         "config", "--format", "json"], env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                services = json.loads(result.stdout)["services"]
                self.assertTrue({"postgres", "valkey", "axiom", "migrate", "keys"}.issubset(services))
                for name, service in services.items():
                    if not service.get("profiles"):
                        self.assertEqual(service.get("cgroup_parent", ""), parent or "", name)


@unittest.skipUnless(BASH, "Bash is unavailable")
class RehearsalInspection(unittest.TestCase):
    def test_all_containers_including_setup_are_checked_before_removal(self):
        script = (ROOT / "scripts" / "ci" / "deploy-check.sh").read_text()
        start = re.search(r"^start\(\).*?^}", script, re.M | re.S)[0]
        stubs = r'''
timeout() { :; }
compose() {
  case "$*" in
    'ps --all --quiet')
      [ "$INVENTORY_STATUS" = 0 ] || return "$INVENTORY_STATUS"
      printf '%s\n' postgres valkey axiom migrate keys ;;
    'port axiom 8080'|'port axiom 8082') echo 127.0.0.1:12345 ;;
    *) return 99 ;;
  esac
}
docker() {
  if [ "$3" = '{{.HostConfig.CgroupParent}}' ]; then
    echo "$4" >> "$LOG"
    printf '%s\n' "$ACTUAL_PARENT"
  fi
}
'''
        for parent in (None, "", "axiom-ci.slice"):
            for actual, inventory in ((parent or "", 0), ("daemon-default.slice", 0), (parent or "", 37)):
                with self.subTest(parent=parent, actual=actual, inventory=inventory), tempfile.TemporaryDirectory() as tmp:
                    log = Path(tmp) / "inspect"
                    env = {**os.environ, "ACTUAL_PARENT": actual, "INVENTORY_STATUS": str(inventory),
                           "LOG": log.as_posix()}
                    env.pop("AXIOM_TEST_CGROUP_PARENT", None)
                    if parent is not None:
                        env["AXIOM_TEST_CGROUP_PARENT"] = parent
                    result = subprocess.run([BASH, "-eu", "-c", stubs + start + "\nstart"], env=env,
                                            text=True, capture_output=True)
                    self.assertEqual(result.returncode, inventory or (1 if parent and actual != parent else 0), result.stderr)
                    if result.returncode == 0:
                        self.assertEqual(log.read_text().splitlines(), ["postgres", "valkey", "axiom", "migrate", "keys"])
