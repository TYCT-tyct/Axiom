#!/usr/bin/env bash
# Restores one backup set onto this machine and starts the program on it (docs/ops/backup-restore.md「恢复」).
#
#   AXIOM_RESTORE_DB_IDENTITY=/etc/axiom-backup/db.key \
#   AXIOM_RESTORE_SECRETS_IDENTITY=/etc/axiom-backup/keys.key \
#   scripts/ops/restore.sh [-f deploy/compose.yaml -f deploy/compose.edge.yaml] [--env-file FILE] <set>
#
# The identities are this machine's paths to the age private keys; they are mounted read-only for the run and stay where
# they are. -f names the compose files the deployment runs, in the order it runs them; the default is
# deploy/compose.yaml alone, and a deployment with the edge passes both, so that the program comes back on the network
# and with the proxy address its edge needs.
#
# --env-file names the compose variables and is the file the set's own copy replaces: the cluster is created with the
# passwords of the set, not of the host, because a database created from other passwords would not accept them. On a
# blank machine that file needs only the bucket's settings (deploy/.env.example); the three passwords are added here as
# placeholders until step 1 installs the set's copy, and nothing is ever created with them.
#
# The bucket's location and credentials, on the other hand, are read once at the start and exported for the whole run: the
# file installed in step 1 carries the ones of the day the set was written, and those may have been rotated since, or the
# sets moved to another bucket or prefix. Step 4 would then fail to fetch the database after step 1 had already replaced
# the file this run started from.
#
# A blank environment is the supported one: no database and no call log. --overwrite starts that over by deleting the
# project's database and key ring and setting its call log aside in a volume of its own, which is what a retry after a
# failed restore needs. The restored program never copies the audit chain to the bucket the set names: that bucket holds
# the history the restore abandons, so the installed file names no replica and records which one it was.
#
# The images have to be on this machine already: building them is the deployment's build step (what `just up` does), and
# a restore that built something else would install the wrong program.
set -euo pipefail

env_file=deploy/.env
overwrite=
set_name=
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
  --overwrite)
    overwrite=yes
    shift
    ;;
  -*)
    echo "restore: unknown option $1" >&2
    exit 2
    ;;
  *)
    set_name=$1
    shift
    ;;
  esac
