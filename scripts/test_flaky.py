import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import check_flaky
from test_ci_workflow import browser, browser_report, ROOT


class E2EFlaky(unittest.TestCase):
    ENTRY = {"spec": "boards.spec.ts", "title": "flaky", "issue": 830}

    def test_format_keeps_go_entries_and_requires_spec_title_and_issue_for_e2e(self):
        text = '[[test]]\nid = "pkg.TestA"\nissue = 1\n[[e2e]]\nspec = "boards.spec.ts"\ntitle = "flaky"\nissue = 830\n'
        self.assertEqual(check_flaky.e2e_entries(text), [self.ENTRY])
        self.assertEqual(check_flaky.train.parse_flaky(text), [{"id": "pkg.TestA", "issue": 1}])
        for line in ('spec = "boards.spec.ts"\n', 'title = "flaky"\n', 'issue = 830\n'):
            with self.subTest(line=line), self.assertRaises(ValueError):
                check_flaky.e2e_entries(text.replace(line, ""))
        check_flaky.e2e_entries((ROOT / ".github/flaky.toml").read_text())

    def test_closed_issues_and_pull_requests_remind_and_cannot_allow_a_retry(self):
        for issue in ({"state": "open"}, {"state": "closed"}, {"state": "open", "pull_request": {}}):
            with self.subTest(issue=issue), mock.patch.object(check_flaky.train, "sh", return_value=
                    subprocess.CompletedProcess([], 0, stdout=json.dumps(issue))):
                opened, stale = check_flaky.open_e2e("owner/repo", [self.ENTRY])
                self.assertEqual(bool(opened), issue == {"state": "open"})
                self.assertEqual(bool(stale), not bool(opened))
                with mock.patch.object(check_flaky.Path, "read_text", return_value='[[e2e]]\nspec="boards.spec.ts"\ntitle="flaky"\nissue=830'), \
                        mock.patch.object(check_flaky.train, "flaky_check", return_value=0), \
                        mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": "owner/repo"}), \
                        mock.patch.object(check_flaky.sys, "argv", ["check_flaky.py"]), contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(check_flaky.main(), 1 if stale else 0)

    def test_only_a_complete_report_of_listed_failures_can_retry(self):
        report = browser_report(["flaky"], "failed")
        selected = list(browser.tests(report))
        self.assertEqual(browser.quarantined(report, selected, [self.ENTRY]), selected)
        for changed, entries in ((report, []), (browser_report(["flaky", "unknown"], "failed"), [self.ENTRY]),
                                 ({**report, "errors": ["web server failed"]}, [self.ENTRY]),
                                 (browser_report(["flaky"], "skipped"), [self.ENTRY]),
                                 (report, [{**self.ENTRY, "spec": "other.spec.ts"}])):
            self.assertEqual(browser.quarantined(changed, list(browser.tests(changed)), entries), [])
        self.assertEqual(browser.quarantined(report, selected + ["missing"], [self.ENTRY]), [])
        nested = browser_report(["flaky"], "failed")
        nested["suites"][0]["title"] = "nested suite"
        nested["suites"][0]["line"] = 5
        self.assertEqual(browser.quarantined(nested, list(browser.tests(nested)), [self.ENTRY]), [])
        self.assertTrue(browser.quarantined(nested, list(browser.tests(nested)),
                                            [{**self.ENTRY, "title": "nested suite › flaky"}]))

    def test_train_retries_only_failed_tests_once_and_pr_main_only_report(self):
        for retry, retry_status, entries, expected_calls, expected_status in (
            ("true", "passed", [self.ENTRY], 2, 0),
            ("true", "failed", [self.ENTRY], 2, 1),
            ("false", "passed", [self.ENTRY], 1, 1),
            ("true", "passed", [], 1, 1),
        ):
            with self.subTest(retry=retry, retry_status=retry_status, entries=entries), tempfile.TemporaryDirectory() as tmp:
                root, cache = Path(tmp), Path(tmp) / "cache"
                (root / "scripts/ci").mkdir(parents=True)
                (root / "scripts/ci/playwright-durations.json").write_text('{"e2e":{}}')
                (root / "web/e2e").mkdir(parents=True)
                (root / "web/e2e/boards.spec.ts").write_text("")
                full = browser_report(["flaky", "passes"])
                played = []
                def run(command, **kwargs):
                    if "--list" in command:
                        return subprocess.CompletedProcess(command, 0, stdout=json.dumps(full))
                    listing = next(arg.split("=", 1)[1] for arg in command if arg.startswith("--test-list="))
                    played.append(Path(listing).read_text().splitlines())
                    if len(played) == 1:
                        measured = browser_report(["flaky"], "failed")
                        measured["suites"][0]["specs"] += browser_report(["passes"])["suites"][0]["specs"]
                        status = 1
                    else:
                        measured = browser_report(["flaky"], retry_status)
                        status = int(retry_status != "passed")
                        self.assertIn("--retries=0", command)
                    Path(kwargs["env"]["PLAYWRIGHT_JSON_OUTPUT_FILE"]).write_text(json.dumps(measured))
                    return subprocess.CompletedProcess(command, status)
                with mock.patch.object(browser.subprocess, "run", side_effect=run), \
                        mock.patch.object(browser.web_cache, "toolchain", return_value="tools"), \
                        mock.patch.object(browser.web_cache, "e2e_key", return_value="key"), \
                        mock.patch.dict(os.environ, {"E2E_FLAKY": json.dumps(entries), "E2E_FLAKY_RETRY": retry}, clear=True), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(browser.execute(root, cache, "e2e", "1/1", ""), expected_status)
                self.assertEqual(len(played), expected_calls)
                if expected_calls == 2:
                    self.assertEqual(played[1], ["[chromium] › boards.spec.ts › flaky"])
                    self.assertTrue((root / "web/playwright-report/report.json").is_file())
                    self.assertTrue((root / "web/playwright-report/retry.json").is_file())
                self.assertFalse(cache.exists())
