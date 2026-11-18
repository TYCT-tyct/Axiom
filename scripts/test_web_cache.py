import contextlib
import io
import os
import sys
import tempfile
import threading
import unittest
import shutil
from pathlib import Path
from unittest import mock

import web_cache

FAKE_BUILD = """
import pathlib, sys
root = pathlib.Path(sys.argv[1])
with (root / "built.log").open("a", encoding="utf-8") as log:
    log.write("build\\n")
for app in ("site", "console", "admin"):
    dist = root / "web" / app / "dist"
    dist.mkdir(parents=True, exist_ok=True)
    (dist / "index.html").write_text(app + " " + str(len((root / "web" / "site" / "src" / "app.ts").read_text())), encoding="utf-8")
"""

FAKE_E2E = """
import pathlib, sys
root = pathlib.Path(sys.argv[1])
with (root / "ran.log").open("a", encoding="utf-8") as log:
    log.write("e2e\\n")
"""


class Repository:
    def __init__(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.write("web/site/src/app.ts", "export const site = 1\n")
        self.write("web/console/src/app.ts", "export const consoleApp = 1\n")
        self.write("web/admin/src/app.ts", "export const admin = 1\n")
        self.write("web/site/src/app.test.ts", "// a test input\n")
        self.write("web/pnpm-lock.yaml", "lockfileVersion: 9\n")
        self.write("web/package.json", "{}\n")
        self.write("web/e2e/apps.spec.ts", "// e2e\n")
        self.write("api/console.yaml", "openapi: 3.0.3\n")
        self.write("go.mod", "module axiom\n")
        self.write("go.sum", "")
        self.write("main.go", "package main\n")
        self.write("internal/x/x_test.go", "package x\n")
        self.write("web/site/dist/index.html", "old output\n")
        self.write("web/node_modules/p/index.js", "dependency\n")

    def write(self, path: str, text: str) -> None:
        file = self.root / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(text, encoding="utf-8")

    def close(self) -> None:
        self.temp.cleanup()


class EnvInputs(unittest.TestCase):
    def test_both_keys_share_one_prefix_rule(self):
        with mock.patch.dict(os.environ, {
            "VITE_PROOF": "vite",
            "NODE_OPTIONS": "--proof",
            "AXIOM_SHOTS_DIR": "shots",
            "AXIOM_CACHE_DIR": "cache",
            "AXIOM_WEB_PORT": "1",
            "AXIOM_VERSION": "v",
            "AXIOM_TEST_RESULTS": "/ci/axiom/reports/1/e2e/test-results",
            "AXIOM_PLAYWRIGHT_REPORTS": "/ci/axiom/reports/1/e2e/playwright-report",
        }, clear=False):
            inputs = dict(web_cache.env_inputs())
        self.assertEqual(inputs["VITE_PROOF"], "vite")
        self.assertEqual(inputs["NODE_OPTIONS"], "--proof")
        self.assertEqual(inputs["AXIOM_SHOTS_DIR"], "shots")
        for name in ("AXIOM_CACHE_DIR", "AXIOM_WEB_PORT", "AXIOM_VERSION", "AXIOM_TEST_RESULTS", "AXIOM_PLAYWRIGHT_REPORTS"):
            self.assertNotIn(name, inputs)


class Hashes(unittest.TestCase):
    def setUp(self):
        self.repo = Repository()
        self.other = Repository()

    def tearDown(self):
        self.repo.close()
        self.other.close()


    def test_web_hash_is_content_not_path(self):
        self.assertEqual(web_cache.web_hash(self.repo.root), web_cache.web_hash(self.other.root))
        self.repo.write("web/site/dist/index.html", "new output\n")
        self.repo.write("web/node_modules/p/index.js", "new dependency\n")
        self.assertEqual(web_cache.web_hash(self.repo.root), web_cache.web_hash(self.other.root))
        self.repo.write("web/site/src/app.ts", "export const site = 2\n")
        self.assertNotEqual(web_cache.web_hash(self.repo.root), web_cache.web_hash(self.other.root))

    def test_e2e_inputs_cover_web_api_and_all_go(self):
        before = web_cache.e2e_inputs_hash(self.repo.root)
        self.repo.write("api/console.yaml", "openapi: 3.0.4\n")
        after_api = web_cache.e2e_inputs_hash(self.repo.root)
        self.assertNotEqual(before, after_api)
        self.repo.write("internal/x/x_test.go", "package x // changed\n")
        after_test = web_cache.e2e_inputs_hash(self.repo.root)
        self.assertNotEqual(after_api, after_test)
        self.repo.write("web/site/src/app.test.ts", "// changed\n")
        self.assertNotEqual(after_test, web_cache.e2e_inputs_hash(self.repo.root))


@unittest.skipUnless(shutil.which("node"), "Actions cache client runs in Node")
class ActionsCache(unittest.TestCase):
    def test_remote_build_and_pass_restore_by_the_existing_content_keys(self):
        repo = Repository()
        self.addCleanup(repo.close)
        make_dist(repo.root)
        cache, cloud = repo.root / "cache", repo.root / "cloud"
        cloud.mkdir()
        client = repo.root / "client.cjs"
        client.write_text('''
const fs = require('node:fs'), path = require('node:path');
if (process.env.GITHUB_WORKSPACE !== process.cwd()) throw Error('archive base differs from cache root');
exports.saveCache = async (paths, key) => {
  if (paths.length !== 1 || paths[0] !== key) throw Error('wrong content key');
  fs.cpSync(key, path.join(process.env.TEST_CLOUD, key), {recursive: true});
};
exports.restoreCache = async (paths, key, fallback) => {
  if (paths.length !== 1 || paths[0] !== key || fallback?.length) throw Error('wrong content key');
  const source = path.join(process.env.TEST_CLOUD, key);
  if (fs.existsSync(source)) fs.cpSync(source, key, {recursive: true});
};
''')
        with mock.patch.dict(os.environ, ACTIONS_CACHE_CLIENT=str(client), TEST_CLOUD=str(cloud)), \
                mock.patch.object(web_cache, "run", return_value=0) as run, contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(web_cache.build(repo.root, cache, "v1", ["build"], "tools"), 0)
            build_key = web_cache.build_key(repo.root, "v1", "tools")
            self.assertTrue(web_cache.complete(cloud / build_key))
            shutil.rmtree(cache)
            shutil.rmtree(repo.root / "web/site/dist")
            self.assertEqual(web_cache.build(repo.root, cache, "v1", ["build"], "tools"), 0)
            self.assertTrue((repo.root / "web/site/dist/index.html").is_file())
            self.assertEqual(run.call_count, 1)
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test", "1/2"], "tools"), 0)
            key = web_cache.e2e_key(repo.root, "tools", ["test", "1/2"])
            self.assertTrue((cloud / key).is_file())
            shutil.rmtree(cache)
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test", "1/2"], "tools"), 0)
            self.assertEqual(run.call_count, 2)
            run.return_value = 17
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test", "2/2"], "tools"), 17)
            self.assertFalse((cloud / web_cache.e2e_key(repo.root, "tools", ["test", "2/2"])).exists())
            repo.write("main.go", "changed")
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test", "1/2"], "tools"), 17)

    def test_transport_failure_runs_the_check_and_never_publishes_a_failed_pass(self):
        repo = Repository()
        self.addCleanup(repo.close)
        client = repo.root / "client.cjs"
        client.write_text("""
exports.restoreCache = async (paths, key) => { require('node:fs').writeFileSync(key, 'partial'); throw Error('offline'); };
exports.saveCache = async () => { throw Error('offline'); };
""")
        with mock.patch.dict(os.environ, ACTIONS_CACHE_CLIENT=str(client)), \
                mock.patch.object(web_cache, "run", side_effect=[17, 0]) as run, contextlib.redirect_stdout(io.StringIO()):
            cache = repo.root / "cache"
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test"], "tools"), 17)
            self.assertFalse((cache / "e2e" / web_cache.e2e_key(repo.root, "tools", ["test"])).exists())
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test"], "tools"), 0)
            self.assertEqual(web_cache.e2e(repo.root, cache, "v1", ["test"], "tools"), 0)
            self.assertEqual(run.call_count, 2)



def make_dist(root: Path) -> None:
    for app in web_cache.APPS:
        dist = root / "web" / app / "dist"
        dist.mkdir(parents=True, exist_ok=True)
        (dist / "index.html").write_text(app + " proof\n", encoding="utf-8")


class BuildCache(unittest.TestCase):
    def setUp(self):
        self.repo = Repository()
        self.cache = Path(tempfile.mkdtemp())

    def tearDown(self):
        self.repo.close()

    def build(self):
        with contextlib.redirect_stdout(io.StringIO()) as out:
            status = web_cache.build(
                self.repo.root, self.cache, "axiom test", [sys.executable, "-c", FAKE_BUILD, str(self.repo.root)],
                "node 1 pnpm 1",
            )
        self.assertEqual(status, 0)
        return out.getvalue()

    def test_hit_restores_without_building(self):
        self.assertIn("cache miss", self.build())
        self.assertEqual((self.repo.root / "built.log").read_text(encoding="utf-8").count("build"), 1)
        self.assertIn("cache hit", self.build())
        self.assertEqual((self.repo.root / "built.log").read_text(encoding="utf-8").count("build"), 1)
        self.assertIn("site", (self.repo.root / "web" / "site" / "dist" / "index.html").read_text(encoding="utf-8"))

    def test_one_input_byte_misses(self):
        self.build()
        self.repo.write("web/site/src/app.ts", "export const site = 3\n")
        self.assertIn("cache miss", self.build())
        self.assertEqual((self.repo.root / "built.log").read_text(encoding="utf-8").count("build"), 2)

    def test_two_writers_of_one_key_do_not_fail(self):
        roots = [Repository(), Repository()]
        for root in roots:
            make_dist(root.root)
        target = self.cache / "web-build" / "same"
        barrier = threading.Barrier(2)
        original = os.replace
        results = []

        def replace(src, dst):
            barrier.wait()
            return original(src, dst)

        def save(root):
            results.append(web_cache.save(root, target))

        threads = [threading.Thread(target=save, args=(root.root,)) for root in roots]
        with mock.patch.object(web_cache.os, "replace", side_effect=replace):
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
        for root in roots:
            root.close()
        self.assertEqual(sorted(results), [False, True])
        self.assertTrue(web_cache.complete(target))

    def test_an_unexpected_replace_error_is_not_swallowed(self):
        make_dist(self.repo.root)
        with mock.patch.object(web_cache.os, "replace", side_effect=OSError("boom")):
            with self.assertRaises(OSError):
                web_cache.save(self.repo.root, self.cache / "web-build" / "same")


class E2ECache(unittest.TestCase):
    def setUp(self):
        self.repo = Repository()
        self.cache = Path(tempfile.mkdtemp())

    def tearDown(self):
        self.repo.close()

    def e2e(self, exits: int = 0):
        command = [sys.executable, "-c", FAKE_E2E, str(self.repo.root)] if not exits else \
            [sys.executable, "-c", "import sys; sys.exit(1)"]
        with contextlib.redirect_stdout(io.StringIO()) as out:
            status = web_cache.e2e(self.repo.root, self.cache, "axiom test", command, "node 1 pnpm 1")
        return status, out.getvalue()

    def test_hit_skips_and_go_change_misses(self):
        status, out = self.e2e()
        self.assertEqual((status, "cache miss" in out), (0, True))
        self.assertEqual((self.repo.root / "ran.log").read_text(encoding="utf-8").count("e2e"), 1)
        status, out = self.e2e()
        self.assertEqual((status, "cache hit" in out), (0, True))
        self.assertEqual((self.repo.root / "ran.log").read_text(encoding="utf-8").count("e2e"), 1)
        self.repo.write("internal/x/x_test.go", "package x // one byte\n")
        status, out = self.e2e()
        self.assertEqual((status, "cache miss" in out), (0, True))
        self.assertEqual((self.repo.root / "ran.log").read_text(encoding="utf-8").count("e2e"), 2)

    def test_a_partial_pass_does_not_stand_for_the_full_suite(self):
        script = ["-c", FAKE_E2E, str(self.repo.root)]
        with contextlib.redirect_stdout(io.StringIO()) as out:
            for extra_args in (["admin-access"], []):
                web_cache.e2e(self.repo.root, self.cache, "axiom test", [sys.executable, *script, *extra_args], "node 1 pnpm 1")
        self.assertEqual(out.getvalue().count("cache miss"), 2)
        self.assertEqual((self.repo.root / "ran.log").read_text(encoding="utf-8").count("e2e"), 2)

    def test_shard_index_and_count_have_independent_passes(self):
        command = [sys.executable, "-c", FAKE_E2E, str(self.repo.root)]
        for shard in ("1/2", "2/2", "1/3", "1/1"):
            with self.subTest(shard=shard), contextlib.redirect_stdout(io.StringIO()) as out:
                args = [*command, "--shard=" + shard]
                self.assertEqual(web_cache.e2e(self.repo.root, self.cache, "v1", args, "tools"), 0)
                self.assertIn("cache miss", out.getvalue())
                out.truncate(0)
                out.seek(0)
                self.assertEqual(web_cache.e2e(self.repo.root, self.cache, "v2", args, "tools"), 0)
                self.assertIn("cache hit", out.getvalue())
        self.assertEqual((self.repo.root / "ran.log").read_text(encoding="utf-8").count("e2e"), 4)

    def test_failure_is_not_cached(self):
        status, _ = self.e2e(exits=1)
        self.assertEqual(status, 1)
        status, out = self.e2e()
        self.assertEqual((status, "cache miss" in out), (0, True))


class BoardAndVitalsCache(unittest.TestCase):
    def setUp(self):
        self.repo = Repository()
        self.addCleanup(self.repo.close)
        self.cache = self.repo.root / "cache"
        for name in ("justfile", "scripts/ci/vitals-base.sh", "scripts/ci/web-container.sh",
                     ".github/workflows/ci.yml", "scripts/ci/playwright.py", "scripts/ci/boards-inputs.mjs",
                     "web/e2e/collect-errors.ts", "web/e2e/apps.ts", "web/scripts/free-port.ts",
                     "web/scripts/fonts/sources.json", "web/packages/ui/src/css/fonts/fonts.json",
                     "web/scripts/boards.ts", "web/e2e/boards.spec.ts", "web/e2e/design/compare.ts",
                     "web/packages/boards/regions.ts", "web/packages/boards/src/demo.ts",
                     "web/packages/boards/src/AxApp/Board.tsx", "web/packages/boards/src/Site/Board.tsx",
                     "web/packages/boards/inputs/AxApp.head.html", "web/packages/boards/inputs/Site.dc.html"):
            self.repo.write(name, "input\n")

    def key(self, board="AxApp", semantic=None, selected=None):
        return web_cache.boards_key(self.repo.root, board, ["AxApp", "Site"], semantic or {},
                                    selected or [board + " light", board + " dark"], "tools")

    def test_console_and_other_boards_do_not_invalidate_a_board(self):
        before = self.key()
        for name in ("web/console/src/app.ts", "web/packages/boards/src/Site/Board.tsx",
                     "web/packages/boards/inputs/Site.dc.html", "web/packages/boards/regions.ts",
                     "web/packages/boards/src/demo.ts"):
            self.repo.write(name, "changed\n")
            self.assertEqual(self.key(), before, name)
        self.assertNotEqual(before, self.key(semantic={"regions": [{"board": "AxApp", "name": "Shell"}]}))
        self.assertNotEqual(before, self.key(selected=["AxApp light"]))

    def test_board_shared_inputs_and_pins_invalidate(self):
        for name in ("web/packages/boards/src/AxApp/Board.tsx", "web/packages/boards/inputs/AxApp.head.html",
                     "web/packages/boards/src/runtime.tsx", "web/packages/boards/src/engines/clawd.js",
                     "web/scripts/boards.ts", "web/e2e/boards.spec.ts", "web/e2e/design/compare.ts",
                     "web/playwright.boards.config.ts", "web/playwright.config.ts", "web/pnpm-lock.yaml",
                     "web/packages/ui/src/css/fonts/fonts.json", "scripts/ci/playwright.py"):
            with self.subTest(path=name):
                before = self.key()
                self.repo.write(name, "changed\n")
                self.assertNotEqual(before, self.key())

    def test_input_read_failure_is_not_an_empty_hash(self):
        with mock.patch.object(web_cache, "add_file", side_effect=OSError("unreadable")):
            with self.assertRaises(OSError):
                self.key()

    def test_board_script_directories_are_hashed_recursively(self):
        self.repo.write("web/scripts/boards/regions.ts", "converter\n")
        before = self.key()
        self.repo.write("web/scripts/boards/regions.ts", "changed converter\n")
        self.assertNotEqual(before, self.key())

    def test_concurrent_writers_can_publish_the_same_pass(self):
        marker = self.cache / "boards/AxApp/pass"
        barrier = threading.Barrier(2)
        replace = os.replace
        errors = []
        def publish():
            try:
                web_cache.record_pass(marker)
            except OSError as error:
                errors.append(error)
        def together(source, target):
            barrier.wait()
            replace(source, target)
        with mock.patch.object(web_cache.os, "replace", side_effect=together):
            threads = [threading.Thread(target=publish) for _ in range(2)]
            for thread in threads:
                thread.start()
            for thread in threads:
                thread.join()
        self.assertEqual(errors, [])
        self.assertEqual(marker.read_text(), web_cache.CACHE_FORMAT + "\n")
        self.assertEqual(list(marker.parent.iterdir()), [marker])

    def test_vitals_reuses_only_same_build_baseline_and_success(self):
        def measure(base="base", version="v1"):
            with mock.patch.dict(os.environ, {"BASE": base}), contextlib.redirect_stdout(io.StringIO()):
                return web_cache.vitals(self.repo.root, self.cache, version, ["measure"], "tools")
        with mock.patch.object(web_cache, "run", return_value=1) as run:
            self.assertEqual(measure(), 1)
            run.return_value = 0
            self.assertEqual(measure(), 0)
            self.assertEqual(measure(), 0)
            self.assertEqual(run.call_count, 2)
            with mock.patch.dict(os.environ, {"AXIOM_TEST_RESULTS": "/another-run/test-results"}):
                self.assertEqual(measure(), 0)
                self.assertEqual(run.call_count, 2)
            measure(base="other")
            measure(base="")
            measure(version="v2")
            self.repo.write("web/console/src/app.ts", "changed\n")
            measure()
            self.assertEqual(run.call_count, 6)


if __name__ == "__main__":
    unittest.main()