done
# Compose's own rule: the -f flags replace the default, they do not add to it.
[ ${#files[@]} -gt 0 ] || files=(deploy/compose.yaml)

die() {
  printf 'restore: %s\n' "$*" >&2
  exit 1
}
. "${BASH_SOURCE[0]%/*}/env-file.sh"

# Docker Desktop on Windows cannot mount a git bash path such as /tmp/tmp.abc: the daemon needs the Windows form, which
# cygpath prints. Everywhere else cygpath is absent and the path is passed through unchanged.
docker_path() {
  if command -v cygpath > /dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

[ -n "$set_name" ] || die "usage: scripts/ops/restore.sh [-f FILE]... [--env-file FILE] [--overwrite] <set>"
[ -n "${AXIOM_RESTORE_DB_IDENTITY-}" ] ||
  die "AXIOM_RESTORE_DB_IDENTITY must name the age identity that opens the database's sets"
[ -n "${AXIOM_RESTORE_SECRETS_IDENTITY-}" ] ||
  die "AXIOM_RESTORE_SECRETS_IDENTITY must name the age identity that opens the key ring"
for file in "$AXIOM_RESTORE_DB_IDENTITY" "$AXIOM_RESTORE_SECRETS_IDENTITY"; do
  [ -r "$file" ] || die "cannot read $file"
done
[ -r "$env_file" ] || die "cannot read $env_file: it needs at least the bucket's settings (deploy/.env.example)"

# The compose file requires the three passwords before it will parse. They are in the set, so until step 1 installs it the
# file carries placeholders; no cluster exists to be created with them, and step 1 replaces the whole file.
umask 077
for name in AXIOM_POSTGRES_PASSWORD AXIOM_APP_PASSWORD AXIOM_MIGRATOR_PASSWORD; do
  grep -q "^$name=." "$env_file" || printf '%s=unset-before-the-restore\n' "$name" >> "$env_file"
done

project=${COMPOSE_PROJECT_NAME:-axiom}
export COMPOSE_PROJECT_NAME=$project
compose_args=()
for file in "${files[@]}"; do
  [ -r "$file" ] || die "cannot read $file"
  compose_args+=(-f "$file")
done
compose() { docker compose "${compose_args[@]}" --env-file "$env_file" "$@"; }
echo "== compose files: ${files[*]}"

# Every image the restore uses has to be on this machine before anything is replaced, or a restore would stop halfway. They
# are not pulled: two of them are built from this tree, and a newer one from a registry could carry migrations the set
# predates, which is what step 5 warns about. The profile is named only so that the config lists the job's own image;
# nothing activates it, because `up` would then start the backup job as well.
for image in $(compose --profile ops config --images); do
  docker image inspect "$image" > /dev/null 2>&1 ||
    die "the image $image is not on this machine: build the deployment first (docs/ops/backup-restore.md「恢复」)"
done

# The bucket as this run found it: the endpoint, the bucket, the prefix and the credentials, read from the container that
# will use them, so that they are the values compose resolved and not a second reading of the file. They are exported as
# AXIOM_BACKUP_* and AXIOM_BACKUP_S3_*, because interpolation reads the environment before it reads the file: that is what
# keeps the location and the credentials of this run for the whole run, when step 1 installs the set's own copy of the
# deployment's file — which carries the credentials of the day the set was written, revoked since if they were rotated,
# and the location the sets were in before anyone moved them. The program's AXIOM_OBJECTSTORE_* is deliberately not
# exported: step 6 would then run the program against the backup bucket. As the environment rather than as options of the
# two steps, because a credential in a process list is readable by every user on the machine.
resolved=$(compose run --rm -T --no-deps --entrypoint env restore | tr -d '\r')
[ -n "$resolved" ] ||
  die "cannot read the bucket's settings out of $env_file: does it have AXIOM_BACKUP_BUCKET (deploy/.env.example)?"
pin() {
  local line
  line=$(printf '%s\n' "$resolved" | grep -m 1 "^$1=") || die "cannot read $1 from the restore service"
  export "$2=${line#*=}"
}
pin AXIOM_BACKUP_BUCKET AXIOM_BACKUP_BUCKET
pin AXIOM_BACKUP_PREFIX AXIOM_BACKUP_PREFIX
pin AXIOM_OBJECTSTORE_ENDPOINT AXIOM_BACKUP_S3_ENDPOINT
pin AXIOM_OBJECTSTORE_ACCESS_KEY_ID AXIOM_BACKUP_S3_ACCESS_KEY_ID
pin AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY AXIOM_BACKUP_S3_SECRET_ACCESS_KEY
pin AXIOM_OBJECTSTORE_REGION AXIOM_BACKUP_S3_REGION
pin AXIOM_OBJECTSTORE_PATH_STYLE AXIOM_BACKUP_S3_FORCE_PATH_STYLE
[ -n "$AXIOM_BACKUP_BUCKET" ] || die "AXIOM_BACKUP_BUCKET is empty in $env_file: nothing says where the sets are"

# Nothing may write to the database while it is replaced.
[ -z "$(compose ps --quiet axiom)" ] ||
  die "the program is running: docker compose --project-name $project stop axiom"

# The call log holds records of calls the database this volume belonged to had charged, some of them maybe after the set
# was taken; replayed into the restored database they would be calls it never charged. So the restored program starts
# with an empty one, and the old one is kept, not deleted: its records are the evidence of what was served after the
# set. The copy runs in the restore image, which is on this machine, and the original goes only once the copy is whole.
call_log="${project}_call_log"
if docker volume ls --quiet --filter "name=^${call_log}$" | grep -q .; then
  [ -n "$overwrite" ] ||
    die "this project already has a call log, written for another database: rerun with --overwrite to set it aside"
  aside="${call_log}_before_$(date -u +%Y%m%d%H%M%S)"
  ! docker volume ls --quiet --filter "name=^${aside}$" | grep -q . || die "the volume $aside exists already: try again"
  echo "== setting this project's call log aside in the volume $aside"
  # The program and the one-shot keys job are the containers that mount it, and a volume still mounted cannot go.
  compose rm --stop --force axiom keys > /dev/null
  docker volume create "$aside" > /dev/null
  compose run --rm --no-deps -T --entrypoint sh --volume "$call_log:/from:ro" --volume "$aside:/to" restore \
    -c 'cp -a /from/. /to/' || die "cannot copy the call log into $aside; the original is untouched"
  docker volume rm "$call_log" > /dev/null
fi

if docker volume ls --quiet --filter "name=^${project}_postgres$" | grep -q .; then
  [ -n "$overwrite" ] ||
    die "this project already has a database and a restore replaces it: rerun with --overwrite to delete it and start over"
  echo "== deleting this project's database and key ring"
  # Only the containers that use those volumes: `down` would also stop whatever else shares the project, such as the
  # rehearsal's stand-in for the bucket, which the restore still has to read the set from.
  compose rm --stop --force axiom migrate postgres keys > /dev/null
  docker volume rm "${project}_postgres" "${project}_keys" > /dev/null
fi

# keys is a one-shot service: the restored /keys has no valkey-password, so a fresh run generates one and replaces the
# stale users.acl in the cache. If the exited keys container is reused, it does neither, and Valkey comes up with the old
# ACL while the program reads the new password: WRONGPASS at startup. Remove both containers; the cache volume itself
# holds only rate-limit and session state, which is safe to lose and is not touched here.
compose rm --stop --force valkey keys > /dev/null || die "cannot remove the Valkey and keys containers: stop them and run the restore again"

out=$(mktemp -d)
trap 'rm -rf "$out"' EXIT

# The set's environment file first: it holds the passwords the cluster is created with, so it has to be in place before
# initdb runs, and the two steps cannot be one. As this user, so that the file it writes into the temporary directory is
# one this user can copy.
echo "== 1/6 fetch and decrypt the set's environment file"
compose run --rm --user "$(id -u):$(id -g)" --env AXIOM_RESTORE_STEP=config --env AXIOM_RESTORE_SET="$set_name" \
  --volume "$(docker_path "$AXIOM_RESTORE_DB_IDENTITY"):/identities/db.key:ro" \
  --volume "$(docker_path "$out"):/out" restore
cp "$out/deploy.env" "$env_file"
chmod 600 "$env_file"
# The cluster this creates has a new system identifier, while the WAL repository in the bucket belongs to the one that was
# lost: archive-push would fail against its stanza, and an old set without the cipher passphrase would refuse to start.
# Turn the WAL bucket off in the installed file itself, not just for one command, so every later start stays off until a
# new repository is configured; AXIOM_BACKUP_BUCKET stays for the logical sets and the restore jobs below.
env_clear "$env_file" AXIOM_BACKUP_WAL_BUCKET
# Interpolation checks the environment before the file, so the shell must agree with the line just installed.
export AXIOM_BACKUP_WAL_BUCKET=
# The audit chain's replica bucket holds the chain up to wherever the lost database had got, beyond this set. The
# restored chain carries on from the set with links of its own, so copying it there would put a second history next to
# the first; the copy refuses that, and stops (docs/domains/operations.md「审计」). Clear the six variables instead, and
# keep a line saying which bucket held the earlier history: the restored chain goes to a new bucket, which copies it
# from its first link, and the old one is kept as it is.
# The six are cleared in whichever form the file spells them (env-file.sh): the next `up` reads this file, and one of
# them left set would arm half a replica configuration, which the program refuses at startup.
replica_vars=()
for name in ENDPOINT BUCKET REGION ACCESS_KEY_ID SECRET_ACCESS_KEY PATH_STYLE; do replica_vars+=("AXIOM_AUDIT_REPLICA_$name"); done
replica=$(env_value "$env_file" AXIOM_AUDIT_REPLICA_ENDPOINT)
[ -z "$replica" ] || replica="$replica/$(env_value "$env_file" AXIOM_AUDIT_REPLICA_BUCKET)"
env_clear "$env_file" "${replica_vars[@]}"
if [ -n "$replica" ]; then
  printf '# %s: restored set %s; the audit replica %s holds the history this restore abandoned: the restored chain needs a new bucket.\n' \
    "$(date -u '+%Y-%m-%d %H:%M:%S UTC')" "$set_name" "$replica" >> "$env_file"
  echo "== the audit replica $replica is off: it holds the chain the set predates (docs/ops/backup-restore.md「恢复」)"
fi
for name in "${replica_vars[@]}"; do export "$name="; done

echo "== 2/6 create the cluster from it, with the login roles the image's init scripts make"
# The installed file has the WAL bucket empty, so this start and every later one keep the new cluster out of the old
# stanza; the logical set bucket remains for the restore jobs and for later backups.
compose up --detach --wait --wait-timeout 120 postgres

# axiom_owner and axiom_lookup are NOLOGIN and belong to the migrations, so the program creates them: every object in the
# dump belongs to one of them, and the restore cannot hand the objects over before they exist.
echo "== 3/6 create the roles the migrations own"
compose run --rm migrate

echo "== 4/6 restore the database and the key ring"
compose run --rm --env AXIOM_RESTORE_STEP=data --env AXIOM_RESTORE_SET="$set_name" \
  --volume "$(docker_path "$AXIOM_RESTORE_DB_IDENTITY"):/identities/db.key:ro" \
  --volume "$(docker_path "$AXIOM_RESTORE_SECRETS_IDENTITY"):/identities/keys.key:ro" restore

echo "== 5/6 apply the migrations the set predates, if any"
compose run --rm migrate

echo "== 6/6 start the program and wait for its own startup checks"
# `up --wait` reports only that the program exited; its own log line is what says why, and the run is about to be
# abandoned, so print it before the error.
compose up --detach --wait --wait-timeout 180 || {
  compose logs --no-color --tail=200 axiom
  die "the program did not pass its startup checks"
}

# Each answer is checked before it is printed: a curl inside an echo would report the echo's status, so a 500 or a refused
# connection would still end with "restored".
check() {
  local body
  body=$(curl --silent --show-error --fail --max-time 30 "$1") || die "no answer from $2: $1"
  [ -n "$body" ] || die "$2 answered nothing: $1"
  echo "== $2: $body"
}

# Through the edge when the file set has one, and otherwise on the program's own published ports. The edge is where a
# deployment is reached, and it is the only check that proves the two are on a network together: the loopback ports answer
# whether or not the edge can reach the program, which is how a restore could look finished while every request through
# the edge got a 502.
if compose config --services | grep -qx caddy; then
  # The hostnames and the port come from the containers themselves: they live in the environment file the set has just
  # replaced, and Caddy serves each surface under its own name.
  console_host=$(compose exec -T caddy printenv AXIOM_EDGE_CONSOLE_HOST)
  api_host=$(compose exec -T caddy printenv AXIOM_EDGE_API_HOST)
  https=$(compose port caddy 443)
  https=${https##*:}
  # The certificate is the edge's own business (ACME, or a CA of its own), so it is not verified here. --connect-to keeps
  # the request on the standard port, which is what a client of a deployment sends and what the site's hosts are
  # registered as, while the connection goes to the port the host published; the alternative, a URL with the published
  # port in it, would put that port into the Host header and the program would look up a domain nobody registered. Only a
  # request that got no answer at all is retried: a fresh edge has no certificate for the name until it has got one, and
  # that is a handshake that fails rather than a status. The status is kept, because the two surfaces are told apart by
  # it: the version endpoint answers on every host, and the site's answers on a console domain only.
  edge() {
    local out tries=0
    while :; do
      out=$(curl --silent --show-error --insecure --max-time 30 --connect-to "$1:443:127.0.0.1:$https" \
        --write-out '\n%{http_code}' "https://$1$2") && break
      tries=$((tries + 1))
      [ "$tries" -lt 15 ] || die "no answer from the edge for $1$2: is it up, and has it a certificate for $1?"
      sleep 2
    done
    status=${out##*$'\n'}
    body=${out%$'\n'*}
  }
  edge "$console_host" /api/console/v1/site
  [ "$status" = 200 ] || die "the console's site answered $status through the edge, not 200: can the edge reach the program?"
  echo "== console through the edge: $body"
  edge "$api_host" /api/console/v1/site
  [ "$status" = 404 ] ||
    die "the API host answered a console path with $status, not 404: is it registered as the site's API domain?"
  edge "$api_host" /v1/models
  [ "$status" = 401 ] || die "the model API answered $status through the edge, not the 401 a request without a key gets"
  echo "== model API through the edge: 401 without a key, which is the answer this endpoint gives one"
else
  public=$(compose port axiom 8080)
  public=${public##*:}
  admin=$(compose port axiom 8082)
  admin=${admin##*:}
  check "http://127.0.0.1:$public/api/console/v1/version" console
  check "http://127.0.0.1:$admin/api/admin/v1/version" admin
fi
compose ps
echo "== restored $set_name"
