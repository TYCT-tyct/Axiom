"""Merge train: land queued pull requests in batches that were built together (CONTRIBUTING.md「合并队列」).

Strict merging made every merge void the other queued PRs' builds; merging without it let two PRs that each passed break
main together. A train builds the queued PRs merged onto the latest base in queue order, the same tree main will have
after they land, and lands them only if that build passes.

Invariants:
- A verified train lands whole: a passing build is evidence for its whole tree, not for the shorter prefixes in it (a
  later PR can fix what earlier ones broke together), so every PR of it is checked (open, queued, on the base, at the
  recorded head SHA) before the first merge, and if any fails nothing lands. Each PR merges only at that SHA
  (gh pr merge --match-head-commit).
- Before each merge, the base's tree equals the train's tree just before that PR. A PR that changes after landing
  began (a window of seconds), or a tree that does not match, leaves main on an untested state: the run stops with an
  alarm and no train runs on that base until the train-alarm/<base>/* branch it leaves is deleted.
- Landing is resumable: the verified train is kept as the branch train/<base>/land, whose first-parent merges record
  each PR and its SHA, with the status train/verified on its tip. It is deleted only after every PR landed and main's
  own build was dispatched, so a run that finds it lands what is not yet merged, or only dispatches that build.
- No PR code runs here: assembly only merges commits; the builds are separate ci.yml runs on the train branches,
  dispatched with inputs.base, so ci.yml's own md-only, deploy and bench rules see the train's whole change.

A train is a base commit plus entries; a later train can be assembled on a train that has not landed (dry runs do this
after a passing prefix), which is what pipelining needs.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import tomllib
from dataclasses import dataclass
from urllib.parse import quote

QUEUE, HOTFIX, CONFLICT, FAILED = "queue", "hotfix", "train:conflict", "train:failed"
STATUS = "merge-train"  # required on main; posted on a PR head just before the train merges it. Not "train": that is
# the name of the driver job's own check run, which a labelling run attaches to the PR it drives, so a required context of
# that name made a PR block its own landing while the run was in progress, and keep blocking once it failed
VERIFIED = "train/verified"  # on train/<base>/land: the prefix passed and may be landed
FLAKY = ".github/flaky.toml"
SUBJECT = re.compile(r"train: #(\d+) ([0-9a-f]{40})$")
MIGRATION = re.compile(r"^(.*/migrations)/(\d+)_[^/]*\.sql$")
STAMP = re.compile(r"^\d{4}-\d\d-\d\dT[\d:.]+Z ")  # the timestamp on every line of an Actions job log
GREEN = {"success", "skipped", "neutral"}  # a skipped required check counts as passed on GitHub too
SESSION_LINK = "https://claude.ai/code/session_"
POLL_SECONDS = 60  # four parallel builds polled every minute stay far below GITHUB_TOKEN's 1000 requests an hour
BUILD_MINUTES = 90  # a build normally takes 20-25 minutes; past this it is treated as lost and the train stops
IDENTITY = ["-c", "user.name=merge train", "-c", "user.email=41898282+github-actions[bot]@users.noreply.github.com"]


@dataclass(frozen=True)
class Entry:
    number: int
    sha: str
    title: str = ""


@dataclass(frozen=True)
class Car:
    """An entry merged into a train: commit is the train after this entry, the prefix ending at it."""
    entry: Entry
    commit: str


class Abort(Exception):
    """The run stops without blaming any PR: they stay queued, and a verified train stays recorded for the next run."""


# ---- decisions, kept free of I/O so they are tested directly ----

def queue_order(prs):
    """Hotfixes first, then by the time the queue label was added."""
    return sorted(prs, key=lambda pr: (HOTFIX not in pr["labels"], pr["queued_at"], pr["number"]))


def unmet_checks(required, check_runs, statuses):
    """The required checks, other than train itself, that are not green on a head."""
    state = {run["name"]: run["conclusion"] if run["status"] == "completed" else run["status"] for run in check_runs}
    state.update({status["context"]: status["state"] for status in statuses})
    return [name for name in required if name != STATUS and state.get(name) not in GREEN]


def migration_versions(paths):
    """(directory, version) of each migration file: goose refuses two files with one version in a directory."""
    return {(match[1], int(match[2])) for path in paths if (match := MIGRATION.match(path))}


def cut_points(n, lane):
    """Prefix sizes to build when a train of n > 1 entries failed: `lane` cuts spread over 1..n-1, so each round of
    parallel builds shrinks the failing segment about lane+1 times. With n <= lane+1 these are every prefix
    [1], [1..2], ..., [1..n-1]; the doubling sequence would only queue builds the lane cannot run at once."""
    return sorted({max(1, min(n - 1, round(n * i / (lane + 1)))) for i in range(1, lane + 1)})


def verdict(results, n):
    """Given {prefix size: passed} for a failing train of n entries, return (p, q): every tested prefix up to p passed
    and prefix q failed (q = n if none did), so the culprit is in entries[p:q] and entries[:p] can land."""
    passed = 0
    for size in sorted(results):
        if not results[size]:
            return passed, size
        passed = size
    return passed, n


def go_failures(log):
    """Return (tests, unexplained) from a job log: failed top-level tests as 'package.Test', and failed packages that
    name none (a build failure, a panic or timeout outside a test), which no flaky entry covers."""
    tests, pending, unexplained = [], [], []
    for raw in log.splitlines():
        line = STAMP.sub("", raw)
        if match := re.match(r"--- FAIL: (\S+)", line):
            pending.append(match[1])
        elif match := re.match(r"FAIL\t(\S+)", line):
            if pending:
                tests += [f"{match[1]}.{test}" for test in pending]
            else:
                unexplained.append(match[1])
            pending = []
    return tests, unexplained


def covered(tests, unexplained, flaky):
    """Whether every failure is a listed flaky test; an entry names 'path/to/pkg.TestName' from the module root."""
    return bool(tests) and not unexplained and all(
        any(test == known or test.endswith("/" + known) for known in flaky) for test in tests)


def parse_flaky(text):
    """The flaky list: [[test]] tables with id = 'pkg.TestName' and issue = <open issue number>."""
    entries = tomllib.loads(text).get("test", [])
    for entry in entries:
        if not (isinstance(entry.get("id"), str) and isinstance(entry.get("issue"), int) and set(entry) == {"id", "issue"}):
            raise ValueError(f"{FLAKY}: each [[test]] needs exactly id = \"pkg.TestName\" and issue = <number>: {entry}")
    return entries


def landing_step(entry, pr, base):
    """What to do with one entry of a verified train: 'landed' (an earlier run merged it), 'merge', or why to stop."""
    if pr["baseRefName"] != base:
        return f"its base changed to {pr['baseRefName']}"
    if pr["state"] == "MERGED":
        return "landed" if pr["headRefOid"] == entry.sha else f"merged at {pr['headRefOid'][:7]}, not the tested head"
    if pr["state"] != "OPEN":
        return "closed"
    if pr["headRefOid"] != entry.sha:
        return "its head moved after the train was built"
    if QUEUE not in pr["labels"]:
        return "it left the queue"
    return "merge"


def landing_plan(steps):
    """Return (landed, problem) for a verified train's steps: how many leading PRs are already merged, and why the rest
    cannot all land (None when they can). It lands whole or not at all; see the module's invariants."""
    landed = 0
    while landed < len(steps) and steps[landed] == "landed":
        landed += 1
    return landed, next((step for step in steps[landed:] if step != "merge"), None)


