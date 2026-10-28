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

die() {
  printf 'rehearsal: %s\n' "$*" >&2
  exit 1
}

# Nothing here may come from the caller's shell. Compose interpolates from the environment before it reads --env-file, so a
# deployment's exported AXIOM_BACKUP_BUCKET, its endpoint and its credentials would be this run's target: the first backup
# would write into the real bucket and the retention would delete real sets. The variables this script needs are written
# into the file below or passed on each command, and COMPOSE_FILE and friends would change which files are read at all.
for name in $(compgen -e); do
  case $name in
  AXIOM_* | RCLONE_* | DEPLOY_* | COMPOSE_FILE | COMPOSE_PROFILES | COMPOSE_ENV_FILES | COMPOSE_PROJECT_NAME) unset "$name" ;;
  esac
done
# The variables the script sets itself, after the sweep above.
export COMPOSE_PROJECT_NAME=$project
export DEPLOY_IMAGE=axiom-rehearsal:local
export DEPLOY_OPS_IMAGE=axiom-ops-rehearsal:local
export DEPLOY_RESTART_POLICY=no # nothing may come back after a killed run
export DEPLOY_PORT= DEPLOY_ADMIN_PORT= # ports the kernel picks, so a leftover stack cannot hold this one's
export AXIOM_EDGE_HTTP_PORT= AXIOM_EDGE_HTTPS_PORT= # the same for the edge: a real deployment keeps 80 and 443
# The edge's own network and the address in it the program trusts, off the default 172.28.0.0/24 so that a deployment
# running its edge on this machine does not hold this one's:
export AXIOM_EDGE_SUBNET=172.29.0.0/24
export AXIOM_EDGE_PROXY_ADDRESS=172.29.0.10

compose() {
  docker compose -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
    --env-file "$env_file" "$@"
}

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

