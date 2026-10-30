#!/bin/bash
# One step of a restore, run in the ops image (deploy/ops/Dockerfile) by scripts/ops/restore.sh, which explains the order
# and calls the steps: `config` writes the set's environment file where that script can install it, `data` puts the
# database and the key ring back (docs/ops/backup-restore.md「恢复」).
#
# The two are separate runs because a cluster is created from the environment file: its passwords have to exist before
# initdb, and initdb has to finish before anything is restored into it.
set -euo pipefail
PROGRAM=restore
. /ops/common.sh

require_env AXIOM_RESTORE_STEP AXIOM_RESTORE_SET AXIOM_BACKUP_BUCKET AXIOM_BACKUP_PREFIX
require_objectstore_env
require_prefix
case $AXIOM_RESTORE_STEP in
config) require_env AXIOM_RESTORE_OUT_DIR AXIOM_RESTORE_DB_IDENTITY_FILE ;;
data) require_env AXIOM_KEYS_DIR AXIOM_RESTORE_DB_IDENTITY_FILE AXIOM_RESTORE_SECRETS_IDENTITY_FILE PGDATABASE PGPASSWORD ;;
*) die "AXIOM_RESTORE_STEP must be config or data, not '$AXIOM_RESTORE_STEP'" ;;
esac

export LC_ALL=C
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
root="$(object_prefix "$AXIOM_RESTORE_SET")"

fetch() {
  axiom objectstore get --key "$root$1" --file "$work/$1" >/dev/null || die "cannot fetch $root$1"
}

# The manifest is written last by the backup, so its presence is what makes a set restorable, and its contents are the
# record of what this restore installed.
fetch manifest.txt
sed 's/^/  /' "$work/manifest.txt"

case $AXIOM_RESTORE_STEP in
config)
  require_readable "$AXIOM_RESTORE_DB_IDENTITY_FILE"
  [ -w "$AXIOM_RESTORE_OUT_DIR" ] || die "$AXIOM_RESTORE_OUT_DIR is not writable: is it mounted?"
  fetch deploy.env.age
  age --decrypt --identity "$AXIOM_RESTORE_DB_IDENTITY_FILE" --output "$work/deploy.env" "$work/deploy.env.age"
  # The cluster is created from this file: a set that decrypts to something the compose file cannot use has to stop
  # here, not three steps later as "password authentication failed for user axiom_app".
  for name in AXIOM_POSTGRES_PASSWORD AXIOM_APP_PASSWORD AXIOM_MIGRATOR_PASSWORD; do
    grep -q "^$name=..*" "$work/deploy.env" || die "$name is not set in the restored environment file"
  done
  cp "$work/deploy.env" "$AXIOM_RESTORE_OUT_DIR/deploy.env"
  chmod 600 "$AXIOM_RESTORE_OUT_DIR/deploy.env"
  log "wrote $AXIOM_RESTORE_OUT_DIR/deploy.env"
  ;;
data)
  require_readable "$AXIOM_RESTORE_DB_IDENTITY_FILE" "$AXIOM_RESTORE_SECRETS_IDENTITY_FILE"
  fetch db.dump.age
  age --decrypt --identity "$AXIOM_RESTORE_DB_IDENTITY_FILE" --output "$work/db.dump" "$work/db.dump.age"
  # `axiom migrate` ran before this step to create the roles the dump's objects belong to, and it also created the
  # schemas, tables and version rows the dump recreates; a dump cannot be restored over its own leftovers. --clean is not
  # enough: a partition inherits its constraints from its parent, and a drop of an inherited constraint is refused. Every
  # schema migrations make is owned by axiom_owner, so dropping those and nothing else puts the database back to what the
  # migrations would leave in an empty one.
  sql "SET client_min_messages = warning;
  DO \$\$
  DECLARE name text;
  BEGIN
    FOR name IN SELECT nspname FROM pg_namespace WHERE nspowner = 'axiom_owner'::regrole LOOP
      EXECUTE format('DROP SCHEMA %I CASCADE', name);
    END LOOP;
  END
  \$\$;"
  # One transaction, so a restore that fails leaves no half-restored objects behind. The drop above is a no-op on a
  # database that has none of those schemas, so running the whole step again is what recovers from a failure.
  pg_restore --no-password --single-transaction --dbname "$PGDATABASE" "$work/db.dump"
  rm -f "$work/db.dump" "$work/db.dump.age"
  log "restored platform migration $(sql 'SELECT max(version_id) FROM platform.goose_platform')"

  fetch keys.tar.age
  age --decrypt --identity "$AXIOM_RESTORE_SECRETS_IDENTITY_FILE" --output "$work/keys.tar" "$work/keys.tar.age"
  tar -C "$AXIOM_KEYS_DIR" -xpf "$work/keys.tar"
  # The program checks these before it serves anything: three regular files, owned by the user it runs as, with no access
  # for anyone else. The archive carries them, but a restore next to a different uid would fail that check, so set them
  # here as well (deploy/Dockerfile runs as 65532).
  chown 65532:65532 "$AXIOM_KEYS_DIR/master" "$AXIOM_KEYS_DIR/hash" "$AXIOM_KEYS_DIR/identity"
  chmod 0400 "$AXIOM_KEYS_DIR/master" "$AXIOM_KEYS_DIR/hash" "$AXIOM_KEYS_DIR/identity"
  for name in master hash identity; do
    [ -f "$AXIOM_KEYS_DIR/$name" ] || die "$AXIOM_KEYS_DIR/$name is not a regular file"
  done
  log "wrote the key ring into $AXIOM_KEYS_DIR"
  ;;
esac
