#!/usr/bin/env bash
# The backup rehearsal: a stack of its own, a local S3 server in place of the bucket, the database and the key ring wiped
# and restored from a set, and what came back compared with what was there (docs/ops/backup-restore.md「恢复演练」). The
# set is taken after the call log's writer fell behind and with the audit replica running ahead of it afterwards, and the
# second restore replaces a database whose call log holds calls served after its set.
#
#   just backup-rehearsal
#
# Nothing here reaches a real bucket or a deployment, and everything it creates is removed again, volumes included. It
# owns the project axiom-rehearsal, so it runs one at a time on a machine, and it needs what `just up` needs (go, node,
# pnpm, docker compose) plus about twenty minutes, most of it waiting for the audit chain's minutely jobs.
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
  AXIOM_* | DEPLOY_* | COMPOSE_FILE | COMPOSE_PROFILES | COMPOSE_ENV_FILES | COMPOSE_PROJECT_NAME) unset "$name" ;;
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

# One command in the backup image, which has psql, age, the program's `axiom` binary and a shell. No dependencies: every
# caller runs after the stack is up, or before it needs to be.
ops() {
  local entry=$1
  shift
  compose run --rm --no-deps --entrypoint "$entry" backup "$@"
}

# The sets under the prefix, one name per line. The object store lists keys, not directories, so the first path segment
# under the prefix is the set.
bucket_sets() {
  ops axiom objectstore list --prefix "$prefix/" --names |
    sed "s|^$prefix/||" | cut -d/ -f1 | sed '/^$/d' | sort -u
}

# Everything that writes into a bucket goes through scripts/ops/rehearsal-guard.sh, which deploy/compose.rehearsal.yaml
# mounts and makes the backup job's entrypoint: a backup, the rehearsal's own writes here, and the move that stands in for
# a set somebody moved to a new prefix. Reads are not guarded: a wrong target there fails on the way in and changes
# nothing. The guard is bash's script and the command follows it as its arguments: nothing between them that compose
# could read as an option of its own.
write() {
  compose run --rm --no-deps --entrypoint bash backup /rehearsal/guard.sh "$@"
}

# The audit replica's bucket NAME through the ops image's S3 client, with the rehearsal's endpoint and key; a create is
# guarded as every write is.
audit_bucket() {
  local bucket=$1 verb=$2
  shift 2
  if [ "$verb" = create ]; then
    compose run --rm --no-deps --env AXIOM_OBJECTSTORE_BUCKET="$bucket" --entrypoint bash backup /rehearsal/guard.sh \
      axiom objectstore create "$@"
  else
    compose run --rm --no-deps --env AXIOM_OBJECTSTORE_BUCKET="$bucket" --entrypoint axiom backup objectstore "$verb" "$@"
  fi
}
# replica_keys BUCKET: the audit chain's objects in BUCKET, in the chain's order; replica_last BUCKET: the last link they
# hold, from the last key's name (audit-chain/v1/<first>-<last>.jsonl), 0 for none.
replica_keys() { audit_bucket "$1" list --prefix audit-chain/v1/ --names | sort; }
replica_last() {
  local last
  last=$(replica_keys "$1" | tail -n 1 | sed -n 's|^audit-chain/v1/[0-9]*-0*\([0-9][0-9]*\)\.jsonl$|\1|p')
  printf '%s\n' "${last:-0}"
}
psql_value() { compose exec -T postgres psql -X -q -A -t -v ON_ERROR_STOP=1 -U postgres -d axiom -c "$1"; }
chain_head() { psql_value 'SELECT coalesce(max(seq), 0) FROM operations.audit_chain'; }
# wait_replica BUCKET: until every audit entry is chained and BUCKET holds the whole chain. Entries are chained once a
# minute when they are a minute old, and copied a minute later, so this takes up to about three minutes.
wait_replica() {
  local bucket=$1 entries head
  for _ in $(seq 1 60); do
    entries=$(psql_value 'SELECT count(*) FROM platform.audit_log')
    head=$(chain_head)
    if [ "$head" -gt 0 ] && [ "$head" = "$entries" ] && [ "$(replica_last "$bucket")" = "$head" ]; then
      return 0
    fi
    sleep 5
  done
  die "$bucket never caught up with the audit chain: $entries entries, $head chained, $(replica_last "$bucket") copied"
}

