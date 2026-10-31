#!/bin/sh
# One base backup into the same repository as the WAL archive (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」).
# The host schedules it; the logical sets in deploy/ops remain the disaster-recovery copy that does not need this
# repository or its passphrase.
set -eu

. /usr/local/bin/wal-path.sh
PGBACKREST_REPO1_PATH=$(wal_repo_path)
export PGBACKREST_REPO1_PATH

[ -n "${PGBACKREST_REPO1_S3_BUCKET:-}" ] || {
	printf 'basebackup: PGBACKREST_REPO1_S3_BUCKET is not set\n' >&2
	exit 1
}

# A full backup every run: retention is seven of them, and a PITR never depends on an incremental chain that a failed
# run could break.
exec pgbackrest --stanza=axiom --type=full --log-level-console=info backup