def squash_body(body):
    """The PR description without session links, as the squash commit's message (the owner keeps them out of history)."""
    return "\n".join(line for line in (body or "").splitlines() if not line.startswith(SESSION_LINK))


def train_record(log):
    """Parse `git log --first-parent --format=%H %s` of a train tip into (cars in order, base)."""
    cars, base = [], None
    for line in log.splitlines():
        commit, _, subject = line.partition(" ")
        if match := SUBJECT.match(subject):
            cars.append(Car(Entry(int(match[1]), match[2]), commit))
        else:
            base = commit
            break
    return list(reversed(cars)), base


# ---- GitHub and git ----

def sh(*command, data=None, check=True):
    result = subprocess.run(command, input=data, capture_output=True, text=True, encoding="utf-8")
    if check and result.returncode:
        raise RuntimeError(f"{' '.join(command)} exited {result.returncode}: {result.stderr.strip()}")
    return result


def git(*args, check=True):
    return sh("git", *args, check=check).stdout.strip()


def log(message):
    print(message, flush=True)
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a", encoding="utf-8") as file:
            file.write(f"- {message}\n")


class Train:
    def __init__(self, repo, base, dry_run=False, lane=2, only=()):
        self.repo, self.base, self.dry_run, self.lane, self.only = repo, base, dry_run, lane, set(only)
        self.prefix = f"{'train-dry' if dry_run else 'train'}/{base}/"
        self.land_ref = f"train/{base}/land"
        run_id = os.environ.get("GITHUB_RUN_ID", str(int(time.time())))
        self.run_url = f"{os.environ.get('GITHUB_SERVER_URL', 'https://github.com')}/{repo}/actions/runs/{run_id}"
        self.ids = (f"{run_id}-{n}" for n in range(1, 10_000))
        self.changed = 0  # PRs this run landed or removed from the queue
        try:  # read before assembly checks other commits out, so a queued PR cannot excuse its own failures
            self.flaky_list = parse_flaky(open(FLAKY, encoding="utf-8").read())
        except FileNotFoundError:
            self.flaky_list = []

    # GitHub API

    def api(self, path, *args, data=None, check=True):
        result = sh("gh", "api", f"repos/{self.repo}/{path}", *args, data=data, check=check)
        return json.loads(result.stdout) if result.stdout.strip() and result.returncode == 0 else None

    def pr(self, number):
        fields = "number,state,headRefOid,labels,title,body,baseRefName,isDraft,isCrossRepository"
        pr = json.loads(sh("gh", "pr", "view", str(number), "-R", self.repo, "--json", fields).stdout)
        pr["labels"] = [label["name"] for label in pr["labels"]]
        return pr

    def queued(self):
        """Open PRs on the base with the queue label, in queue order."""
        prs = json.loads(sh("gh", "pr", "list", "-R", self.repo, "--state", "open", "--base", self.base, "--label", QUEUE,
                            "--limit", "200", "--json", "number,headRefOid,labels,title,isDraft,isCrossRepository").stdout)
        for pr in prs:
            pr["labels"] = [label["name"] for label in pr["labels"]]
            events = sh("gh", "api", "--paginate", f"repos/{self.repo}/issues/{pr['number']}/events?per_page=100", "--jq",
                        f'.[] | select(.event == "labeled" and .label.name == "{QUEUE}") | .created_at').stdout.split()
            pr["queued_at"] = events[-1] if events else ""
        return queue_order(prs)

    def eligible(self):
        """Queued PRs whose own required checks are green; the others wait in place. A dry run only reports them."""
        required = [rule_check["context"] for rule in self.api(f"rules/branches/{quote(self.base, safe='')}") or []
                    if rule["type"] == "required_status_checks" for rule_check in rule["parameters"]["required_status_checks"]]
        entries = []
        for pr in self.queued():
            if self.only and pr["number"] not in self.only:
                continue
            waiting = (["draft"] if pr["isDraft"] else []) + (["from a fork"] if pr["isCrossRepository"] else [])
            sha = pr["headRefOid"]
            runs = self.api(f"commits/{sha}/check-runs?filter=latest&per_page=100")["check_runs"]
            waiting += unmet_checks(required, runs, self.api(f"commits/{sha}/status")["statuses"])
            if waiting and not self.dry_run:
                log(f"#{pr['number']} waits: {', '.join(waiting)}")
                continue
            if waiting:
                log(f"#{pr['number']} would wait ({', '.join(waiting)}); a dry run takes it anyway")
            entries.append(Entry(pr["number"], sha, pr["title"]))
        return entries

    def eject(self, entry, label, reason):
        log(f"#{entry.number} leaves the queue ({label}): {reason.splitlines()[0]}")
        self.changed += 1
        self.api(f"issues/{entry.number}/labels/{QUEUE}", "-X", "DELETE", check=False)  # gone already is fine
        self.api(f"issues/{entry.number}/labels", "-X", "POST", "-f", f"labels[]={label}")
        body = f"Merge train ({self.run_url}): removed from the queue with `{label}`.\n\n{reason}\n\nFix it, then add `{QUEUE}` again."
        self.api(f"issues/{entry.number}/comments", "-F", "body=@-", data=body)

    def status(self, sha, context, state, description):
        if not self.dry_run:
            self.api(f"statuses/{sha}", "-X", "POST", "-f", f"state={state}", "-f", f"context={context}",
                     "-f", f"description={description}", "-f", f"target_url={self.run_url}")

    # assembly

    def fetch(self, entries):
        git("fetch", "-q", "origin", self.base, *[f"+refs/pull/{e.number}/head:refs/remotes/pr/{e.number}" for e in entries])

    def added_migrations(self, base, sha):
        return migration_versions(git("diff", "--name-only", "--no-renames", "--diff-filter=A", f"{base}...{sha}").splitlines())

    def try_merge(self, sha, message="try"):
        """Merge sha into HEAD; return the conflicting files ([] when it merged), leaving HEAD unchanged on conflict."""
        merged = sh("git", *IDENTITY, "merge", "-q", "--no-ff", "-m", message, sha, check=False)
        if merged.returncode == 0:
            return []
        files = git("diff", "--name-only", "--diff-filter=U").splitlines() or [merged.stderr.strip() or merged.stdout.strip()]
        git("merge", "--abort", check=False)
        return files

    def new_workflow_blobs(self, sha):
        """Workflow files whose merged content is neither side's: GITHUB_TOKEN may push merges of existing workflow
        changes but not new ones, so such a train could not be pushed (rebasing the PR onto the base fixes it)."""
        changed = git("diff", "--name-only", "HEAD^1", "HEAD", "--", ".github/workflows").splitlines()
        return [path for path in changed if git("ls-tree", "HEAD", "--", path) != git("ls-tree", sha, "--", path)]

    def assemble(self, base, entries):
        """Merge entries onto base in order; eject those that conflict, reuse a migration version, or would need a new
        workflow file. Return the cars; nothing is pushed."""
        self.fetch(entries)
        git("checkout", "-q", "--detach", base)
        taken = migration_versions(git("ls-tree", "-r", "--name-only", base).splitlines())
        cars = []
        for entry in entries:
            fetched = git("rev-parse", f"refs/remotes/pr/{entry.number}")
            if fetched != entry.sha:
                log(f"#{entry.number} moved to {fetched[:7]} while the train was assembled; it waits for the next one")
                continue
            before = git("rev-parse", "HEAD")
            if conflicts := self.try_merge(entry.sha, f"train: #{entry.number} {entry.sha}\n\n{entry.title}"):
                self.eject(entry, CONFLICT, "It conflicts with main or a PR ahead of it in the train:\n\n" + "\n".join(f"- `{f}`" for f in conflicts))
                continue
            # Checked before migrations: a PR the train just squash-merged still lists its migrations as added
            if git("rev-parse", "HEAD^{tree}") == git("rev-parse", f"{before}^{{tree}}"):
                # already in the base: GitHub can list a PR as open and queued for seconds after the train merged it
                git("reset", "-q", "--hard", before)
                log(f"#{entry.number} changes nothing on {base[:7]}; left out")
                continue
            added = self.added_migrations(base, entry.sha)
            if clash := added & taken:
                git("reset", "-q", "--hard", before)
                listed = ", ".join(f"`{directory}` {version:05d}" for directory, version in sorted(clash))
                self.eject(entry, CONFLICT, f"It adds a migration version that main or a PR ahead of it already uses: {listed}. Renumber it.")
                continue
            if blobs := self.new_workflow_blobs(entry.sha):
                git("reset", "-q", "--hard", before)
                self.eject(entry, CONFLICT, "Its workflow changes merge with other changes to the same files: "
                           + ", ".join(f"`{b}`" for b in blobs) + ". Merge main into it.")
                continue
            taken |= added
            cars.append(Car(entry, git("rev-parse", "HEAD")))
        return cars

    # builds

    def build_all(self, base, tips):
        """Build each {key: commit} as its own ci.yml run on a train branch; return {key: (passed, run url)}."""
        runs = {}
        for key, commit in tips.items():
            branch = f"{self.prefix}{next(self.ids)}"
            git("push", "-q", "-f", "origin", f"{commit}:refs/heads/{branch}")
            sh("gh", "workflow", "run", "ci.yml", "-R", self.repo, "--ref", branch, "-f", f"base={base}")
            runs[key] = (branch, commit)
        ids = {key: self.find_run(branch, commit) for key, (branch, commit) in runs.items()}
        log("building " + ", ".join(f"{key}: {self.run_link(run)}" for key, run in ids.items()))
        results = self.wait(ids)
        flaky = self.flaky()
        retry = {key: run for key, run in ids.items() if not results[key] and self.only_flaky(run, flaky)}
        for run in retry.values():
            log(f"every failure in {self.run_link(run)} is on the flaky list; running its failed jobs once more")
            sh("gh", "run", "rerun", str(run), "--failed", "-R", self.repo)
        if retry:
            results |= self.wait(retry, attempt=2)
        return {key: (results[key], self.run_link(run)) for key, run in ids.items()}

    def run_link(self, run):
        return f"https://github.com/{self.repo}/actions/runs/{run}"

    def find_run(self, branch, commit):
        for _ in range(30):
            runs = self.api(f"actions/workflows/ci.yml/runs?branch={branch}&event=workflow_dispatch&per_page=5")["workflow_runs"]
            if found := [run["id"] for run in runs if run["head_sha"] == commit]:
                return found[0]
            time.sleep(5)
        raise Abort(f"the build dispatched for {branch} never appeared")

    def wait(self, ids, attempt=1):
        deadline, results = time.monotonic() + BUILD_MINUTES * 60, {}
        while len(results) < len(ids):
            if time.monotonic() > deadline:
                raise Abort(f"builds still running after {BUILD_MINUTES} minutes: {[self.run_link(ids[k]) for k in ids if k not in results]}")
            time.sleep(POLL_SECONDS)
            for key, run in ids.items():
                if key in results:
                    continue
                state = self.api(f"actions/runs/{run}")
                if state["status"] != "completed" or state["run_attempt"] < attempt:
                    continue
                if state["conclusion"] not in ("success", "failure"):
                    raise Abort(f"build {self.run_link(run)} ended {state['conclusion']}")
                results[key] = state["conclusion"] == "success"
        return results

    def flaky(self):
        """Listed flaky tests whose issue is still open; a closed issue means the entry no longer excuses a failure."""
        return [entry["id"] for entry in self.flaky_list if (self.api(f"issues/{entry['issue']}") or {}).get("state") == "open"]

    def only_flaky(self, run, flaky):
        if not flaky:
            return False
        jobs = self.api(f"actions/runs/{run}/jobs?per_page=100")["jobs"]
        failed = [job for job in jobs if job["conclusion"] == "failure"]
        if not failed or [job["name"] for job in failed] != ["build"]:
            return False
        tests, unexplained = go_failures(sh("gh", "api", f"repos/{self.repo}/actions/jobs/{failed[0]['id']}/logs").stdout)
        log(f"failed tests in {self.run_link(run)}: {tests or 'none parsed'}{'; unexplained: ' + str(unexplained) if unexplained else ''}")
        return covered(tests, unexplained, flaky)

    # landing

    def land(self, base, cars):
        """Land cars (a passing train on base). Return the commit later trains build on, or None when nothing landed."""
        if self.dry_run:
            log(f"dry run: would land {', '.join(f'#{car.entry.number}' for car in cars)}; later trains build on {cars[-1].commit[:7]}")
            return cars[-1].commit
        git("push", "-q", "-f", "origin", f"{cars[-1].commit}:refs/heads/{self.land_ref}")
        self.status(cars[-1].commit, VERIFIED, "success", f"passed; landing {len(cars)} PRs")
        return self.land_verified()

    def land_verified(self):
        """Land train/<base>/land whole, skipping what an earlier run already merged. Return the new base commit, or None
        when nothing could land; raise Abort when it must stop, keeping the record unless main is now untested."""
        cars, trees = self.read_land_ref()
        steps = [landing_step(car.entry, self.pr(car.entry.number), self.base) for car in cars]
        landed, problem = landing_plan(steps)
        if problem and not landed:
            log(f"nothing lands: #{cars[steps.index(problem)].entry.number} {problem}; the train's PRs wait for the next one")
            self.drop_land_ref()
            return None
        if problem:  # an earlier run stopped part-way and a PR changed since: main holds a prefix no build tested
            raise self.alarm(cars[:landed], f"#{cars[steps.index(problem, landed)].entry.number} {problem} after "
                                            f"{landed} of the verified train's {len(cars)} PRs landed")
        for index in range(landed, len(cars)):
            car = cars[index]
            if self.base_tree() != trees[index]:
                raise self.alarm(cars[:index], f"{self.base} does not match the tested train before #{car.entry.number}")
            pr = self.pr(car.entry.number)
            if (step := landing_step(car.entry, pr, self.base)) != "merge":
                if index == 0:
                    log(f"nothing lands: #{car.entry.number} {step}")
                    self.drop_land_ref()
                    return None
                raise self.alarm(cars[:index], f"#{car.entry.number} {step} while its train was landing")
            self.status(car.entry.sha, STATUS, "success", "landed by the merge train")
            if error := self.merge(car.entry, pr):
                raise Abort(f"merging #{car.entry.number} failed, the next run resumes the landing: {error}")
            log(f"landed #{car.entry.number} at {car.entry.sha[:7]}")
            self.changed += 1
        if self.base_tree() != trees[-1]:
            raise self.alarm(cars, f"{self.base} does not match the tested train after its {len(cars)} PRs landed")
        if self.base == "main":
            # merges made with GITHUB_TOKEN start no push workflows, so main's own full build is started here; the
            # record goes only after this succeeds, so a run stopped in between dispatches it on resume
            self.dispatch_main()
        self.drop_land_ref()
        return self.base_head()

    def dispatch_main(self):
        sh("gh", "workflow", "run", "ci.yml", "-R", self.repo, "--ref", self.base)

    def read_land_ref(self):
        """The cars of train/<base>/land and the trees before and after each: trees[i] is the base after i of them."""
        git("fetch", "-q", "origin", self.base, f"+refs/heads/{self.land_ref}:refs/remotes/origin/{self.land_ref}")
        cars, start = train_record(git("log", "--first-parent", "--format=%H %s", f"origin/{self.land_ref}"))
        return cars, [git("rev-parse", f"{commit}^{{tree}}") for commit in [start] + [car.commit for car in cars]]

    def merge(self, entry, pr):
        """Squash-merge at the recorded head; return the error, or None."""
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", suffix=".md", delete=False) as body:
            body.write(squash_body(pr["body"]))
        result = sh("gh", "pr", "merge", str(entry.number), "-R", self.repo, "--squash", "--match-head-commit", entry.sha,
                    "--subject", f"{pr['title']} (#{entry.number})", "--body-file", body.name, check=False)
        os.unlink(body.name)
        return (result.stderr.strip() or "failed") if result.returncode else None

    def drop_land_ref(self):
        git("push", "-q", "origin", f":refs/heads/{self.land_ref}")

    def base_head(self):
        git("fetch", "-q", "origin", self.base)
        return git("rev-parse", f"origin/{self.base}")

    def base_tree(self):
        return git("rev-parse", f"{self.base_head()}^{{tree}}")

    def alarm(self, landed, problem):
        """Report that main is in a state no build tested, keep the train for inspection as train-alarm/<base>/<run>
        (which also stops later runs), and return the Abort that ends this run."""
        hold = f"train-alarm/{self.base}/{self.run_url.rsplit('/', 1)[-1]}"
        git("push", "-q", "-f", "origin", f"origin/{self.land_ref}:refs/heads/{hold}")
        self.drop_land_ref()
        body = (f"**Merge train alarm.** {problem} ({self.run_url}). No train runs on `{self.base}` until the branch "
                f"`{hold}`, which holds the verified train, is deleted: check `{self.base}` first (docs/ops/merge-train.md).")
        for car in landed:
            self.api(f"issues/{car.entry.number}/comments", "-F", "body=@-", data=body)
        self.api("issues", "-X", "POST", "-f", f"title=Merge train: {problem}", "-F", "body=@-", data=body)
        if self.base == "main":
            self.dispatch_main()  # main holds an untested state: its build says whether to revert (merge-train.md)
        return Abort(problem)

    # the driver

    def sweep(self):
        """Delete train branches an earlier run left, cancelling their builds: one train runs at a time."""
        for line in git("ls-remote", "origin", f"refs/heads/{self.prefix}*").splitlines():
            ref = line.split()[1]
            if ref == f"refs/heads/{self.land_ref}":
                continue
            branch = ref.removeprefix("refs/heads/")
            for run in self.api(f"actions/runs?branch={branch}&per_page=20")["workflow_runs"]:
                if run["status"] != "completed":
                    self.api(f"actions/runs/{run['id']}/cancel", "-X", "POST", check=False)
            git("push", "-q", "origin", f":{ref}")

    def alarm_held(self):
        return git("ls-remote", "origin", f"refs/heads/train-alarm/{self.base}/*")

    def resume(self):
        if not git("ls-remote", "origin", f"refs/heads/{self.land_ref}"):
            return
        tip = git("ls-remote", "origin", f"refs/heads/{self.land_ref}").split()[0]
        states = [s["state"] for s in self.api(f"commits/{tip}/status")["statuses"] if s["context"] == VERIFIED]
        if states[:1] == ["success"]:
            log(f"resuming the landing of verified train {tip[:7]}")
            self.land_verified()
        else:
            git("push", "-q", "origin", f":refs/heads/{self.land_ref}")

    def run(self):
        if not self.dry_run and (held := self.alarm_held()):
            log(f"no train runs while an alarm is open: delete {held.split()[1]} once {self.base} is checked")
            return 1
        try:
            if not self.dry_run:
                self.resume()
            self.sweep()
            # Another train follows while the last one changed the queue; one that changed nothing would only repeat itself
            while entries := self.eligible():
                before = self.changed
                try:
                    self.train(entries)
                finally:
                    self.sweep()
                if self.dry_run or self.changed == before:
                    return 0
            return 0
        except Abort as error:  # nothing more is assembled, built or merged in this run
            log(f"the run stops: {error}")
            return 1

    def train(self, entries):
        self.ride(self.base_head(), entries)

    def ride(self, base, entries):
        """Build entries on base (any commit, a train that has not landed included), land what passes and eject the
        PR that breaks it."""
        cars = self.assemble(base, entries)
        if not cars:
            return
        log(f"train on {self.base} {base[:7]}: {', '.join(f'#{car.entry.number}' for car in cars)}")
        passed, url = self.build_all(base, {len(cars): cars[-1].commit})[len(cars)]
        if passed:
            self.land(base, cars)
            return
        while len(cars) > 1:
            results = self.build_all(base, {size: cars[size - 1].commit for size in cut_points(len(cars), self.lane)})
            p, q = verdict({size: result[0] for size, result in results.items()}, len(cars))
            url = results[q][1] if q in results else url
            log("prefix builds: " + ", ".join(f"#{cars[0].entry.number}..#{cars[size - 1].entry.number} "
                                              f"{'passed' if result[0] else 'failed'} {result[1]}" for size, result in sorted(results.items())))
            segment = [car.entry for car in cars[p:q]]
            if p:
                base = self.land(base, cars[:p])
                if base is None:
                    return
                cars = self.assemble(base, segment)
                if len(cars) < len(segment):
                    return  # the segment changed, so what is known about it no longer holds; the next train retries
            else:
                cars = cars[:q]
        culprit = cars[0].entry
        self.status(culprit.sha, STATUS, "failure", "broke the merge train's build")
        self.eject(culprit, FAILED, f"The train's build failed with it and passed without the PRs from it on: {url}")


