#!/bin/sh
# The database's entrypoint: the base image's own entrypoint, plus continuous WAL archiving when the backup bucket is
# configured (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」). With no bucket the arguments are untouched and the
# server starts with archive_mode off, so a deployment that does not back up behaves exactly as before.
#
# archive_mode is a start-time setting, so it is passed as a server argument instead of being written to
# postgresql.conf: the image is the same with and without a bucket, and the setting is not part of PGDATA, so a
# restored cluster never starts archiving into the repository it was restored from.
set -eu

if [ -n "${PGBACKREST_REPO1_S3_BUCKET:-}" ]; then
	missing=""
	[ -n "${PGBACKREST_REPO1_PATH-}" ] || missing="$missing PGBACKREST_REPO1_PATH"
	[ -n "${PGBACKREST_REPO1_S3_KEY-}" ] || missing="$missing PGBACKREST_REPO1_S3_KEY"
	[ -n "${PGBACKREST_REPO1_S3_KEY_SECRET-}" ] || missing="$missing PGBACKREST_REPO1_S3_KEY_SECRET"
	[ -n "${PGBACKREST_REPO1_CIPHER_PASS-}" ] || missing="$missing PGBACKREST_REPO1_CIPHER_PASS"
	[ -z "$missing" ] || {
		printf 'postgres: WAL archiving is configured but missing:%s\n' "$missing" >&2
		exit 1
	}
	# Only the server takes these arguments; a one-off client command keeps the image's own behaviour.
	case "${1:-postgres}" in
	postgres | -*) ;;
	*) exec docker-entrypoint.sh "$@" ;;
	esac
	# A quiet cluster would otherwise leave the last segment unarchived until it fills: the archive timeout bounds the
	# window a restore can lose to this value. Each switch costs one compressed segment, not 16 MB on the wire.
	set -- "$@" \
		-c archive_mode=on \
		-c 'archive_command=pgbackrest --stanza=axiom archive-push %p' \
		-c 'restore_command=pgbackrest --stanza=axiom archive-get %f %p' \
		-c archive_timeout=300
fi

exec docker-entrypoint.sh "$@"
