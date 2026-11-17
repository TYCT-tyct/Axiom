"""The report's numbers, against a recorded week: scripts/ci_report.py is a pure function of the API's JSON, so the
whole report is checked here without the network. The fixture's job times are on the minute grid on purpose, so the
expected percentiles below can be checked by adding up the sample sizes."""

import json
import unittest
from datetime import datetime, timezone
from pathlib import Path

import ci_report

DATA = Path(__file__).parent / "testdata" / "ci_report"
SINCE = datetime(2026, 11, 10, 1, 0, tzinfo=timezone.utc)
UNTIL = datetime(2026, 11, 17, 1, 0, tzinfo=timezone.utc)


def week():
    return json.loads((DATA / "week.json").read_text(encoding="utf-8"))


def failed_tests(*logs):
    """The list the fetch layer hands the report: each failed job's log parsed once, a name once per job."""
    return [name for log in logs
            for name in sorted(ci_report.failure_names((DATA / log).read_text(encoding="utf-8")))]


class RecordedWeek(unittest.TestCase):
    def setUp(self):
        self.week = week()
        self.text = ci_report.report(self.week["runs"], self.week["jobs"],
                                     failed_tests("go-test.txt", "e2e-console.txt", "e2e-boards.txt"),
                                     self.week["ejections"], SINCE, UNTIL)

    def test_run_counts_and_pass_rate_per_workflow_and_event(self):
        rows = ("| ci · PR | 2 | 1 | 1 | 0 | 0 | 50% |",
                "| ci · main 推送 | 1 | 1 | 0 | 0 | 0 | 100% |",
                "| ci · 列车构建 | 3 | 1 | 2 | 0 | 0 | 33% |",
                "| train · 定时 | 1 | 1 | 0 | 0 | 0 | 100% |",
                "| train · 构建完成 | 1 | 0 | 0 | 0 | 1 | — |")
        for row in rows:
            with self.subTest(row=row):
                self.assertIn(row, self.text)

    def test_job_percentiles_and_runner_minutes(self):
        # static ran 6 times for 241..541s: p50 is the 3rd sample (283s) and p90 the 6th (541s); its queue is
        # 1..398s, so p50 is 2s and p90 398s
        rows = ("| go-test | 5 | 10.2 | 12.2 | 28 | 419 | 44.6 |",
                "| static | 6 | 4.7 | 9.0 | 2 | 398 | 34.1 |",
                "| boards | 1 | 22.7 | 22.7 | 522 | 522 | 22.7 |",
                "| build | 6 | 0.1 | 0.2 | 2 | 9 | 0.7 |",
                "| changes | 1 | 0.2 | 0.2 | 58 | 58 | 0.2 |")
        for row in rows:
            with self.subTest(row=row):
                self.assertIn(row, self.text)
        self.assertLess(self.text.index("| go-test |"), self.text.index("| static |"))  # costliest first
        self.assertNotIn("| signing |", self.text)  # a skipped job ran nothing to time

    def test_failed_train_builds_by_failing_job(self):
        self.assertIn("本周列车构建失败 2 次；从队列退车 2 次（`train:failed` / `train:conflict`；重新排队后会再算一次）。", self.text)
        self.assertIn("| deploy | 1 |\n| go-test | 1 |\n| static | 1 |", self.text)
        self.assertNotIn("| build | 1 |", self.text)  # the aggregator fails with the part, it is no cause

    def test_the_tests_that_failed_most(self):
        for name in ("e2e/boards.spec.ts › AxApp regions 1440 dark: artboard diff and CSP interactions",
                     "e2e/boards.spec.ts › AxApp regions 1440 light: artboard diff and CSP interactions",
                     "e2e/console.spec.ts › the console offers a reload after a deployment, in either language",
                     "internal/observability.TestUsageBackfillDrainsLiveJournal",
                     "internal/observability.TestUsageBackfillResumesAcrossPartitions"):
            with self.subTest(name=name):
                self.assertEqual(self.text.count(f"| {name} | 1 |"), 1)  # once, though the log repeats it


