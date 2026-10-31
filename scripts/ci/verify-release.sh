#!/usr/bin/env bash
# Verify a release image before anything pulls it: the reference must be a digest, that digest must match the retained
# release record, and the image must carry a keyless cosign signature and an SPDX attestation made by this repository's
# release workflow. The sbom.spdx.json beside the record must hash to the record's sbom_sha256 and, after normalizing
# whitespace and key order, be exactly the predicate of an attestation that verified for this digest: the record and the
# file are unsigned, so a hash alone proves only that they were copied together. An unsigned image, a different digest, a
# signature from another workflow or ref, or an SBOM the signature does not cover is refused.
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
command -v jq > /dev/null || die "jq is required"
command -v cosign > /dev/null || die "cosign is not on PATH (scripts/ci/install-release-tools.sh)"

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

sbom=
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
  # The artifact the owner uploads keeps the SBOM beside the record. Its recorded hash catches a damaged copy; the
  # signature comparison below is what makes it trustworthy.
  sbom=$(dirname "$metadata")/sbom.spdx.json
  [ -f "$sbom" ] || die "the release record must sit beside its SBOM: $sbom is missing"
  recorded_sbom=$(jq -r '.sbom_sha256 // empty' "$metadata")
  [ -n "$recorded_sbom" ] || die "release record has no sbom_sha256"
  actual_sbom=$(sha256sum "$sbom" | cut -d ' ' -f1)
  [ "$actual_sbom" = "$recorded_sbom" ] || die "the SBOM $sbom hashes to $actual_sbom, not the recorded $recorded_sbom"
  # jq -s accepts JSON Lines and several top-level values; the artifact promises one SPDX document, and a trailing
  # object must not be silently ignored by the predicate comparison below.
  jq -e -s 'length == 1 and (.[0] | type == "object")' "$sbom" > /dev/null \
    || die "the SBOM $sbom must be exactly one JSON object"
fi

temps=()
cleanup() { rm -f "${temps[@]}"; }
trap cleanup EXIT

cosign verify "${registry_args[@]}" \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$image" > /dev/null \
  || die "no valid keyless signature from $identity on $image"

attestation=$(mktemp)
temps+=("$attestation")
cosign verify-attestation "${registry_args[@]}" \
  --type spdxjson \
  --certificate-identity "$identity" \
  --certificate-oidc-issuer "$issuer" \
  "$image" > "$attestation" \
  || die "no valid SPDX attestation from $identity on $image"
predicates=$(mktemp)
temps+=("$predicates")
jq -s -c --arg digest "${digest#sha256:}" '
  [ .[] | if type == "array" then .[] else . end
    | .payload | @base64d | fromjson
    | select(.predicateType == "https://spdx.dev/Document")
    | select(any(.subject[]?; .digest.sha256 == $digest))
    | .predicate ]
' "$attestation" > "$predicates" \
  || die "the SPDX attestation is not readable"
[ "$(jq length "$predicates")" -gt 0 ] || die "the SPDX attestation does not name $digest"

# Compare the retained SBOM with the signed predicate, not with the record: whoever swaps the file can update the record
# in the same edit, but cannot make the signature cover the new bytes.
if [ -n "${sbom:-}" ]; then
  jq -e --slurpfile sbom "$sbom" 'any(.[]; . == $sbom[0])' "$predicates" > /dev/null \
    || die "the SBOM $sbom is not the verified attestation's predicate"
fi

echo "verified $image by $identity"
