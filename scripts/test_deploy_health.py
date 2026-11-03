import json
import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).parents[1]


@unittest.skipUnless(shutil.which("docker"), "Docker Compose is unavailable")
class PostgresHealthcheck(unittest.TestCase):
    def test_start_period_covers_a_crash_recovery_and_the_probe_is_over_tcp(self):
        env = {**os.environ, "AXIOM_POSTGRES_PASSWORD": "fixture", "AXIOM_APP_PASSWORD": "fixture",
               "AXIOM_MIGRATOR_PASSWORD": "fixture"}
        result = subprocess.run(["docker", "compose", "-f", str(ROOT / "deploy" / "compose.yaml"),
                                 "config", "--format", "json"], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        health = json.loads(result.stdout)["services"]["postgres"]["healthcheck"]

        # Compose writes durations as Go duration strings: 2m0s, 1m30s, 30s.
        match = re.fullmatch(r"(?:(\d+)m)?(?:(\d+)s)?", health["start_period"])
        self.assertIsNotNone(match, health["start_period"])
        minutes, seconds = (int(value or 0) for value in match.groups())
        # The shared runner's recovery was still progressing at 48 seconds (#605); a shorter period marks a database
        # that is replaying WAL as unhealthy before it can accept connections.
        self.assertGreaterEqual(minutes * 60 + seconds, 120, health["start_period"])
        self.assertEqual(health["retries"], 3)
        self.assertEqual(health["interval"], "10s")
        self.assertIn("--host", health["test"])
        self.assertEqual(health["test"][health["test"].index("--host") + 1], "127.0.0.1")


if __name__ == "__main__":
    unittest.main()