class QuietWeek(unittest.TestCase):
    def test_a_week_without_train_failures_says_none_instead_of_an_empty_table(self):
        recorded = week()
        runs = [run for run in recorded["runs"] if ci_report.group(run) != ("ci", "列车构建")]
        build_ids = {run["id"] for run in runs}
        text = ci_report.report(runs, [job for job in recorded["jobs"] if job["run_id"] in build_ids], [], 0,
                                SINCE, UNTIL)
        self.assertIn("本周没有列车构建失败；从队列退车 0 次", text)
        self.assertNotIn("| 失败作业 |", text)
        self.assertNotIn("| 测试 |", text)


class Percentile(unittest.TestCase):
    def test_nearest_rank(self):
        cases = (([], None, None), ([5], 5, 5), ([10, 20, 30, 40], 20, 40), ([1, 2, 3], 2, 3),
                 (list(range(1, 11)), 5, 9))
        for samples, p50, p90 in cases:
            with self.subTest(samples=samples):
                self.assertEqual(ci_report.percentile(samples, 50), p50)
                self.assertEqual(ci_report.percentile(samples, 90), p90)


class FailureNames(unittest.TestCase):
    def test_go_names_carry_the_package_without_the_module_path(self):
        self.assertEqual(failed_tests("go-test.txt"),
                         ["internal/observability.TestUsageBackfillDrainsLiveJournal",
                          "internal/observability.TestUsageBackfillResumesAcrossPartitions"])

    def test_playwright_names_carry_the_spec_file_and_a_test_counts_once_per_job(self):
        self.assertEqual(failed_tests("e2e-boards.txt"),
                         ["e2e/boards.spec.ts › AxApp regions 1440 dark: artboard diff and CSP interactions",
                          "e2e/boards.spec.ts › AxApp regions 1440 light: artboard diff and CSP interactions"])

    def test_a_log_without_failures_names_nothing(self):
        self.assertEqual(ci_report.failure_names("2026-11-17T04:31:19.9714395Z ·······F\n"), set())

    def test_top_failures_orders_by_count_then_name_and_stops_at_ten(self):
        top = ci_report.top_failures(["b"] * 3 + ["a"] * 3 + ["c"] * 2 + [f"z{index}" for index in range(9)])
        self.assertEqual(len(top), 10)
        self.assertEqual(top[:4], [("a", 3), ("b", 3), ("c", 2), ("z0", 1)])


class Grouping(unittest.TestCase):
    def test_the_row_a_run_belongs_on(self):
        cases = ((("ci", "pull_request", "feat/x"), "ci · PR"),
                 (("ci", "push", "main"), "ci · main 推送"),
                 (("ci", "workflow_dispatch", "train/main/1-1"), "ci · 列车构建"),
                 (("ci", "workflow_dispatch", "main"), "ci · main 派发"),
                 (("train", "schedule", "main"), "train · 定时"),
                 (("train", "workflow_run", "main"), "train · 构建完成"))
        for (workflow, event, branch), row in cases:
            with self.subTest(row=row):
                run = {"workflow": workflow, "event": event, "head_branch": branch}
                self.assertEqual(" · ".join(ci_report.group(run)), row)

    def test_a_run_still_running_is_left_to_the_next_report(self):
        self.assertIsNone(ci_report.bucket({"conclusion": None}))
        self.assertEqual(ci_report.run_rows([{"workflow": "ci", "event": "push", "conclusion": None}]), [])


class IssueBody(unittest.TestCase):
    def test_the_section_replaces_last_week_and_leaves_the_prose_above_it(self):
        body = "CI 的健康度每周自动汇总在这里。\n\n用途：提前发现。"
        first = ci_report.replace_section(body, "## 第一周")
        self.assertEqual(first, body + "\n\n<!-- ci-report -->\n\n## 第一周")
        self.assertEqual(ci_report.replace_section(first, "## 第二周"), body + "\n\n<!-- ci-report -->\n\n## 第二周")

    def test_an_empty_body_is_only_the_section(self):
        self.assertEqual(ci_report.replace_section("", "## 周"), "<!-- ci-report -->\n\n## 周")
