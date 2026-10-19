#!/usr/bin/env bash
# The backup rehearsal: a stack of its own, a local S3 server in place of the bucket, the database and the key ring wiped
# and restored from a set, and what came back compared with what was there (docs/ops/backup-restore.md「恢复演练」).
#
#   just backup-rehearsal
#
# Nothing here reaches a real bucket or a deployment, and everything it creates is removed again, volumes included. It
# owns the project axiom-rehearsal, so it runs one at a time on a machine, and it needs what `just up` needs (go, node,
# pnpm, docker compose) plus about ten minutes.
set -euo pipefail

project=axiom-rehearsal
env_file=deploy/.env.rehearsal
bucket=axiom-rehearsal
prefix=axiom
work=$(mktemp -d)

export COMPOSE_PROJECT_NAME=$project
export DEPLOY_IMAGE=axiom-rehearsal:local
export DEPLOY_OPS_IMAGE=axiom-ops-rehearsal:local
export DEPLOY_RESTART_POLICY=no # nothing may come back after a killed run
export DEPLOY_PORT= DEPLOY_ADMIN_PORT= # ports the kernel picks, so a leftover stack cannot hold this one's

die() {
  printf 'rehearsal: %s\n' "$*" >&2
  exit 1
}

compose() { docker compose -f deploy/compose.yaml -f deploy/compose.rehearsal.yaml --env-file "$env_file" "$@"; }

