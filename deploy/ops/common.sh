# Helpers shared by backup.sh and restore.sh: the configuration checks, the bucket path and the log line. Sourced, never
# run on its own. Both jobs are unattended, so every fault has to be a message and a non-zero exit rather than a prompt
# or a half-written set.

# die MESSAGE...: stop and name the fault.
die() {
  printf '%s: %s\n' "$PROGRAM" "$*" >&2
  exit 1
}

log() {
  printf '%s: %s\n' "$PROGRAM" "$*"
}

# require_env NAME...: report every missing name at once, the way the program reports its own configuration, so an
# operator fixes the environment file once instead of one variable per run.
require_env() {
  local name missing=""
  for name in "$@"; do
    [ -n "${!name-}" ] || missing="$missing $name"
  done
  [ -z "$missing" ] || die "configuration is missing:$missing"
}

# require_age_recipient NAME: the backup side takes public keys only, and a key age cannot parse must not get as far as
# producing a set nobody can decrypt.
require_age_recipient() {
  case ${!1-} in
  age1*) ;;
  *) die "$1 is not an age recipient (age1...)" ;;
  esac
}

# require_readable PATH...: the usual fault is an unmounted volume or identity file, which is worth naming before the
# command that would fail more obscurely. A directory where a file should be is the other one: a missing deploy/.env that
# compose created as a directory.
require_readable() {
  local path
  for path in "$@"; do
    [ -f "$path" ] || die "$path is not a readable file: is it mounted?"
  done
}

# sql QUERY: one value, unaligned, no psqlrc, no password prompt. The connection comes from the PG* environment, so it is
# configured in one place (deploy/compose.yaml).
sql() {
  psql --no-psqlrc --quiet --no-align --tuples-only --no-password --set ON_ERROR_STOP=1 --command "$1"
}

# bucket_path: the rclone path of the backup root. The remote itself ("axiom") is configured through RCLONE_CONFIG_AXIOM_*
# in the environment, so no credential is ever a command-line argument.
bucket_path() {
  printf 'axiom:%s/%s' "$AXIOM_BACKUP_BUCKET" "$AXIOM_BACKUP_PREFIX"
}
