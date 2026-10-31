#!/usr/bin/env bash
# Sign a throwaway image with the release's keyless flow and prove the verifier accepts that digest but refuses a
# different one, a tag, another workflow identity, and a release record naming another digest. Runs where a release can:
# trains and main, never a pull request, because it needs the workflow's GitHub OIDC identity and writes to the public
# transparency log.
set -euo pipefail
: "${GITHUB_REPOSITORY:?the signature records the repository and workflow ref}"
: "${GITHUB_REF:?the signature records the repository and workflow ref}"

job="$(python3 scripts/deploy_name.py "${RUNNER_NAME:-local-$(hostname)}" "${GITHUB_WORKSPACE:-$(pwd)}")"
registry="axiom-signing-$job"
builder="axiom-signing-$job"
tmp="$(mktemp -d)"
cleanup() {
  docker buildx rm --force "$builder" > /dev/null 2>&1 || true
  docker rm -fv "$registry" > /dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# The same runner cannot run two jobs at once; remove only its own interrupted signing resources.
docker buildx rm --force "$builder" > /dev/null 2>&1 || true
docker rm -fv "$registry" > /dev/null 2>&1 || true

tools="$(bash scripts/ci/install-release-tools.sh)"
export PATH="$tools:$PATH"
identity="https://github.com/$GITHUB_REPOSITORY/.github/workflows/ci.yml@$GITHUB_REF"
issuer=https://token.actions.githubusercontent.com
cosign_args=(--allow-insecure-registry --allow-http-registry)

registry_args=()
builder_args=(--driver-opt restart-policy=no)
if [ -n "${AXIOM_TEST_CGROUP_PARENT:-}" ]; then
  registry_args+=(--cgroup-parent "$AXIOM_TEST_CGROUP_PARENT")
  builder_args+=(--driver-opt "cgroup-parent=$AXIOM_TEST_CGROUP_PARENT")
fi
docker run -d --name "$registry" "${registry_args[@]}" -p 127.0.0.1::5000 registry:3.0.0 > /dev/null
repo="$(docker port "$registry" 5000/tcp)/axiom-signing"
# Host networking is confined to the builder so it can reach this loopback registry.
docker buildx create --name "$builder" --driver docker-container --driver-opt network=host "${builder_args[@]}"

build() { # tag: push a one-label scratch image and print its digest reference
  printf 'FROM scratch\nLABEL io.axiom.signing-check=%s\n' "$1" > "$tmp/Dockerfile"
  docker buildx build --builder "$builder" --platform linux/amd64 --provenance=false --push \
    --tag "$repo:$1" --file "$tmp/Dockerfile" "$tmp" > /dev/null
  printf '%s@%s\n' "$repo" \
    "$(docker buildx imagetools inspect "$repo:$1" --format '{{json .Manifest}}' | jq -r .digest)"
}

expect_refusal() { # what, then the command that must fail
  local what=$1
  shift
  if "$@" > /dev/null 2>&1; then
    echo "::error::the verifier accepted $what"
    exit 1
  fi
  echo "refused $what"
}

signed="$(build valid)"
digest=${signed##*@}
SYFT_REGISTRY_INSECURE_USE_HTTP=true \
  syft "registry:$signed" --output "spdx-json=$tmp/sbom.spdx.json" --quiet
cosign sign --yes --oidc-provider=github-actions "${cosign_args[@]}" "$signed"
cosign attest --yes --oidc-provider=github-actions "${cosign_args[@]}" \
  --type spdxjson --predicate "$tmp/sbom.spdx.json" "$signed"
cosign save --dir "$tmp/signed" "${cosign_args[@]}" "$signed"
jq -n --arg digest "$digest" --arg identity "$identity" --arg issuer "$issuer" \
  '{image_digest: $digest, signing_identity: $identity, signing_issuer: $issuer}' > "$tmp/release.json"

verify() {
  RELEASE_SIGNING_IDENTITY="$identity" RELEASE_SIGNING_ISSUER="$issuer" \
    RELEASE_ALLOW_INSECURE_REGISTRY=true bash scripts/ci/verify-release.sh "$@"
}

verify "$signed" "$tmp/release.json"
echo "accepted $digest signed by $identity"

# A saved release layout carries the image, its signature and its attestation to the registry the deployment pulls from.
roundtrip="$repo-roundtrip@$digest"
cosign load --dir "$tmp/signed" "${cosign_args[@]}" "$roundtrip"
verify "$roundtrip" "$tmp/release.json"
echo "accepted $digest after cosign save and load"

tampered="$(build tampered)"
expect_refusal "an unsigned digest" verify "$tampered"
expect_refusal "a digest that differs from the release record" verify "$tampered" "$tmp/release.json"
expect_refusal "a tag" verify "$repo:valid" "$tmp/release.json"
jq '.image_digest = "sha256:" + ("0" * 64)' "$tmp/release.json" > "$tmp/wrong.json"
expect_refusal "a release record with another digest" verify "$signed" "$tmp/wrong.json"
expect_refusal "a signature from another workflow identity" \
  env RELEASE_SIGNING_IDENTITY="https://github.com/$GITHUB_REPOSITORY/.github/workflows/ci.yml@refs/heads/not-this-ref" \
  RELEASE_SIGNING_ISSUER="$issuer" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  bash scripts/ci/verify-release.sh "$signed" "$tmp/release.json"

echo "signed a throwaway image by digest and refused every tampered reference"
