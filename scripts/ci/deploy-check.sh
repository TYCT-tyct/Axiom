#!/usr/bin/env bash
# CI's deployment checks (ci.yml, job deploy): build once or consume the release digest, check the node runs it, upgrade a
# deployment from before the migration administrator in place, and always tear down with the database.
#
# The job owns its compose project, image tag and volumes, named from RUNNER_NAME and its workspace path
# (scripts/deploy_name.py), and publishes host ports the kernel picks, so jobs on the runners sharing this host never
# collide and need no lock. A runner runs one job at a time and keeps its workspace, so the next job on it removes
# whatever a job killed before its trap reached left. If that runner never runs a deploy check again, the next deploy
# check on any runner removes it instead: the teardown only ever names this job's project, and clean_stale below only
# removes projects that carry this script's label (deploy/compose.yaml), are named axiom-ci-*, and whose containers are
# all older than the 40-minute step timeout — a running job's stack is younger, and nothing else is inspected.
set -euo pipefail

job="$(python3 scripts/deploy_name.py "${RUNNER_NAME:-local-$(hostname)}" "${GITHUB_WORKSPACE:-$(pwd)}")"
export COMPOSE_PROJECT_NAME="axiom-ci-$job"
export DEPLOY_IMAGE="${CI_DEPLOY_IMAGE:-axiom-ci:$job}"
export DEPLOY_RESTART_POLICY=no # a CI stack must not start again after a killed job leaves it behind
# Empty: the compose file publishes a port the kernel picks (deploy/compose.yaml), which no other job can hold.
export DEPLOY_PORT= DEPLOY_ADMIN_PORT=

compose() { docker compose -f deploy/compose.yaml "$@"; }

# No 90-second graceful stop in CI (the compose file's default): a short one finishes before the runner kills the
# step, and `down` by project name works before deploy/.env exists.
down() { docker compose --project-name "$COMPOSE_PROJECT_NAME" down --volumes --timeout 5; }

# Both steps always run: failing to stop the containers must not leave the image behind.
teardown() {
  down || true
  if [ -z "${CI_DEPLOY_IMAGE:-}" ]; then docker image rm --force "$DEPLOY_IMAGE" > /dev/null 2>&1 || true; fi
}

# Keep the status the script already had: neither a failed teardown nor a successful one may change it.
finish() {
  status=$?
  teardown
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The step timeout in ci.yml is 40 minutes: no job of this script can still be running after it, so an older stack was
# abandoned. The label and the axiom-ci- prefix keep this scan away from every other container on the host, and the
# project name keeps `down` scoped to one stack.
STALE_AFTER=2400 # seconds
clean_stale() {
  local id project created now
  local -A newest=()
  for id in $(docker ps -aq --filter label=axiom.ci.deploy-check=true); do
    project="$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' "$id" 2>/dev/null)" || continue
    case "$project" in axiom-ci-*) ;; *) continue ;; esac
    created="$(date -d "$(docker inspect --format '{{.Created}}' "$id" 2>/dev/null)" +%s 2>/dev/null)" || continue
    if [ "${newest[$project]:-0}" -lt "$created" ]; then newest[$project]=$created; fi
  done
  now=$(date +%s)
  for project in "${!newest[@]}"; do
    [ $(( now - newest[$project] )) -ge "$STALE_AFTER" ] || continue
    echo "removing the abandoned CI deploy stack $project"
    docker compose --project-name "$project" down --volumes --timeout 5 || true
    docker image rm --force "axiom-ci:${project#axiom-ci-}" > /dev/null 2>&1 || true
  done
}

# Whatever a killed job left: this runner's own stack first, then any abandoned one.
teardown
clean_stale

if [ -n "${CI_DEPLOY_IMAGE:-}" ]; then
  [[ "$CI_DEPLOY_IMAGE" =~ @sha256:[0-9a-f]{64}$ ]] || { echo "::error::release deployment requires a digest"; exit 1; }
  built="${CI_DEPLOY_VERSION:?release version is required}"
else
  just web-embed
  mkdir -p deploy/bin
  CGO_ENABLED=0 GOOS=linux go build -trimpath -o deploy/bin/axiom ./cmd/axiom
  built="$(just axiom-version)"
  docker build --tag "$DEPLOY_IMAGE" deploy
fi

