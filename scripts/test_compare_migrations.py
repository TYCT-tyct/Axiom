import unittest

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
