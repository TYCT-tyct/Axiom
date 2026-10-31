import hashlib
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from test_runner_host import BASH

ROOT = Path(__file__).parents[1]


@unittest.skipUnless(BASH, "Bash is unavailable")
class ReuseRelease(unittest.TestCase):
    def run_deploy(self, image, actual_image=None, verify_status=0, attest_status=0):
        stubs = r'''
python3() { echo fixture; }
just() { [ "$1" != axiom-version ] || echo 'axiom fixture'; }
go() { :; }
timeout() { shift; "$@"; }
cosign() {
  printf 'cosign %s\n' "$*" >> "$CALLS"
  case "$1" in
    verify) return "${COSIGN_VERIFY_STATUS:-0}" ;;
    verify-attestation)
      [ "${COSIGN_ATTEST_STATUS:-0}" = 0 ] || return "$COSIGN_ATTEST_STATUS"
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
}
export -f cosign
docker() {
  printf '%s\n' "$*" >> "$CALLS"
  case "$*" in
    'inspect --format {{.Config.Image}} axiom'|'inspect --format {{.Config.Image}} migrate') echo "${ACTUAL_IMAGE:-$CI_DEPLOY_IMAGE}" ;;
    'compose -f deploy/compose.yaml ps --all --quiet axiom') echo axiom ;;
    'inspect --format {{range .Config.Env}}'*) echo 'TMPDIR=/retention-tmp' ;;
    'inspect --format {{range .Mounts}}'*) echo 'volume true' ;;
    'run --rm --network none --volumes-from axiom'*) echo '65532:65532:700' ;;
    'run --rm --user 65532:65532 --network none --volumes-from axiom'*) : ;;
    'compose -f deploy/compose.yaml ps --all --quiet migrate') echo migrate ;;
    'compose -f deploy/compose.yaml port axiom 8080') echo 127.0.0.1:12345 ;;
    'compose -f deploy/compose.yaml port axiom 8082') echo 127.0.0.1:12346 ;;
  esac
}
curl() {
  case "$*" in
    *'/api/console/v1/version'|*'/api/admin/v1/version') echo '{"version":"axiom fixture"}' ;;
    *'/api/console/v1/site'|*'/api/admin/v1/users') echo '{"error":{"code":"not_found"}}' ;;
    *'-w '*'/assets/'*) echo 404 ;;
    *'-H Accept-Encoding: br'*) printf 'cache-control: public, max-age=31536000, immutable\ncontent-encoding: br\n' ;;
    *'-H Accept: text/html'*) printf 'cache-control: no-store\n' ;;
    *) echo '<script src="/assets/index-fixture.js"></script>' ;;
  esac
}
'''
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "deploy/postgres").mkdir(parents=True)
            (root / "deploy/postgres/roles.sh").touch()
            log = root / "docker.log"
            env = {**os.environ, "CALLS": str(log), "CI_DEPLOY_VERSION": "axiom fixture", "AXIOM_TEST_CGROUP_PARENT": "",
                   "COSIGN_VERIFY_STATUS": str(verify_status), "COSIGN_ATTEST_STATUS": str(attest_status)}
            for name in ("GITHUB_ACTIONS", "GITHUB_REPOSITORY", "GITHUB_REF", "RELEASE_SIGNING_IDENTITY",
                         "CI_DEPLOY_RELEASE"):
                env.pop(name, None)
            if actual_image:
                env["ACTUAL_IMAGE"] = actual_image
            env.pop("CI_DEPLOY_IMAGE", None)
            if image is not None:
                env["CI_DEPLOY_IMAGE"] = image
                sbom = json.dumps({"spdxVersion": "SPDX-2.3"})
                (root / "sbom.spdx.json").write_text(sbom)
                (root / "release.json").write_text(json.dumps({
                    "image_digest": image.rsplit("@", 1)[-1],
                    "signing_identity": "https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main",
                    "sbom_sha256": hashlib.sha256(sbom.encode()).hexdigest(),
                }))
                env["CI_DEPLOY_RELEASE"] = str(root / "release.json")
            # Source to preserve stub functions; the script's exit trap is exercised in the child shell.
            result = subprocess.run([BASH, "-c", stubs + '\nsource "$1"', "test", str(ROOT / "scripts/ci/deploy-check.sh")],
                                    env=env, cwd=tmp, capture_output=True, text=True)
            return result, log.read_text().splitlines()

    def test_restarts_and_legacy_role_upgrade_reuse_one_build_or_the_release_digest(self):
        for image in (None, "127.0.0.1:1234/axiom@sha256:" + "a" * 64):
            with self.subTest(image=image):
                result, calls = self.run_deploy(image)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(sum(call.startswith("build --tag") for call in calls), 1 if image is None else 0)
                starts = [call for call in calls if " up " in call]
                self.assertEqual(len(starts), 6)  # database then services for each of three starts
                self.assertTrue(all("--no-build" in call for call in starts))

    def test_legacy_upgrade_stops_the_node_before_recreating_postgres(self):
        result, calls = self.run_deploy(None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        drop = next(i for i, call in enumerate(calls) if "DROP OWNED BY axiom_migrator" in call)
        stopped = "compose -f deploy/compose.yaml stop axiom"
        self.assertIn(stopped, calls[:drop], "the old node must release its database name lock before the upgrade")
        stop = calls.index(stopped)
        starts = [i for i, call in enumerate(calls) if " up " in call]
        self.assertLess(starts[1], stop)
        self.assertLess(drop, starts[2])

    def test_a_container_running_a_different_reference_is_rejected(self):
        result, _ = self.run_deploy("local/axiom@sha256:" + "a" * 64, actual_image="axiom:stale")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not consume the release digest", result.stdout)

    def test_a_tag_cannot_reach_release_startup(self):
        result, calls = self.run_deploy("axiom:mutable")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires a digest", result.stdout)
        self.assertFalse(any(" up " in call or call.startswith("build ") for call in calls))

    def test_the_signature_is_verified_before_the_release_is_pulled_or_started(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        verify = next(i for i, call in enumerate(calls) if call.startswith("cosign verify "))
        self.assertLess(verify, next(i for i, call in enumerate(calls) if " up " in call))
        self.assertIn("--certificate-identity https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main",
                      calls[verify])
        self.assertIn("--allow-insecure-registry", calls[verify]) # the train's loopback registry
        self.assertTrue(any(call.startswith("cosign verify-attestation ") for call in calls))

    def test_an_unsigned_release_never_reaches_startup(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64, verify_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid keyless signature", result.stderr)
        self.assertFalse(any(" up " in call or call.startswith("build ") for call in calls))

    def test_a_release_without_its_sbom_attestation_never_reaches_startup(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64, attest_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid SPDX attestation", result.stderr)
        self.assertFalse(any(" up " in call or call.startswith("build ") for call in calls))


if __name__ == "__main__":
    unittest.main()