# Docker Desktop on Windows cannot mount a git bash path such as /tmp/tmp.abc, and silently mounts nothing instead; the
# daemon needs the Windows form. Same helper as scripts/ops/restore.sh, which the rehearsal runs.
docker_path() {
  if command -v cygpath > /dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

# One command in the backup image, which has psql, rclone, age, sha256sum and a shell. No dependencies: every caller runs
# after the stack is up, or before it needs to be.
ops() {
  local entry=$1
  shift
  compose run --rm --no-deps --entrypoint "$entry" backup "$@"
}

teardown() {
  status=$?
  # By project name and without the environment file, which a failed run may already have deleted.
  docker compose --project-name "$project" down --volumes --timeout 5 > /dev/null 2>&1 || true
  docker image rm --force "$DEPLOY_IMAGE" "$DEPLOY_OPS_IMAGE" > /dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap teardown EXIT
trap 'exit 130' INT

# Before deploy/.env.rehearsal exists there is nothing to interpolate, so this one names the project only.
echo "== 1/9 write the environment file and clear a stack a killed run left behind"
docker compose --project-name "$project" down --volumes --timeout 5 > /dev/null 2>&1 || true
rm -f "$env_file"
# Everything this script writes holds a password or a private key, so it is all owner-only by default; the one exception is
# the binary below, whose mode is set after the build.
umask 077
s3_password=$(openssl rand -hex 16)
cat > "$env_file" <<EOF
# Written by scripts/ops/rehearse.sh for one run and deleted with the stack: never a deployment's file.
AXIOM_POSTGRES_PASSWORD=$(openssl rand -hex 24)
AXIOM_APP_PASSWORD=$(openssl rand -hex 24)
AXIOM_MIGRATOR_PASSWORD=$(openssl rand -hex 24)
AXIOM_BACKUP_BUCKET=$bucket
AXIOM_BACKUP_PREFIX=$prefix
AXIOM_BACKUP_S3_ENDPOINT=http://s3:9000
AXIOM_BACKUP_S3_ACCESS_KEY_ID=rehearsal
AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=$s3_password
AXIOM_BACKUP_S3_REGION=us-east-1
# Two sets make the retention check cheap: the third run has to drop the first.
AXIOM_BACKUP_KEEP_SETS=2
AXIOM_BACKUP_KEEP_DAYS=30
# The stack's own environment file is what the backup archives.
AXIOM_BACKUP_ENV_FILE=./.env.rehearsal
EOF

echo "== 2/9 build the images just up builds, plus the backup job's"
just web-embed
CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
# Under the umask above the binary is mode 0700, and the image runs it as another user: without this the container fails
# at start with `exec: "/axiom": permission denied`.
chmod 0755 deploy/bin/axiom
compose --profile ops build

echo "== 3/9 generate the two identities, and put their recipients in the environment file"
mkdir -p "$work/identities" "$work/before" "$work/after" "$work/set"
compose --profile ops run --rm --no-deps --user "$(id -u):$(id -g)" --volume "$(docker_path "$work/identities"):/out" \
  --entrypoint age-keygen backup -o /out/db.key
compose --profile ops run --rm --no-deps --user "$(id -u):$(id -g)" --volume "$(docker_path "$work/identities"):/out" \
  --entrypoint age-keygen backup -o /out/keys.key
db_recipient=$(sed -n 's/^# public key: //p' "$work/identities/db.key")
keys_recipient=$(sed -n 's/^# public key: //p' "$work/identities/keys.key")
[ -n "$db_recipient" ] && [ -n "$keys_recipient" ] || die "age-keygen printed no public key"
[ "$db_recipient" != "$keys_recipient" ] || die "the two identities are the same"
# The marker is a line only the set's copy of the file has once it is restored: without it, the comparison below would
# pass even if nothing had been written back.
cat >> "$env_file" <<EOF
AXIOM_BACKUP_DB_RECIPIENT=$db_recipient
AXIOM_BACKUP_SECRETS_RECIPIENT=$keys_recipient
# rehearsal marker: $(openssl rand -hex 8)
EOF

echo "== 4/9 start the stack and seed it"
compose up --detach --wait --wait-timeout 60 postgres
compose exec -T postgres sh < deploy/postgres/roles.sh
compose up --detach --build --wait --wait-timeout 180
for _ in $(seq 1 60); do
  ops rclone lsd axiom: > /dev/null 2>&1 && break
  sleep 1
done
ops rclone lsd axiom: > /dev/null || die "the S3 server never answered"
compose run --rm --no-deps axiom site init --name Rehearsal --currency USD \
  --console console.rehearsal.example --api api.rehearsal.example --admin admin.rehearsal.example
printf '%s\n' "$(openssl rand -hex 16)" | compose run --rm --no-deps -T axiom user create --email owner@rehearsal.example

# The three readers below are the evidence: the data, the roles with their memberships, and the key ring.
fingerprint() {
  compose exec -T postgres psql -X -q -A -t -U postgres -d axiom < scripts/ops/fingerprint.sql
}
roles() {
  compose exec -T postgres psql -X -q -A -t -U postgres -d axiom <<'SQL'
SELECT rolname || '|' || rolcanlogin || '|' || rolcreaterole || '|' || rolsuper || '|' || rolbypassrls || '|' || rolcreatedb
FROM pg_roles WHERE rolname LIKE 'axiom\_%' ORDER BY 1;
SELECT r.rolname || '->' || m.rolname || '|' || am.admin_option
FROM pg_auth_members am
JOIN pg_roles r ON r.oid = am.roleid JOIN pg_roles m ON m.oid = am.member
WHERE r.rolname LIKE 'axiom\_%' OR m.rolname LIKE 'axiom\_%' ORDER BY 1;
SQL
}
keys_hash() { ops sha256sum /keys/master /keys/hash /keys/identity; }

echo "== 5/9 record the state the set has to preserve"
compose stop axiom
fingerprint | sort > "$work/before/fingerprint.txt"
roles > "$work/before/roles.txt"
keys_hash > "$work/before/keys.txt"
cp "$env_file" "$work/before/deploy.env"
seeded=$(grep -c -E '\|[0-9a-f]{32}$' "$work/before/fingerprint.txt" || true)
[ "$seeded" -ge 3 ] || die "only $seeded tables have rows: the seed did not happen"

echo "== 6/9 back up, and check the guards around it"
compose run --rm backup
set_name=$(ops rclone lsf --dirs-only "axiom:$bucket/$prefix" | tr -d '/' | sort | tail -n 1)
[ -n "$set_name" ] || die "the bucket holds no set"

# One run at a time: with the lock held, a second one exits 75 and leaves the bucket alone.
compose run --detach --name "$project-lock" --entrypoint bash backup -c \
  'exec 9>/state/lock; flock -n 9 || exit 9; echo held; sleep 120' > /dev/null
for _ in $(seq 1 100); do
  docker logs "$project-lock" 2>&1 | grep -q held && break
  sleep 0.2
done
status=0
compose run --rm backup > /dev/null 2>&1 || status=$?
docker rm --force "$project-lock" > /dev/null
[ "$status" -eq 75 ] || die "a second backup with the lock held exited $status, not 75"

# The key ring never shares a recipient with the database: the database's identity must not open it.
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" --entrypoint rclone backup \
  copyto "axiom:$bucket/$prefix/$set_name/keys.tar.age" /work/keys.tar.age
status=0
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" \
  --volume "$(docker_path "$work/identities"):/identities:ro" --entrypoint bash \
  backup -c 'age --decrypt --identity /identities/db.key --output /dev/null /work/keys.tar.age' > /dev/null 2>&1 ||
  status=$?
[ "$status" -ne 0 ] || die "the key ring decrypted with the database's identity"
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" \
  --volume "$(docker_path "$work/identities"):/identities:ro" --entrypoint bash \
  backup -c 'age --decrypt --identity /identities/keys.key --output /dev/null /work/keys.tar.age'

echo "== 7/9 wipe the database and the key ring, then restore the set into the blank environment"
# Only the containers that use those volumes: the S3 server holds the set and has to stay up for the restore to fetch it.
compose rm --stop --force axiom postgres keys > /dev/null
docker volume rm "${project}_postgres" "${project}_keys" > /dev/null
sed -i '/^# rehearsal marker:/d' "$env_file"
started=$(date +%s)
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh --env-file "$env_file" "$set_name"
rto=$(($(date +%s) - started))

echo "== 8/9 compare what came back"
compose stop axiom
fingerprint | sort > "$work/after/fingerprint.txt"
roles > "$work/after/roles.txt"
keys_hash > "$work/after/keys.txt"
# A table has to come back, whatever happens to its rows: the comparison below skips some tables, this does not.
cut -d '|' -f 1 "$work/before/fingerprint.txt" > "$work/before/tables.txt"
cut -d '|' -f 1 "$work/after/fingerprint.txt" > "$work/after/tables.txt"
diff -u "$work/before/tables.txt" "$work/after/tables.txt" || die "the restored database has a different set of tables"
# Three kinds of rows move on their own while the program runs, so they are left out of the row comparison: River's
# queue tables (the library's, whose version the startup check verifies), billing.holders (one row per start of a core
# process, so the restored database has the set's row and the new process' too) and billing.expiry_cursor (where the
# expiry job's scan of accounts stopped, written every minute).
skip() { grep -v -e '^river\.' -e '^billing\.holders|' -e '^billing\.expiry_cursor|'; }
skip < "$work/before/fingerprint.txt" > "$work/before/state.txt"
skip < "$work/after/fingerprint.txt" > "$work/after/state.txt"
diff -u "$work/before/state.txt" "$work/after/state.txt" || die "the restored database differs from the backup"
diff -u "$work/before/roles.txt" "$work/after/roles.txt" || die "the restored roles differ from the backup"
diff -u "$work/before/keys.txt" "$work/after/keys.txt" || die "the restored key ring differs from the backup"
cmp "$work/before/deploy.env" "$env_file" || die "the restored environment file differs from the backup"
tables=$(grep -c . "$work/before/state.txt" || true)
echo "   $tables tables, the roles, the key ring and the environment file came back identical"

echo "== 9/9 retention: a third set has to drop the first"
compose run --rm backup
compose run --rm backup
sets=$(ops rclone lsf --dirs-only "axiom:$bucket/$prefix" | tr -d '/' | sort)
count=$(printf '%s\n' "$sets" | grep -c . || true)
[ "$count" -eq 2 ] || die "retention left $count sets, not 2"
if printf '%s\n' "$sets" | grep -q -x -e "$set_name"; then
  die "the oldest set survived retention"
fi

echo
echo "the rehearsal passed. Row for docs/ops/backup-restore.md「恢复演练记录」:"
printf '| %s | %s | %ss | 数据库、角色、密钥环、配置一致 | 通过 |\n' "$(date -u '+%Y-%m-%d %H:%M')" "$set_name" "$rto"