# The program's call log volume, filled with a spill from internal/observability/testdata/spill: the files a node leaves
# that accepted those calls and stopped before its database took them (TestRehearsalSpillsReplay keeps them replayable).
# They replace whatever the volume held, as the node's own directory would be, and belong to the program's user.
load_spill() {
  compose stop axiom > /dev/null
  compose run --rm --no-deps -T --entrypoint sh \
    --volume "$(docker_path "$PWD/internal/observability/testdata/spill/$1"):/from:ro" \
    --volume "${project}_call_log:/to" restore -c \
    'find /to -mindepth 1 -delete && cp -R /from/. /to/ && chown -R 65532:65532 /to &&
     find /to -type d -exec chmod 700 {} + && find /to -type f -exec chmod 600 {} +'
}
# spill_check VOLUME: the program's own check of a call log volume, its message on stdout whether it passes or not (it
# fails on a volume with records, which is what some callers expect).
spill_check() {
  compose run --rm --no-deps -T --entrypoint axiom --volume "$1:/check:ro" backup call-log check /check 2>&1 || true
}
call_rows() { psql_value 'SELECT count(*) FROM observability.call_log'; }
# grown: the tables that the restored program may add rows to on its own while the set's rows must all come back:
# platform.discarded_jobs and the alert event it raises, when River's rescuer discards a job the set caught running whose
# kind the restored node has no worker for (the audit replica's, once the restore turned the replica off). rows TABLE is
# one hash per row.
grown="observability.alert_events platform.discarded_jobs"
rows() { psql_value "SELECT md5(to_jsonb(t)::text) FROM $1 t ORDER BY 1"; }
# The calls a spill holds, by request ID, as the database would store them: settled records are in the segments, parked
# ones in pending/ by name.
spill_ids() {
  local dir=internal/observability/testdata/spill/$1
  { grep -aoh '"id":"[0-9a-f-]\{36\}"' "$dir"/call-*.wal | cut -d'"' -f4
    find "$dir/pending" -name '*.rec' -exec basename {} .rec \; 2>/dev/null; } | sort -u
}
backup() {
  bash scripts/ops/backup.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
    --env-file "$env_file"
}

teardown() {
  status=$?
  # By project name and without the environment file, which a failed run may already have deleted.
  docker compose --project-name "$project" down --volumes --timeout 5 > /dev/null 2>&1 || true
  # The call logs a restore set aside are volumes of their own, outside the project's.
  docker volume ls --quiet --filter "name=^${project}_call_log_before_" | xargs -r docker volume rm > /dev/null 2>&1 || true
  rm -f "$env_file.backup-lock" # scripts/ops/backup.sh's, next to the environment file
  docker image rm --force "$DEPLOY_IMAGE" "$DEPLOY_OPS_IMAGE" > /dev/null 2>&1 || true
  rm -rf "$work"
  exit "$status"
}
trap teardown EXIT
trap 'exit 130' INT

# Before deploy/.env.rehearsal exists there is nothing to interpolate, so this one names the project only.
echo "== 1/12 write the environment file and clear a stack a killed run left behind"
docker compose --project-name "$project" down --volumes --timeout 5 > /dev/null 2>&1 || true
docker volume ls --quiet --filter "name=^${project}_call_log_before_" | xargs -r docker volume rm > /dev/null 2>&1 || true
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
AXIOM_BACKUP_CIPHER_PASS=$(openssl rand -hex 24)
AXIOM_BACKUP_PREFIX=$prefix
AXIOM_BACKUP_S3_ENDPOINT=http://s3:9000
AXIOM_BACKUP_S3_ACCESS_KEY_ID=rehearsal
AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=$s3_password
AXIOM_BACKUP_S3_REGION=us-east-1
# Two sets make the retention check cheap: the fourth run has to drop the second.
AXIOM_BACKUP_KEEP_SETS=2
AXIOM_BACKUP_KEEP_DAYS=30
# The audit chain's replica, in a bucket of its own on the same server: the set is taken while it holds the chain, it
# runs ahead of the set afterwards, and the restored program must leave it alone.
AXIOM_AUDIT_REPLICA_ENDPOINT=http://s3:9000
AXIOM_AUDIT_REPLICA_BUCKET=axiom-rehearsal-audit
AXIOM_AUDIT_REPLICA_REGION=us-east-1
AXIOM_AUDIT_REPLICA_ACCESS_KEY_ID=rehearsal
AXIOM_AUDIT_REPLICA_SECRET_ACCESS_KEY=$s3_password
AXIOM_AUDIT_REPLICA_PATH_STYLE=true
# The stack's own environment file is what the backup archives.
AXIOM_BACKUP_ENV_FILE=./.env.rehearsal
# The edge, with the same file a deployment uses. The hostnames are not real: no request leaves this machine.
AXIOM_EDGE_CONSOLE_HOST=console.rehearsal.example
AXIOM_EDGE_API_HOST=api.rehearsal.example
AXIOM_EDGE_ADMIN_HOST=admin.rehearsal.example
AXIOM_EDGE_TLS_ISSUER=internal
EOF
# The initial cluster has no pgBackRest stanza yet, so it must not archive: an archive-push with no repository can stall
# the temporary server's shutdown. Keep the switch in the shell for this stack and leave the set's file without the line,
# so the restore below still exercises adding it.
export AXIOM_BACKUP_WAL_BUCKET=

