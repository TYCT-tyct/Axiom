#!/bin/sh
# Point-in-time restore: fetch the newest base backup into this container's empty data directory, replay WAL up to
# AXIOM_PITR_TARGET, promote, and start PostgreSQL (docs/ops/backup-restore.md「时间点恢复」).
#
# This runs in a container of its own with a fresh volume, never in the source database's container: a restore writes
# every file of the cluster, and starting from an empty directory is the only state the runbook can check. A second start
# on a directory that already holds the restored cluster is refused instead of silently starting it, because the target
# time is an input, not a property of the data.
set -eu

[ -n "${PGBACKREST_REPO1_S3_BUCKET:-}" ] || {
	printf 'pitr: PGBACKREST_REPO1_S3_BUCKET is not set: there is no archive to restore from\n' >&2
	exit 1
}
[ -n "${AXIOM_PITR_TARGET:-}" ] || {
	printf 'pitr: set AXIOM_PITR_TARGET to the time to restore to, for example 2026-10-30 12:00:00+00\n' >&2
	exit 1
}
if [ -n "$(ls -A "$PGDATA" 2>/dev/null)" ]; then
	printf 'pitr: %s is not empty; remove the restore volume to restore again\n' "$PGDATA" >&2
	exit 1
fi

# pgBackRest wants the path to exist; the volume is owned by this container's user, so creating it here is enough.
mkdir -p "$PGDATA"
pgbackrest --stanza=axiom --pg1-path="$PGDATA" --type=time --target="$AXIOM_PITR_TARGET" \
	--target-action=promote --log-level-console=info restore

# The restored cluster carries pgBackRest's restore_command in postgresql.auto.conf; PostgreSQL replays up to the target
# and promotes. It starts without archive_mode: this instance is for verification, and its WAL must not enter the
# repository of the database it was restored from.
exec docker-entrypoint.sh postgres
