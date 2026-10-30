#!/bin/bash
# One encrypted backup run: the database, the deployment's environment file and the key ring, uploaded to the bucket as
# one set, then the sets past their retention are dropped (docs/ops/backup-restore.md).
#
# Every run is a fresh container of the ops image (deploy/ops/Dockerfile), started by a scheduler or by hand; nothing
# here runs inside the program and nothing here talks to it.
set -euo pipefail
PROGRAM=backup
. /ops/common.sh

require_env AXIOM_BACKUP_BUCKET AXIOM_BACKUP_PREFIX AXIOM_BACKUP_DB_RECIPIENT AXIOM_BACKUP_SECRETS_RECIPIENT \
  AXIOM_BACKUP_KEEP_SETS AXIOM_BACKUP_KEEP_DAYS AXIOM_BACKUP_STATE_DIR AXIOM_KEYS_DIR AXIOM_DEPLOY_ENV_FILE \
  PGDATABASE PGPASSWORD
require_objectstore_env
require_prefix
require_age_recipient AXIOM_BACKUP_DB_RECIPIENT
require_age_recipient AXIOM_BACKUP_SECRETS_RECIPIENT
require_readable "$AXIOM_DEPLOY_ENV_FILE" "$AXIOM_KEYS_DIR/master" "$AXIOM_KEYS_DIR/hash" "$AXIOM_KEYS_DIR/identity"
# The key ring is the one secret whose loss no other copy can undo, so it never shares a recipient with the database:
# one leaked or retired identity must not open both.
[ "$AXIOM_BACKUP_DB_RECIPIENT" != "$AXIOM_BACKUP_SECRETS_RECIPIENT" ] ||
  die "AXIOM_BACKUP_DB_RECIPIENT and AXIOM_BACKUP_SECRETS_RECIPIENT must differ"
case $AXIOM_BACKUP_KEEP_SETS in '' | *[!0-9]*) die "AXIOM_BACKUP_KEEP_SETS must be a number" ;; esac
case $AXIOM_BACKUP_KEEP_DAYS in '' | *[!0-9]*) die "AXIOM_BACKUP_KEEP_DAYS must be a number" ;; esac
# Both limits are at least 1. Keeping zero sets would delete every backup the moment it finished, and keeping zero days
# would put the cutoff at this run's own second, which the age check below reads as "older than now" and drops the set it
# has just uploaded. Retention prunes; it is not the way to stop backing up (the scheduler is) and there is no value that
# means "keep forever": a number nobody reaches is how a deployment says that.
[ "$AXIOM_BACKUP_KEEP_SETS" -ge 1 ] || die "AXIOM_BACKUP_KEEP_SETS must be at least 1"
[ "$AXIOM_BACKUP_KEEP_DAYS" -ge 1 ] || die "AXIOM_BACKUP_KEEP_DAYS must be at least 1"

# Set names sort in time order, so the retention compares them as strings; the C collation keeps that true whatever
# locale the host passes in.
export LC_ALL=C

# One run at a time on this host. A second run would dump the same state and both would prune the same listing; the lock
# is a file on the state volume, so it outlives the container that took it, and a run that finds it taken exits with 75
# (EX_TEMPFAIL), which a scheduler reads as "busy, try later". It guards this machine only, which matches the deployment
# model (docs/architecture.md「部署拓扑」).
mkdir -p "$AXIOM_BACKUP_STATE_DIR"
exec 9>"$AXIOM_BACKUP_STATE_DIR/lock"
flock -n 9 || {
  log "another run holds the lock, nothing to do"
  exit 75
}

# Refuse to spend a dump on a bucket the credentials cannot address; the same HEAD an operator runs by hand.
axiom objectstore check >/dev/null || die "cannot check bucket $AXIOM_OBJECTSTORE_BUCKET"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Seconds in UTC plus six hex digits from the database, so the name is the time and the suffix keeps two runs in the same
# second from writing into one directory. The database already supplies both, which saves a second tool here.
set_name="$(sql "SELECT to_char(now() at time zone 'utc', 'YYYYMMDDHH24MISS') || '-' || substr(md5(random()::text), 1, 6)")"
log "backing up into $set_name"

# The database, as the superuser: every table forces row security, and a dump that obeyed it would silently lose rows.
# Owners and grants stay in the archive because the restore puts them back as they were.
pg_dump --no-password --format=custom --compress=zstd:9 --lock-wait-timeout=30s --file="$work/db.dump"
age --encrypt --recipient "$AXIOM_BACKUP_DB_RECIPIENT" --output "$work/db.dump.age" "$work/db.dump"
rm -f "$work/db.dump"

