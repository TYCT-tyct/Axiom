#!/usr/bin/env bash
# Restores one backup set onto this machine and starts the program on it (docs/ops/backup-restore.md「恢复」).
#
#   AXIOM_RESTORE_DB_IDENTITY=/etc/axiom-backup/db.key \
#   AXIOM_RESTORE_SECRETS_IDENTITY=/etc/axiom-backup/keys.key \
#   scripts/ops/restore.sh <set>
#
# The identities are this machine's paths to the age private keys; they are mounted read-only for the run and stay where
# they are. --env-file names the compose variables and is the file the set's own copy replaces: the cluster is created
# with the passwords of the set, not of the host, because a database created from other passwords would not accept them.
# On a blank machine that file needs only the bucket's settings (deploy/.env.example); the three passwords are added here
# as placeholders until step 1 installs the set's copy, and nothing is ever created with them.
#
# A blank environment is the supported one: no database. --overwrite starts that over by deleting the project's database
# and key ring, which is what a retry after a failed restore needs.
#
# The images have to be on this machine already: building them is the deployment's build step (what `just up` does), and
# a restore that built something else would install the wrong program.
set -euo pipefail

env_file=deploy/.env
overwrite=
set_name=
while [ $# -gt 0 ]; do
  case $1 in
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

die() {
  printf 'restore: %s\n' "$*" >&2
  exit 1
}

# Docker Desktop on Windows cannot mount a git bash path such as /tmp/tmp.abc: the daemon needs the Windows form, which
# cygpath prints. Everywhere else cygpath is absent and the path is passed through unchanged.
docker_path() {
  if command -v cygpath > /dev/null 2>&1; then cygpath -w "$1"; else printf '%s' "$1"; fi
}

[ -n "$set_name" ] || die "usage: scripts/ops/restore.sh [--env-file FILE] [--overwrite] <set>"
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
compose() { docker compose -f deploy/compose.yaml --env-file "$env_file" "$@"; }

# Every image the restore uses has to be on this machine before anything is replaced, or a restore would stop halfway. They
# are not pulled: two of them are built from this tree, and a newer one from a registry could carry migrations the set
# predates, which is what step 5 warns about. The profile is named only so that the config lists the job's own image;
# nothing activates it, because `up` would then start the backup job as well.
for image in $(compose --profile ops config --images); do
  docker image inspect "$image" > /dev/null 2>&1 ||
    die "the image $image is not on this machine: build the deployment first (docs/ops/backup-restore.md「恢复」)"
done

# Nothing may write to the database while it is replaced.
[ -z "$(compose ps --quiet axiom)" ] ||
  die "the program is running: docker compose --project-name $project stop axiom"

if docker volume ls --quiet --filter "name=^${project}_postgres$" | grep -q .; then
  [ -n "$overwrite" ] ||
    die "this project already has a database and a restore replaces it: rerun with --overwrite to delete it and start over"
  echo "== deleting this project's database and key ring"
  # Only the containers that use those two volumes: `down` would also stop whatever else shares the project, such as the
  # rehearsal's stand-in for the bucket, which the restore still has to read the set from.
  compose rm --stop --force axiom migrate postgres keys > /dev/null
  docker volume rm "${project}_postgres" "${project}_keys" > /dev/null
fi

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

echo "== 2/6 create the cluster from it, with the login roles the image's init scripts make"
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
compose up --detach --wait --wait-timeout 180

public=$(compose port axiom 8080)
public=${public##*:}
admin=$(compose port axiom 8082)
admin=${admin##*:}
echo "== console: $(curl -fsS "http://127.0.0.1:$public/api/console/v1/version")"
echo "== admin: $(curl -fsS "http://127.0.0.1:$admin/api/admin/v1/version")"
compose ps
echo "== restored $set_name"
