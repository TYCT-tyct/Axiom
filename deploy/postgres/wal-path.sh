#!/bin/sh
# Derives the pgBackRest repository path from the two backup prefixes. It is sourced by every process that talks to the
# repository (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」), so the server's archive commands, the jobs, the
# restore and `docker compose exec postgres pgbackrest ...` agree.
#
# The logical sets live under AXIOM_BACKUP_PREFIX and their retention lists and deletes that whole prefix. The WAL
# repository must not be equal to it or nested under it, or a prune would delete the base backups and WAL with the sets.
# A leading or trailing slash is normalized before the default <prefix>-pgbackrest is derived. An explicit
# PGBACKREST_REPO1_PATH is checked against the same rule.
wal_repo_path() {
	logical=${AXIOM_BACKUP_PREFIX:-axiom}
	while [ "${logical#/}" != "$logical" ]; do logical=${logical#/}; done
	while [ "${logical%/}" != "$logical" ]; do logical=${logical%/}; done
	[ -n "$logical" ] || {
		printf 'pgbackrest: AXIOM_BACKUP_PREFIX is empty after removing slashes\n' >&2
		exit 1
	}

	path=${PGBACKREST_REPO1_PATH:-}
	while [ "${path#/}" != "$path" ]; do path=${path#/}; done
	while [ "${path%/}" != "$path" ]; do path=${path%/}; done
	if [ -z "$path" ]; then
		if [ -n "${AXIOM_BACKUP_WAL_PREFIX:-}" ]; then
			path=${AXIOM_BACKUP_WAL_PREFIX}
			while [ "${path#/}" != "$path" ]; do path=${path#/}; done
			while [ "${path%/}" != "$path" ]; do path=${path%/}; done
			[ -n "$path" ] || {
				printf 'pgbackrest: AXIOM_BACKUP_WAL_PREFIX is empty after removing slashes\n' >&2
				exit 1
			}
		else
			path="$logical-pgbackrest"
		fi
	fi

	overlap() {
		printf 'pgbackrest: WAL prefix "%s" overlaps the logical backup prefix "%s"\n' "$path" "$logical" >&2
		exit 1
	}
	case "$path/" in
	"$logical/"*) overlap ;;
	esac
	case "$logical/" in
	"$path/"*) overlap ;;
	esac

	printf '/%s\n' "$path"
}

# Executed directly (the container test does), print the path; sourced by the scripts above, only define the function.
if [ "${0##*/}" = "wal-path.sh" ]; then
	wal_repo_path
fi