# The deployment's environment file: the passwords that open the restored cluster and the bucket's credentials live in
# it, so it travels with the database, to the database's recipient.
age --encrypt --recipient "$AXIOM_BACKUP_DB_RECIPIENT" --output "$work/deploy.env.age" "$AXIOM_DEPLOY_ENV_FILE"

# The key ring with its modes and owner, which the program checks before it serves anything.
tar -C "$AXIOM_KEYS_DIR" -cf "$work/keys.tar" master hash identity
age --encrypt --recipient "$AXIOM_BACKUP_SECRETS_RECIPIENT" --output "$work/keys.tar.age" "$work/keys.tar"
rm -f "$work/keys.tar"

# The manifest goes up last and is what makes a set restorable: a run that died halfway leaves a directory the restore
# refuses, and the pruning below collects it by age.
{
  printf 'set=%s\n' "$set_name"
  printf 'pg_dump=%s\n' "$(pg_dump --version)"
  printf 'server_version=%s\n' "$(sql 'SHOW server_version')"
  printf 'app_image=%s\n' "${AXIOM_BACKUP_APP_IMAGE:-unknown}"
  printf 'db_bytes=%s\n' "$(wc -c <"$work/db.dump.age" | tr -d ' ')"
  printf 'keys_bytes=%s\n' "$(wc -c <"$work/keys.tar.age" | tr -d ' ')"
  printf 'env_bytes=%s\n' "$(wc -c <"$work/deploy.env.age" | tr -d ' ')"
  printf 'db_recipient=%s\n' "$AXIOM_BACKUP_DB_RECIPIENT"
  printf 'secrets_recipient=%s\n' "$AXIOM_BACKUP_SECRETS_RECIPIENT"
} >"$work/manifest.txt"

root="$(object_prefix "$set_name")"
# A 5 GiB dump over a 2 MiB/s link needs about 43 minutes; the CLI default is 30 minutes for interactive use, so the
# unattended job gets a larger, still finite limit. The value is validated by the command.
transfer_timeout=${AXIOM_BACKUP_TRANSFER_TIMEOUT:-12h}
for file in db.dump.age deploy.env.age keys.tar.age manifest.txt; do
  content_type=application/octet-stream
  [ "$file" != manifest.txt ] || content_type=text/plain
  axiom objectstore put --timeout "$transfer_timeout" --key "$root$file" --file "$work/$file" --content-type "$content_type" >/dev/null ||
    die "cannot upload $root$file"
done
log "uploaded $root"

# Retention: keep the newest AXIOM_BACKUP_KEEP_SETS complete sets and drop anything older than AXIOM_BACKUP_KEEP_DAYS
# days. Both limits apply, so a busy month is bounded by count and a quiet one still by age. A set without a manifest is
# a failed run: it is left to the age limit rather than counted, so it neither pushes out a good set nor destroys the
# half-written set of another host.
prune() {
  local listing all_sets complete_sets newest cutoff name key rel root_prefix
  root_prefix=$(backup_root)
  listing=$(axiom objectstore list --prefix "$root_prefix" --names) || die "cannot list $root_prefix"
  all_sets="" complete_sets=""
  while IFS= read -r key; do
    [ -n "$key" ] || continue
    rel=${key#"$root_prefix"}
    name=${rel%%/*}
    [ -n "$name" ] || continue
    all_sets+="$name"$'\n'
    case $rel in */manifest.txt) complete_sets+="$name"$'\n' ;; esac
  done <<<"$listing"
  all_sets=$(printf '%s' "$all_sets" | sort -u)
  [ -n "$all_sets" ] || return 0
  complete_sets=$(printf '%s' "$complete_sets" | sort -u)
  newest=$(printf '%s\n' "$complete_sets" | tail -n "$AXIOM_BACKUP_KEEP_SETS")
  cutoff=$(sql "SELECT to_char(now() at time zone 'utc' - interval '$AXIOM_BACKUP_KEEP_DAYS days', 'YYYYMMDDHH24MISS')")
  for name in $all_sets; do
    # The set this run wrote is never dropped, whatever the two limits work out to: a run that deleted its own output
    # would report success with nothing to restore.
    [ "$name" != "$set_name" ] || continue
    if [[ $name < $cutoff ]]; then
      log "dropping $name: older than $AXIOM_BACKUP_KEEP_DAYS days"
    elif printf '%s\n' "$complete_sets" | grep -q -x -e "$name" &&
      ! printf '%s\n' "$newest" | grep -q -x -e "$name"; then
      log "dropping $name: not among the newest $AXIOM_BACKUP_KEEP_SETS"
    else
      continue
    fi
    axiom objectstore delete --prefix "$(object_prefix "$name")" >/dev/null || die "cannot drop $name"
  done
}

prune
log "done"