# Everything that writes into the bucket goes through here: a backup, and the move that stands in for a set somebody moved
# to a new prefix. The check runs in the job's own container on the values compose resolved for it — not on what this
# script meant to write — and it runs before the command, because an upload and the pruning after it cannot be undone.
# Reads are not guarded: a wrong target there fails on the way in and changes nothing.
storage_guard='
for want in AXIOM_BACKUP_BUCKET=axiom-rehearsal AXIOM_BACKUP_PREFIX=axiom \
  RCLONE_CONFIG_AXIOM_ENDPOINT=http://s3:9000 RCLONE_CONFIG_AXIOM_ACCESS_KEY_ID=rehearsal; do
  name=${want%%=*} value=${want#*=} got=${!name-}
  if [ "$got" != "$value" ]; then
    printf "rehearsal: %s is %s, not %s: refusing to write\n" "$name" "${got:-<empty>}" "$value" >&2
    exit 1
  fi
done
exec "$@"'
# The guard is the -c script and the command follows it as bash's $0 and $1...: nothing between them that compose could
# read as an option of its own.
write() {
  compose run --rm --no-deps --entrypoint bash backup -c "$storage_guard" rehearsal "$@"
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
echo "== 1/10 write the environment file and clear a stack a killed run left behind"
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
# Two sets make the retention check cheap: the fourth run has to drop the second.
AXIOM_BACKUP_KEEP_SETS=2
AXIOM_BACKUP_KEEP_DAYS=30
# The stack's own environment file is what the backup archives.
AXIOM_BACKUP_ENV_FILE=./.env.rehearsal
# The edge, with the same file a deployment uses. The hostnames are not real: no request leaves this machine.
AXIOM_EDGE_CONSOLE_HOST=console.rehearsal.example
AXIOM_EDGE_API_HOST=api.rehearsal.example
AXIOM_EDGE_ADMIN_HOST=admin.rehearsal.example
AXIOM_EDGE_TLS_ISSUER=internal
EOF

echo "== 2/10 build the images just up builds, plus the backup job's"
# The Caddyfile names its issuer as a module, and Caddy substitutes the variable before parsing, so the shape of what it
# parses depends on the value — a bare `tls` is a parse error. The rehearsal runs with `internal`, so `acme` is the
# configuration it would otherwise never load, and it is the one every deployment with public certificates gets: compose has
# to fill it in when the variable is unset, because an empty value would override the Caddyfile's default.
grep -v '^AXIOM_EDGE_TLS_ISSUER=' "$env_file" > "$work/edge-unset.env"
edge_default=$(docker compose -f deploy/compose.yaml -f deploy/compose.edge.yaml --env-file "$work/edge-unset.env" config |
  sed -n 's/^ *AXIOM_EDGE_TLS_ISSUER: *//p')
[ "$edge_default" = acme ] || die "with AXIOM_EDGE_TLS_ISSUER unset compose passes the edge '$edge_default', want acme"
for issuer in internal "$edge_default"; do
  # Absolute: docker reads a relative path with a separator in it as a named volume, and says so.
  docker run --rm --network none \
    -e AXIOM_EDGE_TLS_ISSUER="$issuer" \
    -e AXIOM_EDGE_CONSOLE_HOST=console.rehearsal.example \
    -e AXIOM_EDGE_API_HOST=api.rehearsal.example \
    -e AXIOM_EDGE_ADMIN_HOST=admin.rehearsal.example \
    --volume "$(docker_path "$PWD/deploy/ingress/Caddyfile"):/etc/caddy/Caddyfile:ro" \
    caddy:2.11.7-alpine caddy validate --adapter caddyfile --config /etc/caddy/Caddyfile ||
    die "the Caddyfile does not parse with issuer $issuer"
done
just web-embed
CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
# Under the umask above the binary is mode 0700, and the image runs it as another user: without this the container fails
# at start with `exec: "/axiom": permission denied`.
chmod 0755 deploy/bin/axiom
compose --profile ops build

echo "== 3/10 generate the two identities, and put their recipients in the environment file"
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

echo "== 4/10 start the stack and seed it"
compose up --detach --wait --wait-timeout 60 postgres
compose exec -T postgres sh < deploy/postgres/roles.sh
compose up --detach --build --wait --wait-timeout 240
for _ in $(seq 1 60); do
  ops rclone lsd axiom: > /dev/null 2>&1 && break
  sleep 1
done
ops rclone lsd axiom: > /dev/null || die "the S3 server never answered"
compose run --rm --no-deps axiom site init --name Rehearsal --currency USD \
  --console console.rehearsal.example --api api.rehearsal.example --admin admin.rehearsal.example
printf '%s\n' "$(openssl rand -hex 16)" | compose run --rm --no-deps -T axiom user create --email owner@rehearsal.example

# The fingerprint reader also returns each table's row count, which is the evidence that the seed went in and came back;
# the roles and the key ring are the other two.
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

# Through the edge, on the name each surface answers. This is the check a deployment is reached by, so the rehearsal makes
# it the same way rather than on the program's loopback ports. The certificate is Caddy's own, hence --insecure, and
# --connect-to keeps the request on the standard port while the connection goes to the one the host published: a URL with
# that port in it would put the port into the Host header, and the program looks a domain up by it. A fresh edge has no
# certificate for a name until it has got one, which is a failed handshake rather than a status, so only a request with no
# answer at all is retried.
edge() {
  local name=$1 path=$2 out tries=0
  while :; do
    out=$(curl --silent --show-error --insecure --max-time 30 --connect-to "$name:443:127.0.0.1:$https" \
      --write-out '\n%{http_code}' "https://$name$path") && break
    tries=$((tries + 1))
    [ "$tries" -lt 15 ] || die "no answer from the edge for $name$path: is it up, and has it a certificate for $name?"
    sleep 2
  done
  edge_status=${out##*$'\n'}
  edge_body=${out%$'\n'*}
}

echo "== 5/10 record the state the set has to preserve"
compose stop axiom
fingerprint | sort > "$work/before/fingerprint.txt"
roles > "$work/before/roles.txt"
keys_hash > "$work/before/keys.txt"
cp "$env_file" "$work/before/deploy.env"
# A comparison of two empty databases passes, so the seed has to be in the set by name: the site that was registered and
# the user that was created, with the rows they bring.
for table in accounts.sites accounts.users; do
  grep -q -E "^$table\|[1-9]" "$work/before/fingerprint.txt" || die "the seed left no row in $table"
done
seeded=$(awk -F'|' '$2 > 0' "$work/before/fingerprint.txt" | wc -l | tr -d ' ')
[ "$seeded" -ge 3 ] || die "only $seeded tables have rows: the seed did not happen"

echo "== 6/10 back up, and check the guards around it"
write /ops/backup.sh
set_name=$(ops rclone lsf --dirs-only "axiom:$bucket/$prefix" | tr -d '/' | sort | tail -n 1)
[ -n "$set_name" ] || die "the bucket holds no set"

# A limit of zero is refused before the lock is taken and before anything is written, rather than uploaded and then
# pruned to nothing; either of these runs would empty the bucket if the check were missing.
for limit in AXIOM_BACKUP_KEEP_SETS=0 AXIOM_BACKUP_KEEP_DAYS=0; do
  status=0
  compose run --rm --no-deps --env "$limit" backup > /dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || die "$limit did not stop the run with exit 1 but with $status"
done
after=$(ops rclone lsf --dirs-only "axiom:$bucket/$prefix" | tr -d '/' | sort)
[ "$after" = "$set_name" ] || die "a run with a retention limit of zero changed the bucket"

# One run at a time: with the lock held, a second one exits 75 and leaves the bucket alone.
compose run --detach --name "$project-lock" --entrypoint bash backup -c \
  'exec 9>/state/lock; flock -n 9 || exit 9; echo held; sleep 120' > /dev/null
for _ in $(seq 1 100); do
  docker logs "$project-lock" 2>&1 | grep -q held && break
  sleep 0.2
done
status=0
write /ops/backup.sh > /dev/null 2>&1 || status=$?
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

echo "== 7/10 wipe the database and the key ring, then restore the set into the blank environment"
# Only the containers that use those volumes: the S3 server holds the set and has to stay up for the restore to fetch it.
compose rm --stop --force axiom postgres keys > /dev/null
docker volume rm "${project}_postgres" "${project}_keys" > /dev/null
sed -i '/^# rehearsal marker:/d' "$env_file"
started=$(date +%s)
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
  --env-file "$env_file" "$set_name"
rto=$(($(date +%s) - started))

echo "== 8/10 compare what came back"
# First the edge, while the program the restore started is still running: it is where a deployment is reached, and the
# program's own loopback ports answer whether or not it can. Each surface under its own name, then the other one's path,
# which a domain of two surfaces cannot refuse: the console's site answers on a console domain and 404s on any other, and
# the model API answers a request with no key with 401.
https=$(compose port caddy 443)
https=${https##*:}
[ -n "$https" ] || die "the edge published no https port"
edge console.rehearsal.example /api/console/v1/site
[ "$edge_status" = 200 ] || die "the console answered $edge_status through the edge, not 200"
edge api.rehearsal.example /api/console/v1/site
[ "$edge_status" = 404 ] || die "the API host answered a console path with $edge_status, not 404"
edge api.rehearsal.example /v1/models
[ "$edge_status" = 401 ] || die "the model API answered $edge_status through the edge, not the 401 a keyless one gets"
echo "   both surfaces answer through the edge"

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
rows=$(awk -F'|' '{sum += $2} END {print sum}' "$work/before/state.txt")
echo "   $tables tables and $rows rows, the roles, the key ring and the environment file came back identical"

echo "== 9/10 retention: three more sets, so the first has to go"
write /ops/backup.sh
write /ops/backup.sh
sets=$(ops rclone lsf --dirs-only "axiom:$bucket/$prefix" | tr -d '/' | sort)
count=$(printf '%s\n' "$sets" | grep -c . || true)
[ "$count" -eq 2 ] || die "retention left $count sets, not 2"
if printf '%s\n' "$sets" | grep -q -x -e "$set_name"; then
  die "the oldest set survived retention"
fi

echo "== 10/10 the sets moved and the credentials rotated since the set was written"
# A restore that installed the set's own copy of the environment file and then re-read it would go back to the location and
# the credentials of the day of the backup here, after it had already replaced the database.
newest=$(printf '%s\n' "$sets" | tail -n 1)
rotated=$(openssl rand -hex 16)
sed -i "s/^AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=.*/AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=$rotated/" "$env_file"
# The stand-in reads its own credentials from the same file, so it has to be recreated to hold the new pair; its volume,
# and with it the sets, stays.
compose up --detach --force-recreate --wait --wait-timeout 60 s3
for _ in $(seq 1 60); do
  ops rclone lsd axiom: > /dev/null 2>&1 && break
  sleep 1
done
ops rclone lsd axiom: > /dev/null || die "the S3 server did not take the rotated credentials"
write rclone move "axiom:$bucket/$prefix" "axiom:$bucket/axiom-moved"
sed -i "s/^AXIOM_BACKUP_PREFIX=.*/AXIOM_BACKUP_PREFIX=axiom-moved/" "$env_file"
compose rm --stop --force axiom postgres keys > /dev/null
docker volume rm "${project}_postgres" "${project}_keys" > /dev/null
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
  --env-file "$env_file" "$newest"
# The installed file is the set's own, so it names the old location and the revoked credentials: that is the deployment's
# configuration as of the backup, which is what a restore installs and what the next step of the runbook replaces.
cmp -s "$work/before/deploy.env" "$env_file" || die "the second restore did not install the set's environment file"
echo "   restored $newest from the new prefix with the rotated credentials"

echo
echo "the rehearsal passed. Row for docs/ops/backup-restore.md「恢复演练记录」:"
printf '| %s | %s | %ss | 数据库、角色、密钥环、配置一致，入口两个域名应答正确 | 通过 |\n' \
  "$(date -u '+%Y-%m-%d %H:%M')" "$set_name" "$rto"
