import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

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


def ready(number):
    return {"number": number, "state": "OPEN", "headRefOid": SHA, "labels": ["queue"], "baseRefName": "main",
            "title": f"pr {number}", "body": ""}


class FakeLand(train.Train):
    """train/main/land holding PRs 1..n, landed by the real land_verified: main's tree is t<k> after k of them."""

    def __init__(self, n, landed=0, changes=None, merge_error=None, dispatch_error=False, diverge=None):
        self.base, self.dry_run, self.changed = "main", False, 0
        self.cars = [Car(Entry(number, SHA), f"c{number}") for number in range(1, n + 1)]
        self.prs = {number: ready(number) | ({"state": "MERGED"} if number <= landed else {}) for number in range(1, n + 1)}
        self.on_main, self.actions = landed, []
        self.changes = changes or {}  # {after k merges: {number: change}}: what changes while the train lands
        self.merge_error, self.dispatch_error, self.diverge = merge_error, dispatch_error, diverge

    def read_land_ref(self):
        return self.cars, [f"t{k}" for k in range(len(self.cars) + 1)]

    def pr(self, number):
        return self.prs[number]

    def base_tree(self):
        return "elsewhere" if self.diverge == self.on_main else f"t{self.on_main}"

    def base_head(self):
        return f"m{self.on_main}"

    def status(self, *args):
        pass

    def merge(self, entry, pr):
        if self.merge_error == entry.number:
            return "502 Bad Gateway"
        self.actions.append(f"merge {entry.number}")
        self.prs[entry.number]["state"] = "MERGED"
        self.on_main += 1
        for number, change in self.changes.get(self.on_main, {}).items():
            self.prs[number] |= change
        return None

    def dispatch_main(self):
        if self.dispatch_error:
            raise RuntimeError("gh workflow run failed")
        self.actions.append("dispatch")

    def drop_land_ref(self):
        self.actions.append("drop")

    def alarm(self, landed, problem):
        self.actions.append("alarm")
        return train.Abort(problem)


class Landing(unittest.TestCase):
    def test_a_passing_train_lands_whole_then_starts_mains_build_then_forgets_the_record(self):
        fake = FakeLand(3)
        self.assertEqual(fake.land_verified(), "m3")
        self.assertEqual(fake.actions, ["merge 1", "merge 2", "merge 3", "dispatch", "drop"])

    def test_nothing_lands_when_any_pr_changed_after_the_build(self):
        # C fixed what A and B broke together, so the train passed; C then left: A+B alone was never tested
        for change in ({"labels": []}, {"baseRefName": "release"}, {"headRefOid": "b" * 40}, {"state": "CLOSED"}):
            with self.subTest(change=change):
                fake = FakeLand(3)
                fake.prs[3] |= change
                self.assertIsNone(fake.land_verified())
                self.assertEqual(fake.actions, ["drop"])

    def test_resume_merges_the_rest_or_only_starts_mains_build(self):
        for landed, merges in ((1, ["merge 2", "merge 3"]), (3, [])):  # 3: stopped after the last merge
            with self.subTest(landed=landed):
                fake = FakeLand(3, landed=landed)
                fake.land_verified()
                self.assertEqual(fake.actions, merges + ["dispatch", "drop"])

    def test_the_record_stays_until_mains_build_is_dispatched(self):
        fake = FakeLand(2, dispatch_error=True)
        with self.assertRaises(RuntimeError):
            fake.land_verified()
        self.assertEqual(fake.actions, ["merge 1", "merge 2"])

    def test_a_failed_merge_keeps_the_record_for_the_next_run(self):
        fake = FakeLand(3, merge_error=2)
        with self.assertRaises(train.Abort):
            fake.land_verified()
        self.assertEqual(fake.actions, ["merge 1"])

    def test_main_left_untested_raises_an_alarm(self):
        cases = [("a PR changed while landing", dict(changes={1: {2: {"labels": []}}}), ["merge 1"]),
                 ("main diverged before a merge", dict(diverge=0), []),
                 ("main diverged after the last merge", dict(diverge=3), ["merge 1", "merge 2", "merge 3"])]
        for name, options, merges in cases:
            with self.subTest(name):
                fake = FakeLand(3, **options)
                with self.assertRaises(train.Abort):
                    fake.land_verified()
                self.assertEqual(fake.actions, merges + ["alarm"])
        fake = FakeLand(3, landed=1)  # a run stopped part-way, then a PR changed: main holds an untested prefix
        fake.prs[2]["labels"] = []
        with self.assertRaises(train.Abort):
            fake.land_verified()
        self.assertEqual(fake.actions, ["alarm"])


