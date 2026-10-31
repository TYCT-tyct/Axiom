#!/usr/bin/env bash
# Sign a throwaway image with the release's keyless flow and prove the verifier accepts that digest and its SBOM but
# refuses a different digest, a tag, another repository/workflow/ref/issuer (with release metadata that matches the
# expected identity, so cosign and not the metadata check refuses it), a signature moved from another digest, an SBOM
# attestation whose subject is another digest, and an SBOM swapped together with its recorded hash. Runs where a release
# can: trains and main, never a pull request, because it needs the workflow's GitHub OIDC identity and writes to the
# public transparency log.
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

# A negative that must be decided after the release record, not by it. The verifier's message proves the command got past
# the metadata checks; the cosign message, where given, proves the cryptographic check is what refused it.
refuse_after_record() { # what, verifier message, cosign message (empty for the verifier's own check), command...
  local what=$1 expected=$2 cosign_expected=$3 output
  shift 3
  if output=$("$@" 2>&1); then
    echo "::error::the verifier accepted $what"
    exit 1
  fi
  if ! grep -qF "$expected" <<< "$output"; then
    echo "::error::$what was refused before the verifier reached cosign"
    printf '%s\n' "$output"
    exit 1
  fi
  if [ -n "$cosign_expected" ] && ! grep -qF "$cosign_expected" <<< "$output"; then
    echo "::error::$what failed for a different reason than $cosign_expected"
    printf '%s\n' "$output"
    exit 1
  fi
  echo "refused $what after the release record"
}

