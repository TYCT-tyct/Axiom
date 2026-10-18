import unittest
from pathlib import Path

import train
from train import Car, Entry

SHA = "a" * 40


class FakeTrain(train.Train):
    """The real train loop over a fake repository: a commit names the PRs merged into it, and a build fails when every
    PR of some breaking combination is in it."""

    def __init__(self, breaking, lane):
        self.base, self.dry_run, self.lane, self.breaking = "main", True, lane, breaking
        self.landed, self.ejected, self.builds = [], [], 0

    def assemble(self, base, entries):
        merged = [int(n) for n in base.split(",") if n]
        return [Car(e, ",".join(map(str, merged + [x.number for x in entries[:i + 1]]))) for i, e in enumerate(entries)]

    def build_all(self, base, tips):
        self.builds += len(tips)
        return {key: (not any(combo <= {int(n) for n in commit.split(",")} for combo in self.breaking), f"run {commit}")
                for key, commit in tips.items()}

    def land(self, base, cars):
        self.landed += [car.entry.number for car in cars]
        return cars[-1].commit

    def eject(self, entry, label, reason):
        self.ejected.append((entry.number, label))

    def status(self, *args):
        pass


class Bisection(unittest.TestCase):
    def run_train(self, n, breaking, lane):
        fake = FakeTrain(breaking, lane)
        fake.ride("", [Entry(number, SHA) for number in range(1, n + 1)])
        return fake

    def test_a_single_culprit_is_found_and_everything_before_it_lands(self):
        for lane in (1, 2, 3):
            for n in range(1, 10):
                for culprit in range(1, n + 1):
                    with self.subTest(lane=lane, n=n, culprit=culprit):
                        fake = self.run_train(n, [{culprit}], lane)
                        self.assertEqual(fake.ejected, [(culprit, train.FAILED)])
                        self.assertEqual(fake.landed, list(range(1, culprit)))  # those after it wait for the next train

    def test_two_prs_that_break_only_together_eject_the_later_one(self):
        fake = self.run_train(6, [{2, 5}], lane=2)
        self.assertEqual(fake.ejected, [(5, train.FAILED)])
        self.assertEqual(fake.landed, [1, 2, 3, 4])

    def test_a_passing_train_lands_whole_after_one_build(self):
        fake = self.run_train(5, [], lane=2)
        self.assertEqual((fake.landed, fake.ejected, fake.builds), ([1, 2, 3, 4, 5], [], 1))

    def test_rounds_shrink_with_the_lane(self):
        # 8 PRs, culprit last: lane 1 halves (1 + 3 rounds of 1 build); lane 3 quarters (1 + 2 rounds of up to 3)
        self.assertLessEqual(self.run_train(8, [{8}], lane=1).builds, 4)
        self.assertLessEqual(self.run_train(8, [{8}], lane=3).builds, 7)