class Alarm(unittest.TestCase):
    def test_an_alarm_holds_the_train_reports_and_starts_mains_build(self):
        fake = FakeLand(2)
        fake.repo, fake.run_url, fake.land_ref, calls = "x/y", "https://github.com/x/y/actions/runs/7", "train/main/land", []
        fake.api = lambda path, *args, **kwargs: calls.append(path)
        with mock.patch.object(train, "git", lambda *args, **kwargs: calls.append(" ".join(args))):
            error = train.Train.alarm(fake, fake.cars[:1], "main does not match the tested train")
        self.assertIsInstance(error, train.Abort)
        self.assertIn("push -q -f origin origin/train/main/land:refs/heads/train-alarm/main/7", calls)
        self.assertEqual(calls[1:], ["issues/1/comments", "issues"])
        self.assertEqual(fake.actions, ["drop", "dispatch"])


class Stopping(unittest.TestCase):
    def test_an_aborted_landing_ends_the_run(self):
        class Run(FakeTrain):
            assembled = 0

            def eligible(self):
                return [Entry(1, SHA), Entry(2, SHA)]

            def assemble(self, base, entries):
                self.assembled += 1
                return super().assemble(base, entries)

            def land(self, base, cars):
                raise train.Abort("main does not match the tested train")

            def alarm_held(self):
                return ""

            def resume(self):
                pass

            def sweep(self):
                pass

            def base_head(self):
                return ""

        fake = Run([], lane=2)
        fake.dry_run, fake.changed = False, 0
        self.assertEqual(fake.run(), 1)
        self.assertEqual((fake.assembled, fake.builds), (1, 1))  # nothing assembled or built after it

    def test_an_open_alarm_stops_every_run(self):
        fake = FakeTrain([], lane=2)
        fake.dry_run = False
        fake.alarm_held = lambda: "abc\trefs/heads/train-alarm/main/1"
        fake.eligible = lambda: self.fail("a run went on past an open alarm")
        self.assertEqual(fake.run(), 1)


