#!/usr/bin/env bash
# CI's deployment checks (ci.yml, job build): deploy this tree with `just up`, check the node runs it, upgrade a
# deployment from before the migration administrator in place, and always tear down with the database. Each job gets its
# own compose project, image tag, volumes (compose prefixes them with the project) and host ports, so jobs on the runners
# sharing a host need no lock; local `just up` keeps its defaults.
set -euo pipefail

# The runner names the job's stack: a runner runs one job at a time, and its name stays the same between them, so the
# next job on it can remove what a job that was killed before its trap ran left behind — no trap survives a SIGKILL.
job="$(printf '%s' "${RUNNER_NAME:-local-$(hostname)}" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-' '-')"
export COMPOSE_PROJECT_NAME="axiom-ci-$job"
export DEPLOY_IMAGE="axiom-ci:$job"
# Empty: the compose file publishes a port the kernel picks (deploy/compose.yaml), which no other job can hold.
export DEPLOY_PORT= DEPLOY_ADMIN_PORT=

compose() { docker compose -f deploy/compose.yaml "$@"; }

# A killed job leaves its containers, volumes and image behind, so every run starts and ends with nothing; the volumes
# go with `down -v`, which knows them from the project name.
teardown() {
  just down -v
  docker image rm --force "$DEPLOY_IMAGE" > /dev/null 2>&1 || true
}
trap teardown EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
teardown

# Start the stack and learn the ports it published: --build can replace the container, and with it the mapping.
start() {
  timeout 300 just up # one command deploys this machine and waits for a ready node
  public="$(compose port axiom 8080)"
  public="${public##*:}"
  admin="$(compose port axiom 8082)"
  admin="${admin##*:}"
}

start

# The node answering runs the program built from this tree, not a stale image; the passwords are its owner's only
deployed="$(curl -fsS "http://127.0.0.1:$public/api/console/v1/version" | jq -r .version)"
built="$(just axiom-version)"
[ "$deployed" = "$built" ] || { echo "::error::the node runs $deployed, this tree builds $built"; exit 1; }
[ "$(stat -c %a deploy/.env)" = 600 ] || { echo "::error::deploy/.env is readable by others"; exit 1; }
# The console's other operations are served by a node that read its key files and its contract: an address no site
# registers is the contract's 404
[ "$(curl -s "http://127.0.0.1:$public/api/console/v1/site" | jq -r .error.code)" = not_found ] || { echo "::error::the node does not answer the console's API as the contract says"; exit 1; }

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
