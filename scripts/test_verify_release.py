import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "scripts" / "ci" / "verify-release.sh"
DIGEST = "sha256:" + "a" * 64
IMAGE = "registry.example/axiom@" + DIGEST
IDENTITY = "https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main"
ISSUER = "https://token.actions.githubusercontent.com"
PREDICATE = {"spdxVersion": "SPDX-2.3"}

STUB = r'''#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  verify) exit "${VERIFY_STATUS:-0}" ;;
  verify-attestation)
    [ "${ATTEST_STATUS:-0}" = 0 ] || exit "$ATTEST_STATUS"
    image="${!#}"
    digest="${image##*@}"
    payload="$(jq -nc --arg d "${digest#sha256:}" --arg subject "${SUBJECT_DIGEST:-}" '
      {_type: "https://in-toto.io/Statement/v0.1",
       subject: [{name: "x", digest: {sha256: (if $subject == "" then $d else $subject end)}}],
       predicateType: "https://spdx.dev/Document",
       predicate: {spdxVersion: "SPDX-2.3"}}' | base64 | tr -d '\n')"
    printf '{"payload":"%s","payloadType":"application/vnd.in-toto+json","signatures":[]}\n' "$payload"
    ;;
esac
'''


class VerifyRelease(unittest.TestCase):
    def run_verify(self, image=IMAGE, metadata=None, env=None, verify_status=0, attest_status=0, subject_digest="",
                   sbom=None, sbom_sha256=None):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bindir = root / "bin"
            bindir.mkdir()
            stub = bindir / "cosign"
            stub.write_text(STUB)
            stub.chmod(0o755)
            calls = root / "calls"
            environment = {**os.environ, "PATH": f"{bindir}:{os.environ['PATH']}", "CALLS": calls.as_posix()}
            for name in ("GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_REF", "RELEASE_SIGNING_IDENTITY",
                         "RELEASE_SIGNING_ISSUER", "RELEASE_ALLOW_INSECURE_REGISTRY"):
                environment.pop(name, None)
            environment.update({"VERIFY_STATUS": str(verify_status), "ATTEST_STATUS": str(attest_status),
                                "SUBJECT_DIGEST": subject_digest})
            environment.update(env or {})
            args = [image]
            if metadata is not None:
                document = json.dumps(PREDICATE if sbom is None else sbom)
                (root / "sbom.spdx.json").write_text(document)
                record = dict(metadata)
                if "sbom_sha256" not in record:
                    record["sbom_sha256"] = sbom_sha256 or hashlib.sha256(document.encode()).hexdigest()
                path = root / "release.json"
                path.write_text(json.dumps(record))
                args.append(str(path))
            result = subprocess.run(["bash", str(SCRIPT), *args], env=environment, text=True, capture_output=True)
            return result, calls.read_text().splitlines() if calls.exists() else []

    def record(self, digest=DIGEST, identity=IDENTITY, issuer=ISSUER, **extra):
        return {"image_digest": digest, "signing_identity": identity, "signing_issuer": issuer, **extra}

    def test_accepts_a_signed_digest_and_checks_the_default_main_identity(self):
        result, calls = self.run_verify(metadata=self.record())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verified", result.stdout)
        self.assertEqual(len(calls), 2)
        for call in calls:
            self.assertIn("--certificate-identity " + IDENTITY, call)
            self.assertIn("--certificate-oidc-issuer " + ISSUER, call)
            self.assertNotIn("--allow-insecure-registry", call)

    def test_inside_actions_the_signature_must_match_the_running_ref(self):
        identity = "https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/train/main/1"
        result, calls = self.run_verify(
            metadata=self.record(identity=identity),
            env={"GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "TYCT-tyct/Axiom",
                 "GITHUB_REF": "refs/heads/train/main/1"})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--certificate-identity " + identity, calls[0])

    def test_refuses_a_tag_before_any_signature_check(self):
        result, calls = self.run_verify(image="registry.example/axiom:latest", metadata=self.record())
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be verified by digest", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_a_release_record_with_another_digest(self):
        result, calls = self.run_verify(metadata=self.record(digest="sha256:" + "b" * 64))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release record names", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_a_record_signed_by_another_identity(self):
        result, calls = self.run_verify(metadata=self.record(identity="https://github.com/elsewhere/workflow"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release record was signed by", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_a_record_from_another_issuer(self):
        result, calls = self.run_verify(metadata=self.record(issuer="https://issuer.example"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("release record names issuer", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_an_unsigned_image(self):
        result, calls = self.run_verify(metadata=self.record(), verify_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid keyless signature", result.stderr)
        self.assertEqual(len(calls), 1) # the attestation is not accepted in place of a signature

    def test_refuses_a_missing_or_invalid_attestation(self):
        result, _ = self.run_verify(metadata=self.record(), attest_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid SPDX attestation", result.stderr)

    def test_refuses_an_attestation_that_names_another_digest(self):
        result, _ = self.run_verify(metadata=self.record(), subject_digest="b" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("does not name", result.stderr)

    def test_refuses_an_sbom_that_does_not_match_the_recorded_hash(self):
        result, calls = self.run_verify(metadata=self.record(), sbom_sha256="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("not the recorded", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_a_release_record_without_an_sbom_hash(self):
        result, calls = self.run_verify(metadata=self.record(sbom_sha256=""))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("has no sbom_sha256", result.stderr)
        self.assertEqual(calls, [])

    def test_refuses_a_swapped_sbom_even_when_its_recorded_hash_is_updated(self):
        # The helper records the swapped file's real hash, as an attacker editing both files would. Only the signature's
        # predicate can tell the retained SBOM from the signed one.
        result, calls = self.run_verify(metadata=self.record(), sbom={"spdxVersion": "SPDX-2.3", "name": "swapped"})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("is not the verified attestation's predicate", result.stderr)
        self.assertEqual(len(calls), 2) # the swap is refused after cosign verified the image and its attestation

    def test_allows_the_loopback_registry_only_when_asked(self):
        result, calls = self.run_verify(metadata=self.record(), env={"RELEASE_ALLOW_INSECURE_REGISTRY": "true"})
        self.assertEqual(result.returncode, 0, result.stderr)
        for call in calls:
            self.assertIn("--allow-insecure-registry", call)
            self.assertIn("--allow-http-registry", call)

    def test_accepts_an_image_without_a_release_record(self):
        result, calls = self.run_verify()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("verified", result.stdout)
        self.assertEqual(len(calls), 2)


if __name__ == "__main__":
    unittest.main()