def precheck(repo, base, number):
    """When a PR is queued, try it against the base and each PR ahead of it so a conflict is reported now, not when its
    train is assembled. Pairs only: a conflict among three is still caught at assembly."""
    train = Train(repo, base)
    pr = train.pr(number)
    if pr["state"] != "OPEN" or pr["baseRefName"] != base or QUEUE not in pr["labels"]:
        return 0
    entry = Entry(number, pr["headRefOid"], pr["title"])
    ahead = []
    for other in train.queued():
        if other["number"] == number:
            break
        ahead.append(Entry(other["number"], other["headRefOid"], other["title"]))
    train.fetch([entry] + ahead)
    tip = git("rev-parse", f"origin/{base}")
    problems = []
    if clash := train.added_migrations(tip, entry.sha) & migration_versions(git("ls-tree", "-r", "--name-only", tip).splitlines()):
        problems.append("adds a migration version main already uses: " + ", ".join(f"`{d}` {v:05d}" for d, v in sorted(clash)))
    git("checkout", "-q", "--detach", tip)
    if files := train.try_merge(entry.sha):
        problems.append("conflicts with main: " + ", ".join(f"`{f}`" for f in files))
    for other in ahead:
        if clash := train.added_migrations(tip, entry.sha) & train.added_migrations(tip, other.sha):
            problems.append(f"adds the same migration version as #{other.number}: " + ", ".join(f"`{d}` {v:05d}" for d, v in sorted(clash)))
        git("checkout", "-q", "--detach", tip)
        if train.try_merge(other.sha):
            continue  # #other conflicts with main itself; its own train will remove it
        if files := train.try_merge(entry.sha):
            problems.append(f"conflicts with #{other.number}, queued ahead of it: " + ", ".join(f"`{f}`" for f in files))
    if problems:
        train.eject(entry, CONFLICT, "Checked when it was queued, it " + ";\n".join(problems) + ".")
        return 0
    for label in (CONFLICT, FAILED):
        if label in pr["labels"]:
            train.api(f"issues/{number}/labels/{label}", "-X", "DELETE", check=False)
    log(f"#{number} merges cleanly with {base} and the {len(ahead)} PRs ahead of it")
    return 0


