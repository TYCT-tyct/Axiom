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
    def run_deploy(self, image, actual_image=None, verify_status=0, attest_status=0, up_replaces_database=False):
        stubs = r'''
python3() { echo fixture; }
just() { [ "$1" != axiom-version ] || echo 'axiom fixture'; }
go() { :; }
bash() {
  case "$*" in
    *old-binary-check.sh*) printf 'bash %s\n' "$*" >> "$CALLS" ;;
    scripts/ops/up.sh*) up "$@" ;;
    *) command bash "$@" ;;
  esac
}
# scripts/ops/up.sh as test_deploy_up.py pins it, with the containers as numbered files: a setting no service reads is
# refused; a running node gets a new container and keeps its database unless the database's environment changed, which
# only --database applies; with --database or no node, both are replaced.
up() {
  printf 'bash %s\n' "$*" >> "$CALLS"
  if grep -q AXIOM_HEALTH_MOOD deploy/.env; then echo 'up: deploy/.env sets AXIOM_HEALTH_MOOD' >&2; return 1; fi
  local env
  env="$(grep '^AXIOM_MIGRATOR_PASSWORD=' deploy/.env)"
  if [ -f node ] && [ "${!#}" != --database ] && [ -z "${UP_REPLACES_DATABASE-}" ]; then
    [ "$env" = "$(cat db.env)" ] || { echo 'up: upgrade it with --database' >&2; return 1; }
  else
    echo "$env" > db.env
    echo $(( $(cat db 2> /dev/null || echo 0) + 1 )) > db
  fi
  echo $(( $(cat node 2> /dev/null || echo 0) + 1 )) > node
}
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
    'compose -f deploy/compose.yaml ps --quiet postgres') cat db ;;
    'compose -f deploy/compose.yaml ps --quiet axiom') cat node ;;
    'ps -aq --filter label=com.docker.compose.project=axiom-ci-fixture') echo axiom ;;
    'inspect --format {{range .Config.Env}}'*) echo 'TMPDIR=/retention-tmp' ;;
    'inspect --format {{range .Mounts}}'*) echo 'volume true' ;;
    'run --rm --network none --volumes-from axiom'*) echo '65532:65532:700' ;;
    'run --rm --user 65532:65532 --network none --volumes-from axiom'*) : ;;
    'compose -f deploy/compose.yaml ps --all --quiet migrate') echo migrate ;;
    'compose -f deploy/compose.yaml port axiom 8080') echo 127.0.0.1:12345 ;;
    'compose -f deploy/compose.yaml port axiom 8082') echo 127.0.0.1:12346 ;;
    # The key ring's refusals: the keys service exits 1, so the node's start fails, and its last line is the reason.
    'compose -f deploy/compose.yaml up --detach --no-build axiom') return 1 ;;
    'compose -f deploy/compose.yaml logs --no-log-prefix --tail 1 keys')
      if [ -n "${AXIOM_KEYRING_GENERATE+set}" ]; then echo "keys: AXIOM_KEYRING_GENERATE must be true or false, not '$AXIOM_KEYRING_GENERATE'"
      else echo 'keys: /keys has master identity but not hash; restore the whole ring'; fi ;;
    *'--entrypoint sh keys -c ls /keys') printf 'identity\nmaster\nvalkey-password\n' ;;
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
            env.pop("UP_REPLACES_DATABASE", None)
            if up_replaces_database:
                env["UP_REPLACES_DATABASE"] = "1"
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

    def test_every_start_goes_through_the_production_start_on_one_build_or_the_release_digest(self):
        for image in (None, "127.0.0.1:1234/axiom@sha256:" + "a" * 64):
            with self.subTest(image=image):
                result, calls = self.run_deploy(image)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertEqual(calls.count("build --tag axiom-ci:fixture deploy"), 1 if image is None else 0)
                # The database image is up.sh's to build, only when no node runs (#605).
                self.assertFalse([call for call in calls if call.startswith("build ") and "postgres" in call], calls)
                # Outside up.sh only the key ring's three starts, each refused by the keys service before the node runs.
                self.assertEqual([call for call in calls if call.startswith("compose ") and " up " in call],
                                 ["compose -f deploy/compose.yaml up --detach --no-build axiom"] * 3)
                starts = [call.removeprefix("bash scripts/ops/up.sh --env-file deploy/.env").strip()
                          for call in calls if call.startswith("bash scripts/ops/up.sh")]
                # The first start, an application release, a misspelled setting, the legacy upgrade refused under the
                # running node and then applied with --database, and the start that repeats the grants.
                self.assertEqual(starts, ["", "", "", "", "--database", ""])
                self.assertEqual(calls[-1], "image rm --force axiom-ci-postgres:fixture")

    def test_a_failed_check_prints_the_containers_output_before_removing_them(self):
        result, calls = self.run_deploy("local/axiom@sha256:" + "a" * 64, actual_image="axiom:stale")
        self.assertNotEqual(result.returncode, 0)
        logs = calls.index("logs --tail 200 axiom")
        down = "compose --project-name axiom-ci-fixture down --volumes --timeout 5"
        self.assertLess(logs, len(calls) - 1 - calls[::-1].index(down))
        self.assertNotIn("logs --tail 200 axiom", self.run_deploy(None)[1])

    def test_the_previous_release_starts_before_the_legacy_upgrade(self):
        result, calls = self.run_deploy(None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        check = next(i for i, call in enumerate(calls) if "old-binary-check.sh" in call)
        self.assertIn("--current-image axiom-ci:", calls[check])
        drop = next(i for i, call in enumerate(calls) if "DROP OWNED BY axiom_migrator" in call)
        self.assertLess(check, drop, "the normal deployment checks run before the separate legacy-role scenario")
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        check = next(i for i, call in enumerate(calls) if "old-binary-check.sh" in call)
        self.assertIn("--current-image 127.0.0.1:1234/axiom@sha256:", calls[check])

    def test_the_legacy_upgrade_is_refused_under_the_running_node_until_it_drains_it(self):
        result, calls = self.run_deploy(None)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        drop = next(i for i, call in enumerate(calls) if "DROP OWNED BY axiom_migrator" in call)
        after = [call for call in calls[drop:] if call.startswith("bash scripts/ops/up.sh")]
        self.assertEqual(after[:2], ["bash scripts/ops/up.sh --env-file deploy/.env",
                                     "bash scripts/ops/up.sh --env-file deploy/.env --database"])

    def test_a_release_that_replaces_the_database_under_the_node_fails_the_check(self):
        # The stub's release path behaves like the database path: a start that replaces the database every time.
        result, _ = self.run_deploy(None, up_replaces_database=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("replaced the database under a running node", result.stdout)

    def test_a_container_running_a_different_reference_is_rejected(self):
        result, _ = self.run_deploy("local/axiom@sha256:" + "a" * 64, actual_image="axiom:stale")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not consume the release digest", result.stdout)

    def test_a_tag_cannot_reach_release_startup(self):
        result, calls = self.run_deploy("axiom:mutable")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("requires a digest", result.stdout)
        self.assertFalse(any("scripts/ops/up.sh" in call or call.startswith("build ") for call in calls))

    def test_the_signature_is_verified_before_the_release_is_pulled_or_started(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        verify = next(i for i, call in enumerate(calls) if call.startswith("cosign verify "))
        self.assertLess(verify, next(i for i, call in enumerate(calls) if "scripts/ops/up.sh" in call))
        self.assertIn("--certificate-identity https://github.com/TYCT-tyct/Axiom/.github/workflows/ci.yml@refs/heads/main",
                      calls[verify])
        self.assertIn("--allow-insecure-registry", calls[verify]) # the train's loopback registry
        self.assertTrue(any(call.startswith("cosign verify-attestation ") for call in calls))

    def test_an_unsigned_release_never_reaches_startup(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64, verify_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid keyless signature", result.stderr)
        self.assertFalse(any("scripts/ops/up.sh" in call or call.startswith("build ") for call in calls))

    def test_a_release_without_its_sbom_attestation_never_reaches_startup(self):
        result, calls = self.run_deploy("127.0.0.1:1234/axiom@sha256:" + "a" * 64, attest_status=1)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no valid SPDX attestation", result.stderr)
        self.assertFalse(any("scripts/ops/up.sh" in call or call.startswith("build ") for call in calls))


if __name__ == "__main__":
    unittest.main()