echo "== 2/12 build the images just up builds, plus the backup job's"
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
# The ops image copies /axiom out of the program image, so that image has to exist before the ops build resolves its FROM.
compose build axiom
compose --profile ops build
# The database image is this tree's (pgBackRest on the pinned PostgreSQL tag); nothing else builds it before the first up.
compose build postgres
# The transfer limit is shared by the backup and restore jobs; a non-default value has to reach both, not only the
# service that happened to gain the override.
config=$(AXIOM_BACKUP_TRANSFER_TIMEOUT=24h compose --profile ops config)
for service in backup restore; do
  got=$(printf '%s\n' "$config" | awk -v service="$service" '
    $0 == "  " service ":" { inside = 1; next }
    inside && /^  [a-z]/ { inside = 0 }
    inside && /AXIOM_BACKUP_TRANSFER_TIMEOUT:/ { print $2; exit }')
  [ "$got" = 24h ] || die "$service did not get AXIOM_BACKUP_TRANSFER_TIMEOUT=24h (got ${got:-nothing})"
done

echo "== 3/12 generate the two identities, and put their recipients in the environment file"
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

echo "== 4/12 start the stack and seed it"
compose up --detach --wait --wait-timeout 60 postgres
compose exec -T postgres sh < deploy/postgres/roles.sh
compose up --detach --build --wait --wait-timeout 240
for _ in $(seq 1 60); do
  write axiom objectstore create > /dev/null 2>&1 && break
  sleep 1
done
ops axiom objectstore check > /dev/null || die "the S3 server never answered"
audit_bucket axiom-rehearsal-audit create > /dev/null
compose run --rm --no-deps axiom site init --name Rehearsal --currency USD \
  --console console.rehearsal.example --api api.rehearsal.example --admin admin.rehearsal.example
printf '%s\n' "$(openssl rand -hex 16)" | compose run --rm --no-deps -T axiom user create --email owner@rehearsal.example
# A non-default policy, so the comparison has business content to preserve rather than an empty table that the program
# fills with its default on the first start after the restore. The startup's pricing job may have inserted the default
# already, so replace its values instead of racing it with a second row.
compose exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U postgres -d axiom <<'SQL'
INSERT INTO billing.price_policies(site_id, floor_multiplier, change_ratio, max_increase_ratio)
SELECT id, '1.25', '0.10', '0.50' FROM accounts.sites
ON CONFLICT (site_id) DO UPDATE SET
  floor_multiplier = EXCLUDED.floor_multiplier,
  change_ratio = EXCLUDED.change_ratio,
  max_increase_ratio = EXCLUDED.max_increase_ratio;
SQL

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

echo "== 5/12 the call log's writer falls behind: the backup refuses until it drains"
# The audit replica holds the seed's chain before anything else happens, so the set below is taken with it caught up.
wait_replica axiom-rehearsal-audit
# A node that stopped while its database refused the call log's writes: settled calls, and a pending one, whose records
# are on this machine only. A backup now would hold whatever the ledger has without those records.
load_spill behind
behind_records=$(spill_ids behind | wc -l | tr -d ' ')
[ "$behind_records" -eq 4 ] || die "the behind spill names $behind_records calls, not 4"
rows=$(call_rows)
status=0
out=$(backup 2>&1) || status=$?
[ "$status" -eq 1 ] || die "a backup over an undrained call log exited $status, not 1: $out"
printf '%s\n' "$out" | grep -q "holds 4 call records" || die "the refused backup did not say what it found: $out"
[ -z "$(bucket_sets)" ] || die "a refused backup left something in the bucket"
# Started, the program writes the records it finds; stopped, it drains: then the check passes.
compose up --detach --wait --wait-timeout 180 axiom
for _ in $(seq 1 60); do
  [ "$(call_rows)" -eq $((rows + 4)) ] && break
  sleep 1
done
[ "$(call_rows)" -eq $((rows + 4)) ] || die "the program did not write the four records it found"
compose stop axiom
spill_check "${project}_call_log" | grep -q "holds no call record" || die "a stopped program left its call log undrained"

echo "== 6/12 record the state the set has to preserve, back up, and check the guards around it"
fingerprint | sort > "$work/before/fingerprint.txt"
for table in $grown; do rows "$table" > "$work/before/$table.rows"; done
roles > "$work/before/roles.txt"
keys_hash > "$work/before/keys.txt"
cp "$env_file" "$work/before/deploy.env"
# A comparison of two empty databases passes, so the seed has to be in the set by name: the site that was registered and
# the user that was created, with the rows they bring; and the calls of the call log, in whichever partitions hold them.
for table in accounts.sites accounts.users; do
  grep -q -E "^$table\|[1-9]" "$work/before/fingerprint.txt" || die "the seed left no row in $table"
done
seeded=$(awk -F'|' '$2 > 0' "$work/before/fingerprint.txt" | wc -l | tr -d ' ')
[ "$seeded" -ge 3 ] || die "only $seeded tables have rows: the seed did not happen"
calls_before=$(awk -F'|' '$1 ~ /^observability\.call_log_/ {sum += $2} END {print sum + 0}' "$work/before/fingerprint.txt")
[ "$calls_before" -eq 4 ] || die "the database holds $calls_before calls, not the four of the spill"
# The program is stopped, so scripts/ops/backup.sh leaves it so.
backup
set_name=$(bucket_sets | tail -n 1)
[ -n "$set_name" ] || die "the bucket holds no set"
[ -z "$(compose ps --status running --quiet axiom)" ] || die "the backup started a program that was not running"
replica_at_set=$(replica_last axiom-rehearsal-audit)

# The job refuses a running program before it dumps anything, whoever starts it.
compose up --detach --wait --wait-timeout 180 axiom
status=0
out=$(write /ops/backup.sh 2>&1) || status=$?
[ "$status" -eq 1 ] && printf '%s\n' "$out" | grep -q "holds the call log" ||
  die "a backup with the program running exited $status: $out"
compose stop axiom
[ "$(bucket_sets)" = "$set_name" ] || die "the refused backup changed the bucket"

# A limit of zero is refused before the lock is taken and before anything is written, rather than uploaded and then
# pruned to nothing; either of these runs would empty the bucket if the check were missing.
for limit in AXIOM_BACKUP_KEEP_SETS=0 AXIOM_BACKUP_KEEP_DAYS=0; do
  status=0
  compose run --rm --no-deps --env "$limit" backup > /dev/null 2>&1 || status=$?
  [ "$status" -eq 1 ] || die "$limit did not stop the run with exit 1 but with $status"
done
after=$(bucket_sets)
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
rm -f "$work/set/keys.tar.age"
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" --entrypoint axiom backup \
  objectstore get --key "$prefix/$set_name/keys.tar.age" --file /work/keys.tar.age > /dev/null
status=0
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" \
  --volume "$(docker_path "$work/identities"):/identities:ro" --entrypoint bash \
  backup -c 'age --decrypt --identity /identities/db.key --output /dev/null /work/keys.tar.age' > /dev/null 2>&1 ||
  status=$?
[ "$status" -ne 0 ] || die "the key ring decrypted with the database's identity"
compose run --rm --no-deps --volume "$(docker_path "$work/set"):/work" \
  --volume "$(docker_path "$work/identities"):/identities:ro" --entrypoint bash \
  backup -c 'age --decrypt --identity /identities/keys.key --output /dev/null /work/keys.tar.age'

echo "== 7/12 the audit replica runs ahead of the set"
# The lost database goes on after its set: another user, so the chain has a link the set does not, which the replica
# copies. The restored database will not have it.
compose up --detach --wait --wait-timeout 180 axiom
printf '%s\n' "$(openssl rand -hex 16)" | compose run --rm --no-deps -T axiom user create --email later@rehearsal.example
wait_replica axiom-rehearsal-audit
[ "$(replica_last axiom-rehearsal-audit)" -gt "$replica_at_set" ] || die "the replica did not get past the set"
replica_keys axiom-rehearsal-audit > "$work/before/replica.txt"
compose stop axiom

echo "== 8/12 the machine is lost: restore the set into a blank environment"
# Its database, key ring and call log go with it. Only the containers that use those volumes are removed: the S3 server
# holds the set and has to stay up for the restore to fetch it.
compose rm --stop --force axiom postgres keys > /dev/null
docker volume rm "${project}_postgres" "${project}_keys" "${project}_call_log" > /dev/null
sed -i '/^# rehearsal marker:/d' "$env_file"
started=$(date +%s)
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
  --env-file "$env_file" "$set_name"
rto=$(($(date +%s) - started))

echo "== 9/12 compare what came back"
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
# The restored program copies the chain nowhere: the bucket the set names holds the history past the set.
compose logs --no-color axiom | grep "the audit chain is not copied off-site" > /dev/null ||
  die "the restored program copies the audit chain somewhere"
grep -q "^# .*: restored set $set_name; the audit replica http://s3:9000/axiom-rehearsal-audit holds" "$env_file" ||
  die "the installed environment file does not record which replica held the earlier history"
for name in ENDPOINT BUCKET REGION ACCESS_KEY_ID SECRET_ACCESS_KEY PATH_STYLE; do
  grep -qx "AXIOM_AUDIT_REPLICA_$name=" "$env_file" || die "the restore left AXIOM_AUDIT_REPLICA_$name set"
done

compose stop axiom
fingerprint | sort > "$work/after/fingerprint.txt"
for table in $grown; do
  missing=$(rows "$table" | comm -23 "$work/before/$table.rows" -)
  [ -z "$missing" ] || die "a row of $table in the set did not come back"
done
roles > "$work/after/roles.txt"
keys_hash > "$work/after/keys.txt"
# A table has to come back, whatever happens to its rows: the comparison below skips some tables, this does not.
cut -d '|' -f 1 "$work/before/fingerprint.txt" > "$work/before/tables.txt"
cut -d '|' -f 1 "$work/after/fingerprint.txt" > "$work/after/tables.txt"
diff -u "$work/before/tables.txt" "$work/after/tables.txt" || die "the restored database has a different set of tables"
# Rows that move on their own while the program runs are left out of the row comparison: River's queue tables (the
# library's, whose version the startup check verifies), billing.holders (one row per start of a core process, so the
# restored database has the set's row and the new process' too), billing.expiry_cursor (where the expiry job's scan of
# accounts stopped, written every minute), platform.alert_signals (the startup's own signals are added after the set),
# platform.credential_share_departures (the new process garbage-collects the set's stale node row), catalog.sync_state
# (the catalog job's progress) and the two rollout flags platform.billing_preference_readiness and
# platform.model_prices_public_readiness (every node's heartbeat rewrites them with its own time). billing.price_policies stays in the comparison: fingerprint.sql leaves out only its
# confirmed_at timestamp, so its rev and multipliers are checked.
# installed: an environment file as the restore installs it, without the lines a restore changes on purpose: the WAL
# bucket it turns off, the audit replica it turns off and the line recording which replica that was.
installed() { grep -v -e '^AXIOM_BACKUP_WAL_BUCKET=' -e '^AXIOM_AUDIT_REPLICA_' -e '^# .*: restored set '; }
skip() { grep -v -e '^river\.' -e '^billing\.holders|' -e '^billing\.expiry_cursor|' -e '^platform\.alert_signals|' -e '^platform\.credential_share_departures|' -e '^catalog\.sync_state|' \
  -e '^platform\.billing_preference_readiness|' -e '^platform\.model_prices_public_readiness|' \
  -e '^observability\.alert_events|' -e '^platform\.discarded_jobs|'; }
skip < "$work/before/fingerprint.txt" > "$work/before/state.txt"
skip < "$work/after/fingerprint.txt" > "$work/after/state.txt"
diff -u "$work/before/state.txt" "$work/after/state.txt" || die "the restored database differs from the backup"
diff -u "$work/before/roles.txt" "$work/after/roles.txt" || die "the restored roles differ from the backup"
diff -u "$work/before/keys.txt" "$work/after/keys.txt" || die "the restored key ring differs from the backup"
# The restore adds AXIOM_BACKUP_WAL_BUCKET= so the new system identifier stays out of the old stanza, and turns the audit
# replica off with a line naming it (checked above); everything else in the installed file must match the set's copy,
# and the WAL switch must be there from this first restore onwards.
cmp -s <(installed < "$work/before/deploy.env") <(installed < "$env_file") ||
  die "the restored environment file differs from the backup"
grep -qx 'AXIOM_BACKUP_WAL_BUCKET=' "$env_file" || die "the restore did not disable WAL archiving in the installed file"
replica_keys axiom-rehearsal-audit | diff -u "$work/before/replica.txt" - || die "the restore changed the old replica"
tables=$(grep -c . "$work/before/state.txt" || true)
rows=$(awk -F'|' '{sum += $2} END {print sum}' "$work/before/state.txt")
echo "   $tables tables and $rows rows, the four calls of the call log among them, the roles, the key ring and the"
echo "   environment file came back identical; the replica that ran ahead is untouched"

echo "== 10/12 retention: three more sets, so the first has to go, taken with the program running"
# scripts/ops/backup.sh stops the program for each and starts it again.
compose up --detach --wait --wait-timeout 180 axiom
backup
backup
[ -n "$(compose ps --status running --quiet axiom)" ] || die "the backup did not start the program again"
sets=$(bucket_sets)
count=$(printf '%s\n' "$sets" | grep -c . || true)
[ "$count" -eq 2 ] || die "retention left $count sets, not 2"
if printf '%s\n' "$sets" | grep -q -x -e "$set_name"; then
  die "the oldest set survived retention"
fi

echo "== 11/12 the sets moved, the credentials rotated and the call log holds calls served after the set"
# A restore that installed the set's own copy of the environment file and then re-read it would go back to the location and
# the credentials of the day of the backup here, after it had already replaced the database.
newest=$(printf '%s\n' "$sets" | tail -n 1)
rotated=$(openssl rand -hex 16)
sed -i "s/^AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=.*/AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=$rotated/" "$env_file"
# The stand-in reads its own credentials from the same file, so it has to be recreated to hold the new pair; its volume,
# and with it the sets, stays.
compose up --detach --force-recreate --wait --wait-timeout 60 s3
for _ in $(seq 1 60); do
  ops axiom objectstore check > /dev/null 2>&1 && break
  sleep 1
done
ops axiom objectstore check > /dev/null || die "the S3 server did not take the rotated credentials"
write bash -c '
set -euo pipefail
from=$1 to=$2
axiom objectstore list --prefix "$from/" --names | while IFS= read -r key; do
  [ -n "$key" ] || continue
  rel=${key#"$from/"}
  file=$(mktemp)
  axiom objectstore get --key "$key" --file "$file" > /dev/null
  axiom objectstore put --key "$to/$rel" --file "$file" > /dev/null
  axiom objectstore delete --key "$key" > /dev/null
  rm -f "$file"
done
' rehearsal "$prefix" axiom-moved
sed -i "s/^AXIOM_BACKUP_PREFIX=.*/AXIOM_BACKUP_PREFIX=axiom-moved/" "$env_file"
# This time the restore replaces the database on the same machine, whose call log holds two calls the set has never
# seen: replayed into the restored database, they would be calls it never charged.
load_spill after
calls_set=$(call_rows)
status=0
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
  --env-file "$env_file" "$newest" > /dev/null 2>&1 || status=$?
[ "$status" -eq 1 ] || die "a restore over an existing database and call log without --overwrite exited $status, not 1"
AXIOM_RESTORE_DB_IDENTITY="$work/identities/db.key" AXIOM_RESTORE_SECRETS_IDENTITY="$work/identities/keys.key" \
  bash scripts/ops/restore.sh -f deploy/compose.yaml -f deploy/compose.edge.yaml -f deploy/compose.rehearsal.yaml \
  --env-file "$env_file" --overwrite "$newest"
aside=$(docker volume ls --quiet --filter "name=^${project}_call_log_before_" | tail -n 1)
[ -n "$aside" ] || die "the restore did not set the call log aside"
spill_check "$aside" | grep -q "holds 2 call records" || die "the call log set aside does not hold the two later calls"
spill_check "${project}_call_log" | grep -q "holds no call record" || die "the restored program got a call log with records"
[ "$(call_rows)" = "$calls_set" ] || die "the restored database has $(call_rows) calls, not the set's $calls_set"
for id in $(spill_ids after); do
  [ "$(psql_value "SELECT count(*) FROM observability.call_log WHERE id = '$id'")" = 0 ] ||
    die "the call $id served after the set reached the restored database"
done
# The installed file is the set's own, so it names the old location and the revoked credentials: that is the deployment's
# configuration as of the backup, which is what a restore installs and what the next step of the runbook replaces. The
# restore also adds AXIOM_BACKUP_WAL_BUCKET= (the restored cluster must not push into the old stanza); compare the rest and
# require that line, so the permanent switch off is what the rehearsal exercises rather than a compose override.
cmp -s <(installed < "$work/before/deploy.env") <(installed < "$env_file") ||
  die "the second restore did not install the set's environment file"
grep -qx 'AXIOM_BACKUP_WAL_BUCKET=' "$env_file" || die "the restore did not disable WAL archiving in the installed file"
echo "   restored $newest from the new prefix with the rotated credentials; the two later calls stayed in $aside"

echo "== 12/12 the restored chain goes to a new replica bucket, from its first link"
# The installed file has the credentials of the day of the set, revoked since; putting the current ones back is the
# runbook's step after a restore, and the new bucket is made with them.
sed -i "s/^AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=.*/AXIOM_BACKUP_S3_SECRET_ACCESS_KEY=$rotated/" "$env_file"
audit_bucket axiom-rehearsal-audit-2 create > /dev/null
sed -i -e "s|^AXIOM_AUDIT_REPLICA_ENDPOINT=.*|AXIOM_AUDIT_REPLICA_ENDPOINT=http://s3:9000|" \
  -e "s|^AXIOM_AUDIT_REPLICA_BUCKET=.*|AXIOM_AUDIT_REPLICA_BUCKET=axiom-rehearsal-audit-2|" \
  -e "s|^AXIOM_AUDIT_REPLICA_REGION=.*|AXIOM_AUDIT_REPLICA_REGION=us-east-1|" \
  -e "s|^AXIOM_AUDIT_REPLICA_ACCESS_KEY_ID=.*|AXIOM_AUDIT_REPLICA_ACCESS_KEY_ID=rehearsal|" \
  -e "s|^AXIOM_AUDIT_REPLICA_SECRET_ACCESS_KEY=.*|AXIOM_AUDIT_REPLICA_SECRET_ACCESS_KEY=$rotated|" \
  -e "s|^AXIOM_AUDIT_REPLICA_PATH_STYLE=.*|AXIOM_AUDIT_REPLICA_PATH_STYLE=true|" "$env_file"
# The restore exported the six empty so that this run's compose would agree with the file; this shell never had them.
compose up --detach --wait --wait-timeout 180 axiom
wait_replica axiom-rehearsal-audit-2
first=$(replica_keys axiom-rehearsal-audit-2 | head -n 1)
[ "$first" = "audit-chain/v1/$(printf '%020d' 1)-${first##*-}" ] || die "the new replica does not start at the first link: $first"
replica_keys axiom-rehearsal-audit | diff -u "$work/before/replica.txt" - || die "the restored chain reached the old replica"
echo "   the new bucket holds the restored chain from link 1 to $(chain_head); the old one holds what it held"

echo
echo "the rehearsal passed. Row for docs/ops/backup-restore.md「恢复演练记录」:"
printf '| %s | %s | %ss | 数据库、角色、密钥环、配置一致，入口两个域名应答正确；日志积压时拒绝备份、排空后的四条调用随 set 恢复，覆盖恢复隔离后来的两条；副本领先时旧桶不动、新桶从第一条复制 | 通过 |\n' \
  "$(date -u '+%Y-%m-%d %H:%M')" "$set_name" "$rto"
