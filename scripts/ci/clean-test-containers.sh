#!/usr/bin/env bash
# Only go-test containers carry this label. Remove this attempt first, then abandoned containers from other jobs.
set -euo pipefail

: "${AXIOM_TEST_JOB:?the CI job identity is required for cleanup}"
ids=$(docker ps -aq --filter "label=axiom.ci.job=$AXIOM_TEST_JOB")
for id in $ids; do
  docker rm -fv "$id" || true # another cleanup or normal termination may have removed it already
done

# The whole go-test job is limited to 40 minutes in ci.yml, so an older container cannot belong to a live job.
STALE_AFTER=2400 # seconds
now=$(date +%s)
ids=$(docker ps -aq --filter label=axiom.ci.job)
for id in $ids; do
  created=$(docker inspect --format '{{.Created}}' "$id" 2>/dev/null) || continue
  created=$(date -d "$created" +%s 2>/dev/null) || continue
  [ $(( now - created )) -ge "$STALE_AFTER" ] || continue
  echo "removing abandoned CI test container $id"
  docker rm -fv "$id" || true
done
