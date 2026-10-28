import unittest

from check_doc_names import declarations, problems

SOURCES = {
    "internal/billing/hold.go": "package billing\n\nfunc Settle() {}\n\nfunc (h *Hold) Release() {}\n\ntype Hold struct{}\n\nconst (\n\tMaxHolds = 3\n)\n",
    "internal/billing/hold_test.go": "package billing_test\n\nfunc TestSettleOnce(t *testing.T) {}\n\nfunc BenchmarkSettle(b *testing.B) {}\n",
}


class Problems(unittest.TestCase):
    def test_cases(self):
        declared = declarations(SOURCES)
        cases = [
            ("test that exists", "see `TestSettleOnce`", 0),
            ("subtest of a test that exists", "`TestSettleOnce/zero`", 0),
            ("renamed test", "`TestSettleTwice` covers it", 1),
            ("prefix glob that matches", "`TestSettle*`", 0),
            ("prefix glob that matches nothing", "`TestRefund*`", 1),
            ("benchmark", "`BenchmarkSettle` and `BenchmarkRefund`", 1),
            ("declared function", "`billing.Settle`", 0),
            ("declared function with arguments", "`billing.Settle(ctx, id)`", 0),
            ("method and block constant", "`billing.Release` `billing.MaxHolds`", 0),
            ("missing function", "`billing.Refund`", 1),
            ("lowercase SQL object is left alone", "`billing.expiry_cursor`", 0),
            ("other package's names are left alone", "`http.Handler` `errors.AsType`", 0),
            ("placeholder", "`包路径.TestName`", 0),
            ("outside code formatting", "TestNothing billing.Refund", 0),
        ]
        for name, text, want in cases:
            with self.subTest(name):
                self.assertEqual(len(problems({"docs/x.md": text}, declared)), want)

    def test_reports_file_and_line(self):
        self.assertEqual(problems({"docs/x.md": "a\n`TestGone`"}, declarations(SOURCES)),
                         ["docs/x.md:2: `TestGone` names test TestGone, which no Go file declares"])


if __name__ == "__main__":
    unittest.main()
