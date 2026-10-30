# Helpers shared by backup.sh and restore.sh: the configuration checks, the object keys and the log line. Sourced, never
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

# require_objectstore_env: the backup job's own bucket and credentials. An empty endpoint means AWS S3 itself, as
# deploy/.env.example says, and the program's client requires an address, so name the one AWS publishes for the region.
# This is the backup adapter's rule, not the program configuration's: AXIOM_OBJECTSTORE_* on the axiom service still
# requires its endpoint explicitly.
require_objectstore_env() {
  require_env AXIOM_OBJECTSTORE_BUCKET AXIOM_OBJECTSTORE_ACCESS_KEY_ID AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY
  if [ -z "${AXIOM_OBJECTSTORE_ENDPOINT-}" ]; then
    # An empty AXIOM_BACKUP_S3_ENDPOINT means AWS itself, as rclone's default provider did. The program's own
    # configuration still requires the endpoint: this derivation belongs to the backup adapter.
    local region domain
    region=${AXIOM_OBJECTSTORE_REGION:-us-east-1}
    domain=amazonaws.com
    case $region in cn-*) domain=amazonaws.com.cn ;; esac
    AXIOM_OBJECTSTORE_ENDPOINT="https://s3.$region.$domain"
    export AXIOM_OBJECTSTORE_ENDPOINT
  fi
}

# require_prefix: every key the backup writes starts with AXIOM_BACKUP_PREFIX, and an empty or absolute key would put
# the sets somewhere else than the operator named.
require_prefix() {
  case ${AXIOM_BACKUP_PREFIX-} in
  '' | /*) die "AXIOM_BACKUP_PREFIX must be a non-empty key prefix without a leading slash" ;;
  esac
}

# backup_root: the key prefix of every set, with one trailing slash.
backup_root() {
  printf '%s/' "${AXIOM_BACKUP_PREFIX%/}"
}

# object_prefix SET: the prefix that holds one set's files, with its trailing slash, so a set whose name is a prefix of
# another cannot take that one's objects with it.
object_prefix() {
  printf '%s%s/' "$(backup_root)" "$1"
}
