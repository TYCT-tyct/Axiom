import contextlib
import io
import os
import sys
import tempfile
import threading
import unittest
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
        }, clear=False):
            inputs = dict(web_cache.env_inputs())
        self.assertEqual(inputs["VITE_PROOF"], "vite")
        self.assertEqual(inputs["NODE_OPTIONS"], "--proof")
        self.assertEqual(inputs["AXIOM_SHOTS_DIR"], "shots")
        for name in ("AXIOM_CACHE_DIR", "AXIOM_WEB_PORT", "AXIOM_VERSION"):
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

    def test_failure_is_not_cached(self):
        status, _ = self.e2e(exits=1)
        self.assertEqual(status, 1)
        status, out = self.e2e()
        self.assertEqual((status, "cache miss" in out), (0, True))


if __name__ == "__main__":
    unittest.main()