class Assembly(unittest.TestCase):
    """assemble() on a real repository: a PR the train just squash-merged is left out, not taken for a duplicate
    migration, while a real duplicate is still ejected."""

    def setUp(self):
        self.cwd, self.tmp = os.getcwd(), tempfile.TemporaryDirectory()
        os.chdir(self.tmp.name)
        train.git("init", "-q")
        self.start = self.commit({"m/migrations/00001_a.sql": "a"})

    def tearDown(self):
        os.chdir(self.cwd)
        self.tmp.cleanup()

    def commit(self, files, parent=None):
        if parent:
            train.git("checkout", "-q", "--detach", parent)
        for path, text in files.items():
            Path(path).parent.mkdir(parents=True, exist_ok=True)
            Path(path).write_text(text + "\n", encoding="utf-8")
            train.git("add", path)
        train.git(*train.IDENTITY, "commit", "-q", "-m", ",".join(files))
        return train.git("rev-parse", "HEAD")

    def test_a_squash_merged_migration_pr_is_left_out_and_a_duplicate_ejected(self):
        merged = self.commit({"m/migrations/00002_b.sql": "b"}, self.start)
        main = self.commit({"m/migrations/00002_b.sql": "b"}, self.start)  # #1 as the train squash-merged it
        duplicate = self.commit({"m/migrations/00002_c.sql": "c"}, self.start)

        class Repo(train.Train):
            def fetch(self, entries):
                for entry in entries:
                    train.git("update-ref", f"refs/remotes/pr/{entry.number}", entry.sha)

            def eject(self, entry, label, reason):
                self.ejected.append(entry.number)

        repo = Repo("x/y", "main")
        repo.ejected = []
        self.assertEqual(repo.assemble(main, [Entry(1, merged), Entry(2, duplicate)]), [])
        self.assertEqual(repo.ejected, [2])

    def test_reserved_high_number_rejects_later_lower_migration(self):
        high = self.commit({"m/migrations/00004_high.sql": "high"}, self.start)
        low = self.commit({"m/migrations/00003_late.sql": "late"}, self.start)

        class Repo(train.Train):
            def fetch(self, entries):
                for entry in entries:
                    train.git("update-ref", f"refs/remotes/pr/{entry.number}", entry.sha)

            def eject(self, entry, label, reason):
                self.ejected.append((entry.number, reason))

        for base, entries in [(self.start, [Entry(1, high), Entry(2, low)]),
                              (high, [Entry(2, low)])]:
            with self.subTest(base=base):
                repo = Repo("x/y", "main")
                repo.ejected = []
                repo.assemble(base, entries)
                self.assertEqual([number for number, _ in repo.ejected], [2])
                self.assertIn("below an already-applied version", repo.ejected[0][1])


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
        runs = [{"id": 1, "name": "build", "status": "completed", "conclusion": "success"},
                {"id": 2, "name": "docs", "status": "completed", "conclusion": "skipped"},
                {"id": 3, "name": "pr", "status": "in_progress", "conclusion": None},
                {"id": 4, "name": "secrets", "status": "completed", "conclusion": "failure"}]
        statuses = [{"context": "merge-train", "state": "pending"}]
        required = ["build", "docs", "pr", "secrets", "review", "merge-train"]
        self.assertEqual(train.unmet_checks(required, runs, statuses), ["pr", "secrets", "review"])

    def test_unmet_checks_reads_the_last_run_of_a_check(self):
        review = lambda id, status, conclusion: {"id": id, "name": "review", "status": status, "conclusion": conclusion}
        cases = {
            "green rerun after a cancel": ([review(3, "completed", "success"), review(2, "completed", "cancelled")], []),
            "failure after a success": ([review(2, "completed", "success"), review(5, "completed", "failure")],
                                        ["review"]),
            "queued rerun": ([review(2, "completed", "success"), review(7, "queued", None)], ["review"]),
            "cancelled before it started, then green": ([review(9, "completed", "success"),
                                                         review(4, "completed", "cancelled")], []),
        }
        for name, (runs, want) in cases.items():
            with self.subTest(name):
                self.assertEqual(train.unmet_checks(["review"], runs, []), want)

    def test_migration_versions_are_per_directory(self):
        paths = ["internal/billing/migrations/00005_x.sql", "internal/accounts/migrations/5_y.sql",
                 "internal/billing/migrations/README.md", "docs/migrations/00001_a.md"]
        self.assertEqual(train.migration_versions(paths),
                         {("internal/billing/migrations", 5), ("internal/accounts/migrations", 5)})

    def test_landing_step(self):
        entry = Entry(7, SHA)
        ready = {"state": "OPEN", "headRefOid": SHA, "labels": ["queue"], "baseRefName": "main"}
        cases = [({"state": "MERGED", "labels": []}, "landed"),
                 ({}, "merge"),
                 ({"headRefOid": "b" * 40}, "its head moved after the train was built"),
                 ({"labels": []}, "it left the queue"),
                 ({"state": "CLOSED"}, "closed"),
                 ({"baseRefName": "release"}, "its base changed to release")]
        for change, step in cases:
            with self.subTest(change=change):
                self.assertEqual(train.landing_step(entry, ready | change, "main"), step)

    def test_landing_plan_lands_whole_or_not_at_all(self):
        cases = [(["merge", "merge"], (0, None)), (["landed", "merge"], (1, None)), (["landed", "landed"], (2, None)),
                 (["merge", "merge", "it left the queue"], (0, "it left the queue")),
                 (["landed", "closed", "merge"], (1, "closed"))]
        for steps, expected in cases:
            with self.subTest(steps=steps):
                self.assertEqual(train.landing_plan(steps), expected)

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

    def test_a_failed_build_offers_a_log_to_read_only_when_the_go_tests_failed(self):
        job = lambda name, conclusion, id=1: {"id": id, "name": name, "conclusion": conclusion}
        cases = [([job("go-test", "failure", 7), job("build", "failure")], 7),
                 ([job("go-test", "failure", 7)], 7),
                 ([job("build", "failure")], None),  # no test failed: nothing a flaky entry could excuse
                 ([job("go-test", "failure"), job("web", "failure")], None),
                 ([job("go-test", "failure"), job("e2e (1/2)", "failure")], None),
                 ([job("go-test", "failure"), job("e2e (2/2)", "failure")], None),
                 ([job("go-test", "success"), job("build", "success")], None)]
        for jobs, expected in cases:
            with self.subTest(jobs=[(j["name"], j["conclusion"]) for j in jobs]):
                found = train.test_job_failing(jobs)
                self.assertEqual(found and found["id"], expected)

    def test_the_repository_flaky_list_parses(self):
        train.parse_flaky((Path(__file__).parents[1] / train.FLAKY).read_text(encoding="utf-8"))

    def test_entries_need_an_id_and_an_issue(self):
        for text in ('[[test]]\nid = "x.TestA"\n', '[[test]]\nissue = 3\n', '[[test]]\nid = "x.TestA"\nissue = "3"\n',
                     '[[test]]\nid = "x.TestA"\nissue = 3\nwhy = "no"\n'):
            with self.subTest(text=text), self.assertRaises(ValueError):
                train.parse_flaky(text)


if __name__ == "__main__":
    unittest.main()
