#!/usr/bin/env bash
# One backup set of this machine, taken with the program stopped (docs/ops/backup-restore.md「备份」):
#
#   scripts/ops/backup.sh [-f deploy/compose.yaml -f deploy/compose.edge.yaml] [--env-file FILE]
#
# A call's record waits in the program's local call log until the database has it, while the call's charge is already in
# the ledger, so only a set dumped with the call log drained holds every record of every charge it holds. This stops the
# program, which drains the call log on its way out (within the service's stop_grace_period), runs the backup job, which
# refuses to dump unless the call log is drained and nothing holds it, and starts the program again as soon as the job
# says the dump is taken, before the encryption and the upload. A program that was not running is left as it was.
#
# The exit status is the job's: 75 while another run holds its lock, 1 for every refusal, a call log that did not drain
# included. The flags are restore.sh's: -f names the compose files the deployment runs, deploy/compose.yaml by default.
# A run takes a lock of its own, next to the environment file, before it looks at the program: a second run while the
# first one uploads exits 75 at once instead of stopping the program the first one has started again.
set -euo pipefail

env_file=deploy/.env
files=()
while [ $# -gt 0 ]; do
  case $1 in
  -f)
    files+=("$2")
    shift 2
    ;;
  --env-file)
    env_file=$2
    shift 2
    ;;
  *)
    echo "backup: usage: scripts/ops/backup.sh [-f FILE]... [--env-file FILE]" >&2
    exit 2
    ;;
  esac
done
[ ${#files[@]} -gt 0 ] || files=(deploy/compose.yaml)
compose_args=()
for file in "${files[@]}"; do compose_args+=(-f "$file"); done
compose() { docker compose "${compose_args[@]}" --env-file "$env_file" "$@"; }

# The job's own lock guards a direct `docker compose run backup` as well; this one covers the stop and the start around
# it. flock(1) is util-linux, on every Linux host.
command -v flock > /dev/null || { echo "backup: flock is missing: install util-linux" >&2; exit 1; }
exec 9>> "$env_file.backup-lock"
flock -n 9 || {
  echo "backup: another run holds $env_file.backup-lock, nothing to do"
  exit 75
}

# The line deploy/ops/backup.sh logs once pg_dump has finished.
dumped="backup: dumped the database; the program may start"

running=$(compose ps --status running --quiet axiom)
# Whatever ends this run, a program it stopped is started again: a backup must not become an outage.
restart() {
  [ -z "$running" ] || compose start axiom > /dev/null || {
    echo "backup: the program did not start again: docker compose start axiom" >&2
    return 1
  }
}
trap 'restart' EXIT
if [ -n "$running" ]; then
  echo "== stopping the program, which writes its call log into the database on the way out"
  compose stop axiom
fi

# The program comes back at the dump's line rather than at the end, so the outage is the stop and the dump, not the
# upload of the set. A start that fails here is retried by the trap and reported there.
status=0
compose run --rm -T backup | while IFS= read -r line; do
  printf '%s\n' "$line"
  if [ "$line" = "$dumped" ] && [ -n "$running" ]; then
    compose start axiom > /dev/null || true
  fi
done || status=$?
trap - EXIT
restart
exit "$status"
