import json
import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).parents[1]


def duration_seconds(value):
    """Parse a Compose duration (Go duration string): 2m0s, 1m30s, 30s."""
    match = re.fullmatch(r"(?:(\d+)m)?(?:(\d+)s)?", value)
    if match is None:
        raise AssertionError(f"not a Compose duration: {value}")
    minutes, seconds = (int(part or 0) for part in match.groups())
    return minutes * 60 + seconds


@unittest.skipUnless(shutil.which("docker"), "Docker Compose is unavailable")
class PostgresHealthcheck(unittest.TestCase):
    def healthcheck(self):
        env = {**os.environ, "AXIOM_POSTGRES_PASSWORD": "fixture", "AXIOM_APP_PASSWORD": "fixture",
               "AXIOM_MIGRATOR_PASSWORD": "fixture"}
        result = subprocess.run(["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                                 "config", "--format", "json"], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(result.stdout)["services"]["postgres"]["healthcheck"]

    def test_start_period_covers_a_crash_recovery_and_the_probe_is_over_tcp(self):
        health = self.healthcheck()
        # The shared runner's recovery was still progressing at 48 seconds (#605); a shorter period marks a database
        # that is replaying WAL as unhealthy before it can accept connections.
        self.assertGreaterEqual(duration_seconds(health["start_period"]), 120, health["start_period"])
        self.assertEqual(health["retries"], 3)
        self.assertEqual(health["interval"], "10s")
        self.assertIn("--host", health["test"])
        self.assertEqual(health["test"][health["test"].index("--host") + 1], "127.0.0.1")

    def test_the_production_wait_covers_the_healthcheck_budget(self):
        health = self.healthcheck()
        # A crash-looping container resets its start period, so the production entry's final bound, not the healthcheck,
        # ends a wait that never becomes healthy; it must leave room for the start period and every failed probe.
        budget = duration_seconds(health["start_period"]) + duration_seconds(health["interval"]) * health["retries"]
        script = (ROOT / "scripts" / "ops" / "deploy.sh").read_text()
        match = re.search(r"up --detach --no-build --wait --wait-timeout (\S+) postgres", script)
        self.assertIsNotNone(match, "the production entry must bound the postgres wait")
        self.assertGreaterEqual(duration_seconds(match.group(1)), budget)


if __name__ == "__main__":
    unittest.main()
