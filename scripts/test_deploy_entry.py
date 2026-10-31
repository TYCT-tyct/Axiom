import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "scripts" / "ops" / "deploy.sh"
DIGEST = "sha256:" + "a" * 64
IMAGE = "registry.example/axiom@" + DIGEST

DOCKER = r'''#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s (DEPLOY_IMAGE=%s)\n' "$*" "${DEPLOY_IMAGE-}" >> "$CALLS"
case "$*" in
  *" config --format json"*) printf '%s\n' "$COMPOSE_CONFIG" ;;
  *" pull "*) exit "${PULL_STATUS:-0}" ;;
  *" up "*) exit "${UP_STATUS:-0}" ;;
esac
'''
# The verified attestation carries the SBOM the release artifact retains, so the predicate here is exactly that document.
COSIGN = r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  verify) exit "${VERIFY_STATUS:-0}" ;;
  verify-attestation)
    [ "${ATTEST_STATUS:-0}" = 0 ] || exit "$ATTEST_STATUS"
    image="${!#}"
    digest="${image##*@}"
    payload="$(jq -nc --arg d "${digest#sha256:}" '
      {_type: "https://in-toto.io/Statement/v0.1",
       subject: [{name: "x", digest: {sha256: $d}}],
       predicateType: "https://spdx.dev/Document",
       predicate: {spdxVersion: "SPDX-2.3"}}' | base64 | tr -d '\n')"
    printf '{"payload":"%s","payloadType":"application/vnd.in-toto+json","signatures":[]}\n' "$payload"
    ;;
esac
'''


@unittest.skipUnless(BASH, "Bash is unavailable")
class DeployEntry(unittest.TestCase):
    def run_deploy(self, config, env_value="registry.example/from-file:latest", exported=None,
                   verify_status=0, pull_status=0, up_status=0):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bindir = root / "bin"
            bindir.mkdir()
            for name, body in (("docker", DOCKER), ("cosign", COSIGN)):
                stub = bindir / name
                stub.write_text(body)
                stub.chmod(0o755)
            calls = root / "calls"
            env_file = root / "deployment.env"
            env_file.write_text(f"DEPLOY_IMAGE={env_value}\n")
            sbom = json.dumps({"spdxVersion": "SPDX-2.3"})
            (root / "sbom.spdx.json").write_text(sbom)
            record = root / "release.json"
            record.write_text(json.dumps({
                "image_digest": config["services"]["axiom"]["image"].rsplit("@", 1)[-1],
                "signing_identity": "https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main",
                "signing_issuer": "https://token.actions.githubusercontent.com",
                "sbom_sha256": hashlib.sha256(sbom.encode()).hexdigest(),
            }))
            environment = {**os.environ, "PATH": f"{bindir}:{os.environ['PATH']}", "CALLS": str(calls),
                           "COMPOSE_CONFIG": json.dumps(config), "VERIFY_STATUS": str(verify_status),
                           "PULL_STATUS": str(pull_status), "UP_STATUS": str(up_status)}
            for name in ("GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_REF", "RELEASE_SIGNING_IDENTITY",
                         "RELEASE_SIGNING_ISSUER", "RELEASE_ALLOW_INSECURE_REGISTRY", "DEPLOY_IMAGE"):
                environment.pop(name, None)
            if exported is not None:
                environment["DEPLOY_IMAGE"] = exported
            result = subprocess.run(
                [BASH, str(SCRIPT), "-f", "deploy/compose.yaml", "-f", "deploy/compose.edge.yaml",
                 "--env-file", str(env_file), str(record)],
                env=environment, cwd=ROOT, text=True, capture_output=True)
            return result, calls.read_text().splitlines() if calls.exists() else []

    def config(self, image=IMAGE, migrate=None):
        return {"services": {"axiom": {"image": image}, "migrate": {"image": migrate or image}}}

    def verified(self, calls):
        return [call for call in calls if call.startswith("verify ")]

    def compose_calls(self, calls):
        return [call for call in calls if call.startswith("docker compose ")]

    def test_verifies_the_resolved_digest_before_pulling_and_starting_it(self):
        result, calls = self.run_deploy(self.config())
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.verified(calls), [
            "verify --certificate-identity https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main"
            " --certificate-oidc-issuer https://token.actions.githubusercontent.com " + IMAGE])
        compose = self.compose_calls(calls)
        self.assertTrue(any(" pull axiom migrate " in call and f"DEPLOY_IMAGE={IMAGE}" in call for call in compose), compose)
        self.assertTrue(any(" up --detach --no-build --wait --wait-timeout 60 postgres " in call
                            and f"DEPLOY_IMAGE={IMAGE}" in call for call in compose), compose)
        self.assertTrue(any(" exec -T postgres " in call for call in compose), compose)
        self.assertLess(next(i for i, call in enumerate(calls) if call.startswith("verify ")),
                        next(i for i, call in enumerate(calls) if " pull " in call))

    def test_an_exported_image_is_the_one_verified_not_the_env_file(self):
        # Compose interpolation prefers the shell over --env-file, so the exported digest is what pull and up would use.
        # A failing verification must therefore stop before either runs, even though the env file names a tag that the
        # old sed-based entry would have extracted and verified.
        result, calls = self.run_deploy(self.config(), exported=IMAGE, verify_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid keyless signature", result.stderr)
        self.assertIn(IMAGE, self.verified(calls)[0])
        self.assertNotIn("from-file", " ".join(calls))
        self.assertFalse(any(" pull " in call or " up " in call for call in self.compose_calls(calls)), calls)

    def test_a_failed_verification_pulls_and_starts_nothing(self):
        result, calls = self.run_deploy(self.config(), verify_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid keyless signature", result.stderr)
        self.assertFalse(any(" pull " in call or " up " in call for call in self.compose_calls(calls)), calls)

    def test_a_tag_in_the_resolved_configuration_is_refused_before_verification(self):
        result, calls = self.run_deploy(self.config(image="registry.example/axiom:latest"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be a digest", result.stderr)
        self.assertEqual(self.verified(calls), [])
        self.assertFalse(any(" pull " in call or " up " in call for call in self.compose_calls(calls)), calls)

    def test_services_that_would_run_different_images_are_refused(self):
        result, calls = self.run_deploy(self.config(image=IMAGE, migrate="registry.example/axiom@sha256:" + "b" * 64))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("different images", result.stderr)
        self.assertEqual(self.verified(calls), [])
        self.assertFalse(any(" pull " in call or " up " in call for call in self.compose_calls(calls)), calls)

    def test_a_failed_pull_stops_before_starting(self):
        result, calls = self.run_deploy(self.config(), pull_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(" up " in call for call in self.compose_calls(calls)), calls)


if __name__ == "__main__":
    unittest.main()
