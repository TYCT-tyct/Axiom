#!/usr/bin/env bash
# CI's deployment checks (ci.yml, job build): deploy this tree with `just up`, check the node runs it, upgrade a
# deployment from before the migration administrator in place, and always tear down with the database. ci.yml runs
# this under a host-wide lock, because the stack uses fixed names a second job on the same host would collide with.
set -euo pipefail
trap 'just down -v' EXIT

timeout 300 just up # one command deploys this machine and waits for a ready node

# The node answering runs the program built from this tree, not a stale image; the passwords are its owner's only
deployed="$(curl -fsS http://127.0.0.1:8080/api/console/v1/version | jq -r .version)"
built="$(just axiom-version)"
[ "$deployed" = "$built" ] || { echo "::error::the node runs $deployed, this tree builds $built"; exit 1; }
[ "$(stat -c %a deploy/.env)" = 600 ] || { echo "::error::deploy/.env is readable by others"; exit 1; }

# A deployment from before the migration administrator: no such role, an owner role that role did not create, and
# a deploy/.env without its password, readable by others and lacking a final newline. just up upgrades it in place.
docker compose -f deploy/compose.yaml exec -T postgres psql -v ON_ERROR_STOP=1 -U postgres -d axiom -c 'DROP OWNED BY axiom_migrator' -c 'DROP ROLE axiom_migrator'
kept="$(grep -v '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env)"
printf '%s' "$kept" > deploy/.env && chmod 644 deploy/.env
timeout 300 just up
[ "$(grep -v '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env)" = "$kept" ] && [ "$(grep -c '^AXIOM_MIGRATOR_PASSWORD=.' deploy/.env)" = 1 ] || { echo "::error::upgrading deploy/.env changed a password or did not add one"; exit 1; }
[ "$(stat -c %a deploy/.env)" = 600 ] || { echo "::error::upgrading left deploy/.env readable by others"; exit 1; }
timeout 300 just up # the next start, which repeats every grant on roles that now have it
