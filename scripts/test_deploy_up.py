import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "scripts" / "ops" / "up.sh"
REAL_DOCKER = shutil.which("docker")

# The daemon's state comes from the environment; the configuration before interpolation is the real Compose's when
# REAL_DOCKER is set, so the env-file check is tested against the files it reads in production.
DOCKER = r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  *" config --no-interpolate "*) [ -z "${REAL_DOCKER-}" ] || exec "$REAL_DOCKER" "$@" ;;
  *" config --hash postgres") echo "postgres $WANT_HASH" ;;
  *" config --format json") echo '{"services":{"postgres":{"image":"axiom-postgres:local"}}}' ;;
  *" config --services") printf '%s\n' postgres keys valkey migrate axiom caddy ;;
  *" ps --status running --quiet axiom") echo "${NODE-}" ;;
  *" ps --status running --quiet postgres") echo "${DB-}" ;;
  "inspect --format {{index .Config.Labels "*) echo "$HAVE_HASH" ;;
  "inspect --format {{.Image}} "*) echo "$HAVE_IMAGE" ;;
  "image inspect --format {{.Id}} "*) echo "$WANT_IMAGE" ;;
esac
'''
PASSWORDS = ["AXIOM_POSTGRES_PASSWORD=fixture", "AXIOM_APP_PASSWORD=fixture", "AXIOM_MIGRATOR_PASSWORD=fixture"]
SAME = {"WANT_HASH": "h1", "HAVE_HASH": "h1", "WANT_IMAGE": "sha256:1", "HAVE_IMAGE": "sha256:1"}


@unittest.skipUnless(BASH, "Bash is unavailable")
class Up(unittest.TestCase):
    def run_up(self, *args, settings=(), real_compose=False, **state):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            stub = root / "docker"
            stub.write_text(DOCKER)
            stub.chmod(0o755)
            env_file = root / "deployment.env"
            # Without the real configuration nothing is known to be read, so the stubbed runs carry no settings.
            env_file.write_text("\n".join((PASSWORDS if real_compose else []) + list(settings)) + "\n")
            calls = root / "calls"
            environment = {key: value for key, value in os.environ.items()
                           if not key.startswith(("AXIOM_", "COMPOSE_", "DEPLOY_"))}
            environment.update({"PATH": f"{root}:{os.environ['PATH']}", "CALLS": str(calls), **SAME, **state})
            if real_compose:
                environment["REAL_DOCKER"] = REAL_DOCKER
            result = subprocess.run([BASH, str(SCRIPT), *args, "--env-file", str(env_file)],
                                    env=environment, cwd=ROOT, text=True, capture_output=True)
            lines = calls.read_text().splitlines() if calls.exists() else []
            # The command after the compose prefix: "-f F... --env-file E <command>".
            commands = [line.split(str(env_file) + " ", 1)[-1] for line in lines]
            return result, commands

    def test_the_database_is_replaced_only_when_no_node_is_running(self):
        everything = "up --detach --no-build --wait"
        without_db = "up --detach --no-build --wait --no-deps keys valkey migrate axiom caddy"
        database = ["build postgres", "up --detach --no-build --wait --wait-timeout 300 postgres",
                    "exec -T postgres sh", everything]
        cases = [
            ("first start", [], {}, database),
            ("database down", [], {"NODE": "n1"}, database),
            ("node down", [], {"DB": "d1"}, database),
            ("release with a running node", [], {"NODE": "n1", "DB": "d1"}, ["exec -T postgres sh", without_db]),
            ("database upgrade drains the node first", ["--database"], {"NODE": "n1", "DB": "d1", "HAVE_HASH": "h0"},
             ["stop axiom"] + database),
        ]
        for name, args, state, want in cases:
            with self.subTest(name):
                result, commands = self.run_up(*args, **state)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual([c for c in commands if c.split()[0] in ("build", "up", "exec", "stop")], want)

    def test_a_changed_database_under_a_running_node_changes_nothing(self):
        for name, state in (("environment", {"HAVE_HASH": "h0"}), ("image", {"HAVE_IMAGE": "sha256:0"})):
            with self.subTest(name):
                result, commands = self.run_up(NODE="n1", DB="d1", **state)
                self.assertEqual(result.returncode, 1)
                self.assertIn("--database", result.stderr)
                self.assertFalse([c for c in commands if c.split()[0] in ("build", "up", "exec", "stop")], commands)

    @unittest.skipUnless(REAL_DOCKER, "Docker Compose is unavailable")
    def test_settings_no_service_reads_are_refused_before_anything_starts(self):
        edge = ("-f", "deploy/compose.yaml", "-f", "deploy/compose.edge.yaml")
        supported = ["AXIOM_OPERATIONS_ADDR=:8084", "AXIOM_OPERATIONS_SOURCES=10.0.0.0/8", "AXIOM_CORE_NODES=2",
                     "AXIOM_BYOK=on", "AXIOM_DIRECT_FORMATS=on", "AXIOM_SMTP_HOST=smtp.example",
                     "AXIOM_BACKUP_BUCKET=sets", "AXIOM_KEYRING_GENERATE=false", "AXIOM_UNUSED=", 'AXIOM_QUOTED=""',
                     "# AXIOM_COMMENTED=1"]
        cases = [
            ("supported settings", (), supported, None),
            ("a misspelled name", (), ["AXIOM_BYOOK=on"], "AXIOM_BYOOK"),
            ("a setting the file fixes", (), ["AXIOM_SHUTDOWN_TIMEOUT=60s", "export AXIOM_PUBLIC_ADDR=:9000"],
             "AXIOM_SHUTDOWN_TIMEOUT AXIOM_PUBLIC_ADDR"),
            ("trusted proxies without the edge", (), ["AXIOM_TRUSTED_PROXIES=10.0.0.1"], None),
            # The edge fixes the program's trusted proxy to its own address.
            ("trusted proxies with the edge", edge, ["AXIOM_TRUSTED_PROXIES=10.0.0.1"], "AXIOM_TRUSTED_PROXIES"),
            ("the edge's settings without the edge", (), ["AXIOM_EDGE_API_HOST=api.example"], "AXIOM_EDGE_API_HOST"),
        ]
        for name, files, settings, refused in cases:
            with self.subTest(name):
                result, commands = self.run_up(*files, settings=settings, real_compose=True)
                if refused is None:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    continue
                self.assertEqual(result.returncode, 1)
                self.assertIn(f"sets {refused}, which no service", result.stderr)
                self.assertEqual(len(commands), 1, commands)  # only the configuration was read


if __name__ == "__main__":
    unittest.main()
