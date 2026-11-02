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
    """train/main/land holding PRs 1..n, landed by the real land_verified: main's head is m<k> after k of them."""

    def __init__(self, n, landed=0, ff=True):
        self.base, self.dry_run, self.changed, self.ff = "main", False, 0, ff  # ff: the ruleset lets the train push main
        self.cars = [Car(Entry(number, SHA), f"c{number}") for number in range(1, n + 1)]
        self.prs = {number: ready(number) | ({"state": "MERGED"} if number <= landed else {}) for number in range(1, n + 1)}
        self.on_main, self.actions = landed, []

    def read_land_ref(self):
        return self.cars

    def pr(self, number):
        return self.prs[number]

    def base_head(self):
        return self.cars[-1].commit if self.on_main == "tip" else f"m{self.on_main}"

    def status(self, *args):
        pass

    def fast_forward(self, tip):
        self.tried_ff = True
        if self.ff:
            self.actions.append("fast-forward")
            self.on_main = "tip"
        return self.ff

    def drop_land_ref(self):
        self.actions.append("drop")


class Landing(unittest.TestCase):
    def test_a_passing_train_lands_whole_in_one_fast_forward_then_forgets_the_record(self):
        fake = FakeLand(3)
        self.assertEqual(fake.land_verified(), "c3")
        # the landing push triggers main's push workflows by itself: landing dispatches no second build
        self.assertEqual((fake.actions, fake.changed), (["fast-forward", "drop"], 3))

    def test_a_refused_fast_forward_lands_nothing_and_fails_the_train(self):
        fake = FakeLand(2, ff=False)
        with self.assertRaises(train.Abort):
            fake.land_verified()
        self.assertEqual((fake.tried_ff, fake.actions, fake.changed), (True, ["drop"], 0))
        self.assertEqual((fake.on_main, [pr["state"] for pr in fake.prs.values()]), (0, ["OPEN", "OPEN"]))

    def test_nothing_lands_when_any_pr_changed_after_the_build(self):
        # C fixed what A and B broke together, so the train passed; C then left: A+B alone was never tested
        for change in ({"labels": []}, {"baseRefName": "release"}, {"headRefOid": "b" * 40}, {"state": "CLOSED"}):
            with self.subTest(change=change), mock.patch.object(train, "log") as log:
                fake = FakeLand(3)
                fake.prs[3] |= change
                self.assertIsNone(fake.land_verified())
                self.assertEqual(fake.actions, ["drop"])
                self.assertIn("#3", log.call_args.args[0])  # the message names the PR that changed, not one ahead of it

    def test_resume_after_a_fast_forward_only_forgets_the_record(self):
        fake = FakeLand(3)
        fake.on_main = "tip"  # the run stopped after the push; GitHub may not have marked the PRs merged yet
        fake.prs[2]["headRefOid"] = "b" * 40  # nor does a PR that moved since matter: main holds the tested tip
        self.assertEqual(fake.land_verified(), "c3")
        self.assertEqual(fake.actions, ["drop"])

    def test_a_train_the_base_already_holds_lands_nothing_more(self):
        fake = FakeLand(3, landed=3)  # an earlier run's push landed it; the base has moved on since
        self.assertEqual(fake.land_verified(), "m3")
        self.assertEqual((fake.actions, fake.changed), (["drop"], 0))

    def test_a_partly_landed_train_lands_nothing(self):
        fake = FakeLand(3, landed=1)  # the base holds a prefix but not the tip, so it cannot take the rest as one push
        self.assertIsNone(fake.land_verified())
        self.assertEqual((fake.actions, fake.changed), (["drop"], 0))

    def test_a_fast_forward_counts_by_where_the_base_is_after_the_push(self):
        # a refused push (the ruleset) leaves the base; a push whose reply was lost may still have moved it
        for returncode, base_after, landed in ((1, "m0", False), (1, "c3", True), (0, "c3", True)):
            with self.subTest(returncode=returncode, base_after=base_after):
                fake = FakeLand(3)
                fake.land_url = "git@github.com:x/y.git"
                fake.base_head = lambda: base_after
                pushed = mock.Mock(returncode=returncode, stderr="remote: error: GH013: Repository rule violations found" if returncode else "")
                with mock.patch.object(train, "sh", return_value=pushed) as sh:
                    self.assertEqual(train.Train.fast_forward(fake, "c3"), landed)
                sh.assert_called_once_with("git", "push", "-q", "git@github.com:x/y.git", "c3:refs/heads/main", check=False)

    def test_the_landing_push_goes_to_the_configured_remote(self):
        with mock.patch.object(train, "FLAKY", "/nonexistent/flaky.toml"), \
                mock.patch.dict(os.environ, {"TRAIN_LAND_URL": "git@github.com:x/y.git"}):
            self.assertEqual(train.Train("x/y", "main").land_url, "git@github.com:x/y.git")
        with mock.patch.object(train, "FLAKY", "/nonexistent/flaky.toml"), mock.patch.dict(os.environ, {}, clear=True):
            self.assertEqual(train.Train("x/y", "main").land_url, "origin")


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


class Assembly(unittest.TestCase):
    """assemble() on a real repository: a PR the train just landed is left out, not taken for a duplicate migration,
    while a real duplicate is still ejected."""

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

    def test_a_migration_pr_already_in_the_base_is_left_out_and_a_duplicate_ejected(self):
        merged = self.commit({"m/migrations/00002_b.sql": "b"}, self.start)
        main = self.commit({"m/migrations/00002_b.sql": "b"}, self.start)  # #1 as the train landed it
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

    def test_cars_carry_the_squash_message_and_read_back_from_a_fast_forwarded_base(self):
        first = self.commit({"a.txt": "a"}, self.start)
        second = self.commit({"b.txt": "b"}, self.start)

        class Repo(train.Train):
            def fetch(self, entries):
                for entry in entries:
                    train.git("update-ref", f"refs/remotes/pr/{entry.number}", entry.sha)

        repo = Repo("x/y", "main")
        [landed] = repo.assemble(self.start, [Entry(1, first, "feat(a): add a", "## Background\n\nRefs #9")])
        [car] = repo.assemble(landed.commit, [Entry(2, second, "fix(b): add b")])  # main fast-forwarded to #1
        self.assertEqual(train.git("log", "-1", "--format=%B", landed.commit).split("\n\n")[:3],
                         ["feat(a): add a (#1)", "## Background", "Refs #9"])
        self.assertEqual(train.git("rev-parse", f"{car.commit}^2"), second)  # GitHub marks #2 merged once this is on main
        cars, base = train.train_record(train.git("log", "--first-parent", train.RECORD, car.commit))
        self.assertEqual((cars, base), ([Car(Entry(2, second), car.commit)], landed.commit))


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
        # c1 is a car of the train fast-forwarded before this one, so the walk stops at the base the cars name
        base, older = "1" * 40, "0" * 40
        log = (f"c3 #12 {'c' * 40} on {base}\nc2 #9 {'b' * 40} on {base}\n{base} #8 {'d' * 40} on {older}\n"
               f"{older} \n")
        cars, start = train.train_record(log)
        self.assertEqual(cars, [Car(Entry(9, "b" * 40), "c2"), Car(Entry(12, "c" * 40), "c3")])
        self.assertEqual(start, base)

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
