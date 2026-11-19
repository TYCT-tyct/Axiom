#!/usr/bin/env bash
set -euo pipefail

group=$2
archive="$RUNNER_TEMP/axiom-$group-images.tar"
case "$group" in
  tests)
    # Keep these in sync with pgtest, kvtest, containertest, websearch and testcontainers-go's reaper.
    images=(postgres:18.6-alpine valkey/valkey:9.1.2-alpine chrislusf/seaweedfs:3.80
      nginx:alpine searxng/searxng:2026.10.30-ffa88e3ba testcontainers/ryuk:0.14.0)
    local_images=("${images[@]}")
    ;;
  web)
    images=("$WEB_IMAGE")
    # docker save/load preserves tags. A digest-only reference can be lost when loading into Docker's classic store.
    local_images=("axiom-ci-playwright:${WEB_IMAGE##*@sha256:}")
    ;;
esac

if [ "$1" = key ]; then
  digest=$(printf '%s\n' "${images[@]}" | sha256sum)
  printf 'key=%s-%s\narchive=%s\n' "$group" "${digest%% *}" "$archive" >> "$GITHUB_OUTPUT"
  exit 0
fi

if [ -f "$archive" ] && docker image load --input "$archive" && docker image inspect "${local_images[@]}" >/dev/null; then
  printf "Loaded cached $group image: %s\n" "${local_images[@]}"
else
  for i in "${!images[@]}"; do
    image=${images[i]}
    if ! docker image inspect "$image" >/dev/null 2>&1; then
      echo "Pulling $image"
      docker image pull "$image" || { echo "Retrying pull of $image"; docker image pull "$image"; }
    fi
    if [ "$image" != "${local_images[i]}" ]; then
      docker image tag "$image" "${local_images[i]}"
    fi
  done
  docker image save --output "$archive" "${local_images[@]}"
  echo "archive-ready=true" >> "$GITHUB_OUTPUT"
fi
if [ "$group" = web ]; then
  echo "AXIOM_CACHED_WEB_IMAGE=${local_images[0]}" >> "$GITHUB_ENV"
fi