def flaky_check(repo):
    """Every flaky entry names an open issue: once the issue closes the entry must go, or it would hide a regression."""
    try:
        entries = parse_flaky(open(FLAKY, encoding="utf-8").read())
    except FileNotFoundError:
        return 0
    except (ValueError, tomllib.TOMLDecodeError) as error:
        print(error)
        return 1
    stale = []
    for entry in entries:
        issue = json.loads(sh("gh", "api", f"repos/{repo}/issues/{entry['issue']}").stdout)
        if issue["state"] != "open" or "pull_request" in issue:
            stale.append(f"{entry['id']}: #{entry['issue']} is {'a pull request' if 'pull_request' in issue else issue['state']}")
    for line in stale:
        print(f"{FLAKY}: {line}; remove the entry, or point it at an open issue")
    return 1 if stale else 0


def main(argv):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"))
    parser.add_argument("--base", default="main")
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser("run", help="land what is queued")
    run.add_argument("--dry-run", action="store_true", help="assemble, build, bisect and eject, but neither post statuses nor merge")
    run.add_argument("--prs", default="", help="dry run only: consider just these PR numbers (comma-separated)")
    run.add_argument("--lane", type=int, default=int(os.environ.get("LANE_SIZE", "2")), help="builds the train lane runs at once")
    check = commands.add_parser("precheck", help="check a newly queued PR against the base and the PRs ahead of it")
    check.add_argument("number", type=int)
    commands.add_parser("flaky-check", help=f"fail when an entry in {FLAKY} names an issue that is not open")
    args = parser.parse_args(argv)
    if args.command == "flaky-check":
        return flaky_check(args.repo)
    if args.command == "precheck":
        return precheck(args.repo, args.base, args.number)
    only = [int(n) for n in args.prs.split(",") if n.strip()]
    if only and not args.dry_run:
        parser.error("--prs is for dry runs")
    return Train(args.repo, args.base, args.dry_run, args.lane, only).run()


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