# Reuse the same image for every start, including the legacy-role upgrade. Only CI's own environment is initialized.
start() {
  umask 077
  if [ ! -f deploy/.env ]; then
    printf 'AXIOM_POSTGRES_PASSWORD=%s\nAXIOM_APP_PASSWORD=%s\n' "$(openssl rand -hex 24)" "$(openssl rand -hex 24)" > deploy/.env
  fi
  if ! grep -q '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env; then
    { [ -z "$(tail -c 1 deploy/.env)" ] || echo; printf 'AXIOM_MIGRATOR_PASSWORD=%s\n' "$(openssl rand -hex 24)"; } >> deploy/.env
  fi
  chmod 600 deploy/.env
  timeout 90 docker compose -f deploy/compose.yaml up --detach --no-build --wait --wait-timeout 60 postgres
  compose exec -T postgres sh < deploy/postgres/roles.sh
  timeout 180 docker compose -f deploy/compose.yaml up --detach --no-build --wait --wait-timeout 60
  # Include exited one-shot services before teardown, so their resource policy cannot escape inspection.
  local id ids parent
  ids=$(compose ps --all --quiet)
  for id in $ids; do
    parent=$(docker inspect --format '{{.HostConfig.CgroupParent}}' "$id")
    docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}} cgroup_parent={{json .HostConfig.CgroupParent}}' "$id"
    [ -z "${AXIOM_TEST_CGROUP_PARENT:-}" ] || [ "$parent" = "$AXIOM_TEST_CGROUP_PARENT" ] || { echo "::error::deployment container $id has unexpected cgroup parent"; return 1; }
  done
  if [ -n "${CI_DEPLOY_IMAGE:-}" ]; then
    for service in axiom migrate; do
      id=$(compose ps --all --quiet "$service")
      [ "$(docker inspect --format '{{.Config.Image}}' "$id")" = "$DEPLOY_IMAGE" ] || {
        echo "::error::$service did not consume the release digest"; return 1;
      }
    done
  fi
  public="$(compose port axiom 8080)"
  public="${public##*:}"
  admin="$(compose port axiom 8082)"
  admin="${admin##*:}"
}

start

# The node answering runs the program built from this tree, not a stale image; the passwords are its owner's only
deployed="$(curl -fsS "http://127.0.0.1:$public/api/console/v1/version" | jq -r .version)"
[ "$deployed" = "$built" ] || { echo "::error::the node runs $deployed, this tree builds $built"; exit 1; }
[ "$(stat -c %a deploy/.env)" = 600 ] || { echo "::error::deploy/.env is readable by others"; exit 1; }
# The console's other operations are served by a node that read its key files and its contract: an address no site
# registers is the contract's 404
[ "$(curl -s "http://127.0.0.1:$public/api/console/v1/site" | jq -r .error.code)" = not_found ] || { echo "::error::the node does not answer the console's API as the contract says"; exit 1; }
# The admin listener runs the same build, and answers its own routes only
adminDeployed="$(curl -fsS "http://127.0.0.1:$admin/api/admin/v1/version" | jq -r .version)"
[ "$adminDeployed" = "$built" ] || { echo "::error::the admin listener runs $adminDeployed, this tree builds $built"; exit 1; }
[ "$(curl -s "http://127.0.0.1:$admin/api/admin/v1/users" | jq -r .error.code)" = not_found ] || { echo "::error::the admin listener does not answer the admin's API as the contract says"; exit 1; }

# The pages: the website and the console on the public port; the admin on its own port, and none of its files on the
# public one. The admin's own script is asked for, so the embedded files, their caching and their compression are what
# is checked.
for path in / /console/ /console/keys; do
  headers="$(curl -fsS -D - -o /dev/null -H 'Accept: text/html' "http://127.0.0.1:$public$path" | tr -d '\r')"
  grep -qi '^cache-control: no-store$' <<< "$headers" || { echo "::error::the public listener does not serve $path uncached"; exit 1; }
done
admin_script="$(curl -fsS "http://127.0.0.1:$admin/" | grep -o '/assets/index-[^"]*\.js' | head -1)"
[ -n "$admin_script" ] || { echo "::error::the admin listener serves no admin page"; exit 1; }
headers="$(curl -fsS -D - -o /dev/null -H 'Accept-Encoding: br' "http://127.0.0.1:$admin$admin_script" | tr -d '\r')"
grep -qi '^cache-control: public, max-age=31536000, immutable$' <<< "$headers" && grep -qi '^content-encoding: br$' <<< "$headers" || { echo "::error::the admin script is not served cached and compressed"; exit 1; }
[ "$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$public$admin_script")" = 404 ] || { echo "::error::the public listener serves the admin's files"; exit 1; }

# A deployment from before the migration administrator: no such role, an owner role that role did not create, and
# a deploy/.env without its password, readable by others and lacking a final newline. just up upgrades it in place.
compose exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d axiom -c 'DROP OWNED BY axiom_migrator' -c 'DROP ROLE axiom_migrator'
kept="$(grep -v '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env)"
printf '%s' "$kept" > deploy/.env && chmod 644 deploy/.env
start
[ "$(grep -v '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env)" = "$kept" ] && [ "$(grep -c '^AXIOM_MIGRATOR_PASSWORD=.' deploy/.env)" = 1 ] || { echo "::error::upgrading deploy/.env changed a password or did not add one"; exit 1; }
[ "$(stat -c %a deploy/.env)" = 600 ] || { echo "::error::upgrading left deploy/.env readable by others"; exit 1; }
start # the next start, which repeats every grant on roles that now have it