class Decisions(unittest.TestCase):
    def test_cut_points_cover_every_prefix_when_the_lane_allows(self):
        cases = {(2, 2): [1], (3, 2): [1, 2], (8, 1): [4], (8, 2): [3, 5], (9, 3): [2, 4, 7], (3, 5): [1, 2]}
        for (n, lane), sizes in cases.items():
            with self.subTest(n=n, lane=lane):
                self.assertEqual(train.cut_points(n, lane), sizes)

    def test_verdict_takes_the_longest_prefix_whose_shorter_prefixes_all_passed(self):
        cases = [({1: True, 2: True}, 3, (2, 3)), ({1: True, 2: False}, 3, (1, 2)), ({3: False, 5: True}, 8, (0, 3)),
                 ({2: True, 4: False, 6: True}, 8, (2, 4))]
        for results, n, expected in cases:
            with self.subTest(results=results):
                self.assertEqual(train.verdict(results, n), expected)

    def test_queue_order_puts_hotfixes_first_then_label_time(self):
        prs = [{"number": 1, "labels": ["queue"], "queued_at": "2026-10-18T02:00:00Z"},
               {"number": 2, "labels": ["queue", "hotfix"], "queued_at": "2026-10-18T03:00:00Z"},
               {"number": 3, "labels": ["queue"], "queued_at": "2026-10-18T01:00:00Z"}]
        self.assertEqual([pr["number"] for pr in train.queue_order(prs)], [2, 3, 1])

    def test_unmet_checks(self):
        runs = [{"name": "build", "status": "completed", "conclusion": "success"},
                {"name": "docs", "status": "completed", "conclusion": "skipped"},
                {"name": "pr", "status": "in_progress", "conclusion": None},
                {"name": "secrets", "status": "completed", "conclusion": "failure"}]
        statuses = [{"context": "train", "state": "pending"}]
        required = ["build", "docs", "pr", "secrets", "review", "train"]
        self.assertEqual(train.unmet_checks(required, runs, statuses), ["pr", "secrets", "review"])

    def test_migration_versions_are_per_directory(self):
        paths = ["internal/billing/migrations/00005_x.sql", "internal/accounts/migrations/5_y.sql",
                 "internal/billing/migrations/README.md", "docs/migrations/00001_a.md"]
        self.assertEqual(train.migration_versions(paths),
                         {("internal/billing/migrations", 5), ("internal/accounts/migrations", 5)})

    def test_landing_step(self):
        entry = Entry(7, SHA)
        cases = [({"state": "MERGED", "headRefOid": SHA, "labels": []}, "landed"),
                 ({"state": "OPEN", "headRefOid": SHA, "labels": ["queue"]}, "merge"),
                 ({"state": "OPEN", "headRefOid": "b" * 40, "labels": ["queue"]}, "its head moved after the train was built"),
                 ({"state": "OPEN", "headRefOid": SHA, "labels": []}, "it left the queue"),
                 ({"state": "CLOSED", "headRefOid": SHA, "labels": ["queue"]}, "closed")]
        for pr, step in cases:
            with self.subTest(pr=pr):
                self.assertEqual(train.landing_step(entry, pr), step)

    def test_train_record_reads_the_entries_back_in_order(self):
        log = f"c3 train: #12 {'c' * 40}\nc2 train: #9 {'b' * 40}\nc1 feat(x): something (#8)\nc0 older\n"
        cars, base = train.train_record(log)
        self.assertEqual(cars, [Car(Entry(9, "b" * 40), "c2"), Car(Entry(12, "c" * 40), "c3")])
        self.assertEqual(base, "c1")

    def test_squash_body_drops_session_links(self):
        self.assertEqual(train.squash_body("Why.\n\nhttps://claude.ai/code/session_x\nRefs #1"), "Why.\n\nRefs #1")
        self.assertEqual(train.squash_body(None), "")


class Flaky(unittest.TestCase):
    LOG = (
        "2026-10-18T10:00:00.1Z === RUN   TestA\n"
        "2026-10-18T10:00:00.2Z --- FAIL: TestA (0.10s)\n"
        "2026-10-18T10:00:00.3Z     --- FAIL: TestA/sub (0.00s)\n"
        "2026-10-18T10:00:00.4Z FAIL\n"
        "2026-10-18T10:00:00.5Z FAIL\taxiom/internal/gateway\t1.2s\n"
        "2026-10-18T10:00:00.6Z ok  \taxiom/internal/billing\t0.5s\n"
    )

    def test_go_failures_name_top_level_tests_by_package(self):
        self.assertEqual(train.go_failures(self.LOG), (["axiom/internal/gateway.TestA"], []))

    def test_a_package_failing_without_a_test_is_unexplained(self):
        log = "FAIL\taxiom/internal/x [build failed]\npanic: test timed out\nFAIL\taxiom/internal/y\t600s\n"
        self.assertEqual(train.go_failures(log), ([], ["axiom/internal/x", "axiom/internal/y"]))

    def test_only_failures_fully_on_the_list_are_covered(self):
        test = "axiom/internal/gateway.TestA"
        cases = [([test], [], ["internal/gateway.TestA"], True),
                 ([test], [], ["gateway.TestAB"], False),
                 ([test, test + "2"], [], ["internal/gateway.TestA"], False),
                 ([test], ["axiom/internal/x"], ["internal/gateway.TestA"], False),
                 ([], [], ["internal/gateway.TestA"], False)]  # no test parsed: lint, web or deploy failed
        for tests, unexplained, flaky, expected in cases:
            with self.subTest(tests=tests, unexplained=unexplained, flaky=flaky):
                self.assertEqual(train.covered(tests, unexplained, flaky), expected)

    def test_the_repository_flaky_list_parses(self):
        train.parse_flaky((Path(__file__).parents[1] / train.FLAKY).read_text(encoding="utf-8"))

    def test_entries_need_an_id_and_an_issue(self):
        for text in ('[[test]]\nid = "x.TestA"\n', '[[test]]\nissue = 3\n', '[[test]]\nid = "x.TestA"\nissue = "3"\n',
                     '[[test]]\nid = "x.TestA"\nissue = 3\nwhy = "no"\n'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                train.parse_flaky(text)


if __name__ == "__main__":
    unittest.main()
