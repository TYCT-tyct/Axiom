#!/usr/bin/env bash
# Verify a release image before anything pulls it: the reference must be a digest, that digest must match the retained
# release record, and the image must carry a keyless cosign signature and an SPDX attestation made by this repository's
# release workflow. An unsigned image, a different digest, or a signature from another workflow or ref is refused.
#
# CI runs this against its loopback registry (RELEASE_ALLOW_INSECURE_REGISTRY=true); a deployment runs it against the
# registry the owner uploaded to, where the default identity is exactly the main branch's release workflow.
set -euo pipefail

image=${1:?usage: verify-release.sh <repository@sha256:...> [release.json]}
metadata=${2:-}
registry_args=()
if [ "${RELEASE_ALLOW_INSECURE_REGISTRY:-false}" = true ]; then
  registry_args+=(--allow-insecure-registry --allow-http-registry)
fi

die() { echo "::error::$*" >&2; exit 1; }

[[ "$image" =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]] || die "a release image must be verified by digest: $image"
digest=${image##*@}

if [ -n "${RELEASE_SIGNING_IDENTITY:-}" ]; then
  identity=$RELEASE_SIGNING_IDENTITY
elif [ "${GITHUB_ACTIONS:-false}" = true ]; then
  : "${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required inside GitHub Actions}"
  : "${GITHUB_REF:?GITHUB_REF is required inside GitHub Actions}"
  identity="https://github.com/$GITHUB_REPOSITORY/.github/workflows/ci.yml@$GITHUB_REF"
else
  identity=https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main
fi
issuer=${RELEASE_SIGNING_ISSUER:-https://token.actions.githubusercontent.com}

if [ -n "$metadata" ]; then
  [ -f "$metadata" ] || die "release record is missing: $metadata"
  recorded_digest=$(jq -r '.image_digest // empty' "$metadata")
  [ "$recorded_digest" = "$digest" ] || die "release record names $recorded_digest, not $digest"
  recorded_identity=$(jq -r '.signing_identity // empty' "$metadata")
  [ -z "$recorded_identity" ] || [ "$recorded_identity" = "$identity" ] \
    || die "release record was signed by $recorded_identity, not $identity"
  recorded_issuer=$(jq -r '.signing_issuer // empty' "$metadata")
  [ -z "$recorded_issuer" ] || [ "$recorded_issuer" = "$issuer" ] \
    || die "release record names issuer $recorded_issuer, not $issuer"
fi

cosign verify "${registry_args[@]}" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$image" > /dev/null \
  || die "no valid keyless signature from $identity on $image"

attestation=$(mktemp)
trap 'rm -f "$attestation"' EXIT
cosign verify-attestation "${registry_args[@]}" \
  --type spdxjson \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$image" > "$attestation" \
  || die "no valid SPDX attestation from $identity on $image"
jq -s -e --arg digest "${digest#sha256:}" '
  [ .[] | if type == "array" then .[] else . end
    | .payload | @base64d | fromjson
    | select(.predicateType == "https://spdx.dev/Document")
    | .subject[]? | select(.digest.sha256 == $digest) ] | length > 0
' "$attestation" > /dev/null \
  || die "the SPDX attestation does not name $digest"

echo "verified $image by $identity"
