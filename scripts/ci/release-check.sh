#!/usr/bin/env bash
# Build one release, retain its OCI bytes, sign it by digest, and rehearse deployment through a loopback registry only.
set -euo pipefail
: "${RELEASE_DIR:?set a job-private artifact directory}"
: "${GITHUB_REPOSITORY:?the signature records the repository and workflow ref}"
: "${GITHUB_REF:?the signature records the repository and workflow ref}"
mkdir -p "$RELEASE_DIR"
job="$(python3 scripts/deploy_name.py "${RUNNER_NAME:-local-$(hostname)}" "${GITHUB_WORKSPACE:-$(pwd)}")"
registry="axiom-release-$job"
builder="axiom-release-$job"
image=
cleanup() {
  docker buildx rm --force "$builder" >/dev/null 2>&1 || true
  docker rm -fv "$registry" >/dev/null 2>&1 || true
  [ -z "$image" ] || docker image rm "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# The same runner cannot run two jobs at once; remove only its own interrupted release resources.
cleanup
registry_args=()
builder_args=(--driver-opt restart-policy=no)
if [ -n "${AXIOM_TEST_CGROUP_PARENT:-}" ]; then
  registry_args+=(--cgroup-parent "$AXIOM_TEST_CGROUP_PARENT")
  builder_args+=(--driver-opt "cgroup-parent=$AXIOM_TEST_CGROUP_PARENT")
fi
docker run -d --name "$registry" "${registry_args[@]}" -p 127.0.0.1::5000 registry:3.0.0
address="$(docker port "$registry" 5000/tcp)"
# Host networking is confined to the builder so it can reach this loopback registry; the registry never binds publicly.
docker buildx create --name "$builder" --driver docker-container --driver-opt network=host "${builder_args[@]}"
python3 scripts/release_image.py inputs "$RELEASE_DIR/release.json"
just web-embed
mkdir -p deploy/bin
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o deploy/bin/axiom ./cmd/axiom
label_args=()
while IFS= read -r label; do label_args+=(--label "$label"); done < <(python3 scripts/release_image.py labels "$RELEASE_DIR/release.json")
docker buildx build --builder "$builder" --platform linux/amd64 --provenance=false \
  "${label_args[@]}" \
  --output "type=oci,dest=$RELEASE_DIR/image.oci.tar,oci-mediatypes=true" \
  --output "type=image,name=$address/axiom:release,push=true,registry.insecure=true,oci-mediatypes=true" deploy
image="$address/axiom@$(python3 scripts/release_image.py seal "$RELEASE_DIR/release.json" "$RELEASE_DIR/image.oci.tar")"
# Sign the digest, not the tag: the tag can move, the digest is what the deployment consumes. The SBOM is generated from
# the same OCI archive the release retains, so the attestation names the bytes that were verified above.
tools="$(bash scripts/ci/install-release-tools.sh)"
export PATH="$tools:$PATH"
identity="https://github.com/$GITHUB_REPOSITORY/.github/workflows/ci.yml@$GITHUB_REF"
issuer=https://token.actions.githubusercontent.com
cosign_args=(--allow-insecure-registry --allow-http-registry)
cosign sign --yes --oidc-provider=github-actions "${cosign_args[@]}" "$image"
syft "oci-archive:$RELEASE_DIR/image.oci.tar" --output "spdx-json=$RELEASE_DIR/sbom.spdx.json" --quiet
cosign attest --yes --oidc-provider=github-actions "${cosign_args[@]}" \
  --type spdxjson --predicate "$RELEASE_DIR/sbom.spdx.json" "$image"
# The signature and attestation live beside the image in the loopback registry; save them with the image so the owner can
# upload both to the deployment registry without rebuilding, and the retained artifact carries the proof with the bytes.
cosign save --dir "$RELEASE_DIR/signed" "${cosign_args[@]}" "$image"
sbom_sha256="$(sha256sum "$RELEASE_DIR/sbom.spdx.json" | cut -d ' ' -f1)"
record="$(mktemp "$RELEASE_DIR/release.json.XXXXXX")"
jq --arg identity "$identity" --arg issuer "$issuer" --arg sbom "$sbom_sha256" \
  '.signing_identity = $identity | .signing_issuer = $issuer | .sbom_sha256 = $sbom' \
  "$RELEASE_DIR/release.json" > "$record"
mv -f "$record" "$RELEASE_DIR/release.json"
RELEASE_SIGNING_IDENTITY="$identity" RELEASE_SIGNING_ISSUER="$issuer" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  bash scripts/ci/verify-release.sh "$image" "$RELEASE_DIR/release.json"
# Pull the archive's exact digest: a divergent registry export fails before deployment.
docker pull "$image"
RELEASE_SIGNING_IDENTITY="$identity" RELEASE_ALLOW_INSECURE_REGISTRY=true \
  CI_DEPLOY_IMAGE="$image" CI_DEPLOY_RELEASE="$RELEASE_DIR/release.json" \
  CI_DEPLOY_VERSION="$(jq -r .frontend_version "$RELEASE_DIR/release.json")" \
  bash scripts/ci/deploy-check.sh
