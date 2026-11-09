"""The host halves of the backup and the restore (scripts/ops/backup.sh, scripts/ops/restore.sh) against a stub docker:
what they do to the program and to the environment file they install. Run with `python3 -m unittest discover -s scripts`."""

import fcntl
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

ROOT = Path(__file__).parents[1]
REAL_DOCKER = shutil.which("docker")

# Every call is recorded. The restore stops at its second step, the cluster's start: everything the test looks at is
# done by then. Step 1's job writes the set's environment file into the directory mounted at /out.
DOCKER = r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  *" config --images") echo stub-image ;;
  "image inspect "*) ;;
  *" --entrypoint env restore")
    printf '%s\n' AXIOM_BACKUP_BUCKET=sets AXIOM_BACKUP_PREFIX=axiom AXIOM_OBJECTSTORE_ENDPOINT=https://s3.example.com \
      AXIOM_OBJECTSTORE_ACCESS_KEY_ID=a AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY=s AXIOM_OBJECTSTORE_REGION=us-east-1 \
      AXIOM_OBJECTSTORE_PATH_STYLE=true ;;
  *" --env AXIOM_RESTORE_STEP=config "*)
    for arg in "$@"; do case $arg in *:/out) cp "$SET_ENV" "${arg%:/out}/deploy.env" ;; esac; done ;;
  *" up --detach --wait --wait-timeout 120 postgres") exit 1 ;;
  *" ps --status running --quiet axiom") echo "${NODE-}" ;;
  *" run --rm -T backup") echo "backup: dumped the database; the program may start"; exit "${JOB_STATUS:-0}" ;;
esac
'''

# A set's file as a deployment may write it: every assignment form Compose reads (scripts/ops/env-file.sh).
SET_ENV = "\n".join([
    "AXIOM_POSTGRES_PASSWORD=fixture",
    "AXIOM_APP_PASSWORD=fixture",
    "AXIOM_MIGRATOR_PASSWORD=fixture",
    "AXIOM_AUDIT_REPLICA_ENDPOINT=https://s3.example.com",
    "export AXIOM_AUDIT_REPLICA_BUCKET=audit",
    "  AXIOM_AUDIT_REPLICA_REGION = us-east-1",
    'export   AXIOM_AUDIT_REPLICA_ACCESS_KEY_ID="A"',
    "AXIOM_AUDIT_REPLICA_SECRET_ACCESS_KEY='S'   # the replica's key",
    "AXIOM_AUDIT_REPLICA_PATH_STYLE=true\r",
    "export AXIOM_BACKUP_WAL_BUCKET=wal",
]) + "\n"
CLEARED = ["AXIOM_AUDIT_REPLICA_" + n for n in
           ("ENDPOINT", "BUCKET", "REGION", "ACCESS_KEY_ID", "SECRET_ACCESS_KEY", "PATH_STYLE")] + ["AXIOM_BACKUP_WAL_BUCKET"]


def clean_environment(root: Path, **extra: str) -> dict:
    environment = {k: v for k, v in os.environ.items() if not k.startswith(("AXIOM_", "COMPOSE_", "DEPLOY_"))}
    environment.update({"PATH": f"{root}:{os.environ['PATH']}", "CALLS": str(root / "calls"), **extra})
    return environment


def stub(root: Path) -> None:
    docker = root / "docker"
    docker.write_text(DOCKER)
    docker.chmod(0o755)


def values(env_file: Path) -> dict:
    """The file's AXIOM_* settings as the scripts read them: the last assignment of each name."""
    out = subprocess.run([BASH, "-c", '. scripts/ops/env-file.sh; env_assignments "$1"', "_", str(env_file)],
                         cwd=ROOT, text=True, capture_output=True, check=True).stdout
    return dict(line.split("\t", 1) for line in out.splitlines())


@unittest.skipUnless(BASH, "Bash is unavailable")
class Restore(unittest.TestCase):
    def test_the_installed_file_names_no_replica_and_no_wal_bucket_however_the_set_spelled_them(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            stub(root)
            (root / "set.env").write_text(SET_ENV)
            env_file = root / "deployment.env"
            env_file.write_text("AXIOM_BACKUP_BUCKET=sets\n")
            for name in ("db.key", "keys.key"):
                (root / name).write_text("identity\n")
            result = subprocess.run(
                [BASH, "scripts/ops/restore.sh", "--env-file", str(env_file), "20261109000000-abcdef"], cwd=ROOT,
                env=clean_environment(root, SET_ENV=str(root / "set.env"), AXIOM_RESTORE_DB_IDENTITY=str(root / "db.key"),
                                      AXIOM_RESTORE_SECRETS_IDENTITY=str(root / "keys.key")),
                text=True, capture_output=True)
            self.assertIn("up --detach --wait --wait-timeout 120 postgres", (root / "calls").read_text(), result.stderr)
            installed = values(env_file)
            self.assertEqual({n: installed.get(n) for n in CLEARED}, {n: "" for n in CLEARED})
            self.assertEqual(installed["AXIOM_POSTGRES_PASSWORD"], "fixture")
            self.assertIn("the audit replica https://s3.example.com/audit holds the history", env_file.read_text())
            if REAL_DOCKER:
                # The next start reads the file through Compose: the set armed a whole replica, the installed file none.
                for path, want in ((root / "set.env", "audit"), (env_file, "")):
                    self.assertEqual(self.replica_bucket(path), want, path.name)

    @staticmethod
    def replica_bucket(env_file: Path) -> str:
        config = subprocess.run([REAL_DOCKER, "compose", "-f", "deploy/compose.yaml", "--env-file", str(env_file),
                                 "config", "--format", "json"], cwd=ROOT, env=clean_environment(Path("/nonexistent")),
                                text=True, capture_output=True, check=True).stdout
        return json.loads(config)["services"]["axiom"]["environment"]["AXIOM_AUDIT_REPLICA_BUCKET"]


@unittest.skipUnless(BASH, "Bash is unavailable")
class Backup(unittest.TestCase):
    def run_backup(self, root: Path, env_file: Path, **state: str) -> tuple[subprocess.CompletedProcess, list]:
        result = subprocess.run([BASH, "scripts/ops/backup.sh", "--env-file", str(env_file)], cwd=ROOT,
                                env=clean_environment(root, **state), text=True, capture_output=True)
        calls = (root / "calls").read_text().splitlines() if (root / "calls").exists() else []
        return result, [c.split(str(env_file) + " ", 1)[-1] for c in calls]

    def test_a_running_program_is_stopped_for_the_dump_and_started_again(self):
        for name, status in (("success", "0"), ("refusal", "1")):
            with self.subTest(name), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                stub(root)
                env_file = root / "deployment.env"
                env_file.write_text("")
                result, calls = self.run_backup(root, env_file, NODE="n1", JOB_STATUS=status)
                self.assertEqual(result.returncode, int(status), result.stderr)
                self.assertEqual([c for c in calls if c.split()[0] in ("stop", "run", "start")],
                                 ["stop axiom", "run --rm -T backup", "start axiom", "start axiom"])

    def test_a_second_run_leaves_the_program_alone(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            stub(root)
            env_file = root / "deployment.env"
            env_file.write_text("")
            with open(f"{env_file}.backup-lock", "a") as held:
                fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result, calls = self.run_backup(root, env_file, NODE="n1")
            self.assertEqual(result.returncode, 75, result.stderr)
            self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