signed="$(build valid)"
digest=${signed##*@}
sbom="$tmp/sbom.spdx.json"
SYFT_REGISTRY_INSECURE_USE_HTTP=true \
  syft "registry:$signed" --output "spdx-json=$sbom" --quiet
cosign sign --yes --oidc-provider=github-actions "${cosign_args[@]}" "$signed"
cosign attest --yes --oidc-provider=github-actions "${cosign_args[@]}" \
  --type spdxjson --predicate "$sbom" "$signed"
cosign save --dir "$tmp/signed" "${cosign_args[@]}" "$signed"
sbom_sha256="$(sha256sum "$sbom" | cut -d ' ' -f1)"
record() { # digest identity issuer output: the record an operator would have received with the artifact
  jq -n --arg digest "$1" --arg identity "$2" --arg issuer "$3" --arg sbom "$sbom_sha256" \
    '{image_digest: $digest, signing_identity: $identity, signing_issuer: $issuer, sbom_sha256: $sbom}' > "$4"
}
record "$digest" "$identity" "$issuer" "$tmp/release.json"

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
tampered_digest=${tampered##*@}
record "$tampered_digest" "$identity" "$issuer" "$tmp/tampered.json"
refuse_after_record "an unsigned digest" "no valid keyless signature from" "" \
  verify "$tampered" "$tmp/tampered.json"
expect_refusal "a digest that differs from the release record" verify "$tampered" "$tmp/release.json"
expect_refusal "a tag" verify "$repo:valid" "$tmp/release.json"
jq '.image_digest = "sha256:" + ("0" * 64)' "$tmp/release.json" > "$tmp/wrong.json"
expect_refusal "a release record with another digest" verify "$signed" "$tmp/wrong.json"

# Every identity negative passes a record that names the expected identity, so the metadata checks pass and cosign has to
# refuse the certificate. Omitting the record would also reach cosign, but then a broken metadata check could hide.
refuse_identity() { # what identity issuer
  record "$digest" "$2" "$3" "$tmp/identity.json"
  refuse_after_record "$1" "no valid keyless signature from" "no matching CertificateIdentity found" \
    env RELEASE_SIGNING_IDENTITY="$2" RELEASE_SIGNING_ISSUER="$3" RELEASE_ALLOW_INSECURE_REGISTRY=true \
    bash scripts/ci/verify-release.sh "$signed" "$tmp/identity.json"
}
refuse_identity "an identity from another repository" \
  "https://github.com/elsewhere/Axiom/.github/workflows/ci.yml@refs/heads/main" "$issuer"
refuse_identity "a workflow other than ci.yml" \
  "https://github.com/$GITHUB_REPOSITORY/.github/workflows/other.yml@$GITHUB_REF" "$issuer"
refuse_identity "another workflow ref" \
  "https://github.com/$GITHUB_REPOSITORY/.github/workflows/ci.yml@refs/heads/not-this-ref" "$issuer"
refuse_identity "a certificate from another issuer" "$identity" "https://issuer.example"

# A's signature moved onto B: cosign 3 stores signatures and attestations in one OCI index under the image digest tag
# (`sha256-<hex>`), so copying that tag attaches A's bundles to B without changing B's image. The signed payload still
# names A, so cosign must refuse it rather than merely finding no signature at all.
copy_bundles() { # from-digest to-digest
  local from=$1 to=$2
  curl -fsS -H 'Accept: application/vnd.oci.image.index.v1+json' \
    "http://${repo%/*}/v2/${repo##*/}/manifests/sha256-${from#sha256:}" > "$tmp/bundle-index.json"
  curl -fsS -X PUT -H 'Content-Type: application/vnd.oci.image.index.v1+json' \
    --data-binary "@$tmp/bundle-index.json" \
    "http://${repo%/*}/v2/${repo##*/}/manifests/sha256-${to#sha256:}" > /dev/null
}
moved="$(build moved)"
moved_digest=${moved##*@}
copy_bundles "$digest" "$moved_digest"
record "$moved_digest" "$identity" "$issuer" "$tmp/moved.json"
refuse_after_record "a signature moved from $digest to $moved_digest" "no valid keyless signature from" \
  "provided artifact digest does not match any digest in statement" \
  env RELEASE_SIGNING_IDENTITY="$identity" RELEASE_SIGNING_ISSUER="$issuer" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  bash scripts/ci/verify-release.sh "$moved" "$tmp/moved.json"

# A valid signature and attestation under the right identity, but the attestation's subject is A: add only A's SPDX
# attestation descriptor to C's index, leaving C's own signature. cosign 3's bundle path verifies every bundle against
# the target digest for both `verify` and `verify-attestation`, so A's statement fails the signature query first; the
# positive case covers a matching attestation and the swapped-SBOM case covers the predicate comparison.
add_attestation() { # from-digest to-digest
  local from=$1 to=$2
  curl -fsS -H 'Accept: application/vnd.oci.image.index.v1+json' \
    "http://${repo%/*}/v2/${repo##*/}/manifests/sha256-${from#sha256:}" > "$tmp/from-index.json"
  curl -fsS -H 'Accept: application/vnd.oci.image.index.v1+json' \
    "http://${repo%/*}/v2/${repo##*/}/manifests/sha256-${to#sha256:}" > "$tmp/to-index.json"
  jq --slurpfile from "$tmp/from-index.json" '
    .manifests += [
      $from[0].manifests[]
      | select(.annotations["dev.sigstore.bundle.predicateType"] == "https://spdx.dev/Document")
    ]
  ' "$tmp/to-index.json" > "$tmp/merged-index.json"
  curl -fsS -X PUT -H 'Content-Type: application/vnd.oci.image.index.v1+json' \
    --data-binary "@$tmp/merged-index.json" \
    "http://${repo%/*}/v2/${repo##*/}/manifests/sha256-${to#sha256:}" > /dev/null
}
attested="$(build attested)"
attested_digest=${attested##*@}
cosign sign --yes --oidc-provider=github-actions "${cosign_args[@]}" "$attested"
add_attestation "$digest" "$attested_digest"
record "$attested_digest" "$identity" "$issuer" "$tmp/attested.json"
refuse_after_record "an SBOM attestation whose subject is another digest" "no valid keyless signature from" \
  "provided artifact digest does not match any digest in statement" \
  env RELEASE_SIGNING_IDENTITY="$identity" RELEASE_SIGNING_ISSUER="$issuer" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  bash scripts/ci/verify-release.sh "$attested" "$tmp/attested.json"

# The record and the local SBOM are unsigned, so an attacker can replace both and update sbom_sha256. Only the verified
# attestation's predicate can tell the retained SBOM from the one the release signed.
mkdir -p "$tmp/swapped"
jq '.name = "swapped"' "$sbom" > "$tmp/swapped/sbom.spdx.json"
swapped_sha256="$(sha256sum "$tmp/swapped/sbom.spdx.json" | cut -d ' ' -f1)"
jq -n --arg digest "$digest" --arg identity "$identity" --arg issuer "$issuer" --arg sbom "$swapped_sha256" \
  '{image_digest: $digest, signing_identity: $identity, signing_issuer: $issuer, sbom_sha256: $sbom}' \
  > "$tmp/swapped/release.json"
refuse_after_record "an SBOM swapped together with its recorded hash" "is not the verified attestation's predicate" "" \
  env RELEASE_SIGNING_IDENTITY="$identity" RELEASE_SIGNING_ISSUER="$issuer" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  bash scripts/ci/verify-release.sh "$signed" "$tmp/swapped/release.json"

echo "signed a throwaway image by digest and refused every tampered reference"
