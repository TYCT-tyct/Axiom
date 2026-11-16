import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import compare_migrations
from compare_migrations import normalized_dump


class DumpComparison(unittest.TestCase):
    def test_order_and_restrict_nonce_only(self):
        header = "-- PostgreSQL database dump\n"
        table = "--\n-- Name: a; Type: TABLE; Schema: a; Owner: axiom_owner\n--\nCREATE TABLE a.a (x int, y text);\n"
        grant = "--\n-- Name: TABLE a; Type: ACL; Schema: a; Owner: axiom_owner\n--\nGRANT SELECT ON a.a TO axiom_app;\n"
        original = normalized_dump(header + "\\restrict one\n" + table + grant + "\\unrestrict one\n")
        self.assertEqual(original, normalized_dump(header + "\\restrict two\n" + grant + table + "\\unrestrict two\n"))
        for changed in [table.replace("x int, y text", "y text, x int"),
                        table.replace("x int", "x bigint"),
                        table.replace("axiom_owner", "axiom_app")]:
            self.assertNotEqual(original, normalized_dump(header + changed + grant))
        self.assertNotEqual(original, normalized_dump(header + table + grant.replace("SELECT", "ALL")))

    def test_platform_runs_without_module_job_roles_on_both_sides(self):
        job_ready = False
        platform_databases = set()

        def execute(prefix, db, sql):
            nonlocal job_ready
            if "CREATE ROLE axiom_retention_job;" in sql:
                job_ready = True

        def apply(prefix, db, files):
            modules = {compare_migrations.module(p) for p in files}
            if "platform" in modules:
                self.assertFalse(job_ready, "platform must also work before observability is bootstrapped")
                self.assertEqual(modules, {"platform"})
                platform_databases.add(db)
            else:
                self.assertTrue(job_ready)
                self.assertIn(db, platform_databases)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            history = []
            for module in ("platform/pg", "observability"):
                parent = root / "internal" / module / "migrations"
                parent.mkdir(parents=True)
                (parent / compare_migrations.BASELINE).write_text("SELECT 1;")
                history.append(str(parent.relative_to(root) / "00001_old.sql"))
            with patch.object(compare_migrations, "ROOT", root), \
                    patch.object(compare_migrations, "migration_paths", return_value=history), \
                    patch.object(compare_migrations, "git", return_value="SELECT 1;"), \
                    patch.object(compare_migrations, "sql", side_effect=execute), \
                    patch.object(compare_migrations, "apply", side_effect=apply), \
                    patch.object(compare_migrations, "snapshots", return_value={"schema": "same"}):
                compare_migrations.compare([])
        self.assertEqual(len(platform_databases), 2)
