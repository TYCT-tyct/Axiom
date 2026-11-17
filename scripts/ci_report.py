"""The weekly CI health report of issue #830: the numbers nobody watched.

A throttled runner that slowed every job and a 32% train failure rate went unnoticed for days because nothing put them
in one place. This runs weekly over the last seven days of `ci` and `train` runs and reports the run counts and pass
rate per workflow and event, job duration and queue percentiles, runner minutes, failed train builds by failing job,
the PRs the train ejected, and the failing tests that recur most. It replaces the report section of the issue and adds
the same tables as a comment, so the issue body is the current week and the comments are the history.

Only `Api` and `main` talk to GitHub: `report` and what it calls are pure functions of the API's JSON, so the tests run
on a trimmed recording of a real week (testdata/ci_report/) instead of the network. Percentiles are nearest-rank: the
smallest sample at or above p% of the samples, so a job that ran once has p50 == p90 == that run.
"""

import argparse
import json
import os
import re
import subprocess
import time
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone

ISSUE = 830  # this repository's weekly report issue
MARKER = "<!-- ci-report -->"  # the report section starts here; the prose above it is hand-edited
MODULE = "axiom"  # go.mod's module path, which go test prints before a package in its FAIL lines
FAILED = ("failure", "timed_out", "startup_failure")
SKIPPED = ("skipped", "neutral", "stale", "action_required")  # action_required waits for a human, it is no failure
RAN = ("success", "failure", "timed_out")  # a cancelled job's duration is not a duration of that job
EJECTED = ("train:failed", "train:conflict")  # scripts/train.py's labels on a PR it removed from the queue
BUILD = "build"  # ci.yml's aggregator: it fails whenever a part does, so it is no cause of its own
GROUPS = ("ci · PR", "ci · main 推送", "ci · 列车构建", "ci · main 派发",
          "train · 定时", "train · 构建完成", "train · 排队", "train · 手动")
TRAIN_EVENTS = {"schedule": "定时", "workflow_run": "构建完成", "pull_request_target": "排队", "workflow_dispatch": "手动"}
TEST_LIMIT = 10  # failing tests named per report
LOG_LIMIT = 40  # failed job logs read: ten names never need more, and GITHUB_TOKEN allows 1000 requests an hour
RETRY_SECONDS = 10  # a failed call waits this long before its one retry
WORKERS = 6  # gh calls in flight at once: a week is a few hundred of them and one at a time takes 11 minutes
SHARD = re.compile(r" \(\d+/\d+\)$")  # the matrix suffix of `go-test (1/2)`, which is the same job as (2/2)

STAMP = re.compile(r"^\d{4}-\d\d-\d\dT[\d:.]+Z ")  # every Actions log line starts with its timestamp
ANNOTATION = re.compile(r"^##\[[a-z]+\]\s*")  # ##[error] and ##[notice] precede the reporter's own line
GO_FAIL = re.compile(r"--- FAIL: (\S+)")
GO_PACKAGE = re.compile(r"FAIL\t(\S+)")
PLAYWRIGHT = re.compile(r"(?:✘\s*\d+|(?<!\d)\d+\))\s*\[[^\]]+\] › (\S+?) › (.+)")
DURATION = re.compile(r"\s+\(\d+(?:\.\d+)?m?s\)$")


def stamp(value):
    """An Actions timestamp (ISO 8601, Z) as an aware datetime, or None for null."""
    return datetime.fromisoformat(value.replace("Z", "+00:00")) if value else None


def percentile(samples, percent):
    """Nearest rank: the smallest sample at or above `percent`% of them; None when there is none."""
    if not samples:
        return None
    ordered = sorted(samples)
    return ordered[max(1, -(-len(ordered) * percent // 100)) - 1]


def minutes(value):
    return "—" if value is None else f"{value / 60:.1f}"


def seconds(value):
    return "—" if value is None else str(round(value))


def rate(success, failure):
    """Successes over the runs that decided: a cancelled run was superseded and a skipped one had nothing to do, so
    neither says whether the build was healthy."""
    return f"{100 * success / (success + failure):.0f}%" if success + failure else "—"


def table(headers, rows):
    """A Markdown table: the first column left-aligned names, the rest right-aligned numbers."""
    rule = ["---"] + ["--:"] * (len(headers) - 1)
    return "\n".join(["| " + " | ".join(headers) + " |", "| " + " | ".join(rule) + " |"]
                     + ["| " + " | ".join(str(cell) for cell in row) + " |" for row in rows])


def go_failures(lines):
    """The Go tests a log names as failed, qualified by package the way .github/flaky.toml writes an id
    (`internal/observability.TestX`, without the module path go test prints): each `--- FAIL:` line is followed by its
    package's `FAIL\tpackage` line, and go test finishes one package's tests before starting the next."""
    tests, pending = set(), []
    for line in lines:
        if match := GO_FAIL.search(line):
            pending.append(match[1])
        elif match := GO_PACKAGE.match(line):
            package = match[1].removeprefix(MODULE + "/")
            tests |= {f"{package}.{test}" for test in pending}
            pending = []
    return tests


def playwright_failures(lines):
    """The Playwright tests a log names as failed, as `spec file › test title`: the reporter prints a failure once per
    retry and again in its summary, so a set is one failing test in this job."""
    tests = set()
    for line in lines:
        if match := PLAYWRIGHT.search(line):
            tests.add(f"{match[1].split(':')[0]} › {DURATION.sub('', match[2]).strip(' ─')}")
    return tests


def failure_names(log):
    """The tests one job's log names as failed. A job runs either go test or Playwright, and each prints its own shape;
    both reporters repeat a failure, so a name counts once per job."""
    lines = [ANNOTATION.sub("", STAMP.sub("", line)) for line in log.splitlines()]
    return go_failures(lines) | playwright_failures(lines)


def bucket(run):
    """The column a concluded run counts in, or None while it is still running (the next report has it)."""
    conclusion = run.get("conclusion")
    if conclusion == "success":
        return "success"
    if conclusion == "cancelled":
        return "cancelled"
    if conclusion in FAILED:
        return "failure"
    if conclusion in SKIPPED:
        return "skipped"
    return None


def group(run):
    """The row a run belongs on: its workflow and event, with ci's dispatches split by branch, because scripts/train.py
    dispatches ci.yml on a `train/<base>/...` branch for each train build while a main dispatch builds what landed."""
    if run["workflow"] == "train":
        return "train", TRAIN_EVENTS.get(run["event"], run["event"])
    if run["event"] == "pull_request":
        return "ci", "PR"
    if run["event"] == "push":
        return "ci", "main 推送"
    if run["event"] == "workflow_dispatch":
        train = run.get("head_branch", "").startswith(("train/", "train-dry/"))
        return "ci", "列车构建" if train else "main 派发"
    return "ci", run["event"]


def run_rows(runs):
    """Runs, successes, failures, cancellations, skips and pass rate per workflow and event."""
    counts = {}
    for run in runs:
        if column := bucket(run):
            counts.setdefault(" · ".join(group(run)), Counter())[column] += 1
    keys = [key for key in GROUPS if key in counts] + sorted(key for key in counts if key not in GROUPS)
    return [[key, sum(counts[key].values()), counts[key]["success"], counts[key]["failure"],
             counts[key]["cancelled"], counts[key]["skipped"],
             rate(counts[key]["success"], counts[key]["failure"])] for key in keys]


def job_rows(jobs):
    """Per job: samples, p50/p90 duration in minutes, p50/p90 queue in seconds and total runner minutes, costliest
    first. Only the jobs that reached an end count, and matrix shards are one job (`go-test (1/2)` and `(2/2)` are
    `go-test`), because a shard's number says which half of the suite it ran, not what job it is."""
    samples = {}
    for job in jobs:
        started, completed = stamp(job.get("started_at")), stamp(job.get("completed_at"))
        if job.get("conclusion") not in RAN or not (started and completed):
            continue
        sample = samples.setdefault(SHARD.sub("", job["name"]), {"duration": [], "queue": []})
        sample["duration"].append((completed - started).total_seconds())
        if created := stamp(job.get("created_at")):
            sample["queue"].append((started - created).total_seconds())
    rows = []
    for name, sample in samples.items():
        duration, queued = sample["duration"], sample["queue"]
        rows.append((sum(duration), [name, len(duration), minutes(percentile(duration, 50)),
                                     minutes(percentile(duration, 90)), seconds(percentile(queued, 50)),
                                     seconds(percentile(queued, 90)), minutes(sum(duration))]))
    return [row for _, row in sorted(rows, key=lambda item: (-item[0], item[1][0]))]


def train_rows(runs, jobs):
    """The failed train builds and the jobs that failed them; ci.yml's `build` aggregator is left out because it fails
    whenever a part does, so it would name nothing."""
    builds = {run["id"] for run in runs if group(run) == ("ci", "列车构建") and bucket(run) == "failure"}
    failing = Counter(SHARD.sub("", job["name"]) for job in jobs
                      if job.get("run_id") in builds and job.get("conclusion") in FAILED)
    failing.pop(BUILD, None)
    return len(builds), sorted(failing.items(), key=lambda item: (-item[1], item[0]))


def top_failures(failures, limit=TEST_LIMIT):
    """The names that failed in the most jobs, most frequent first and alphabetical within a count, so two weeks'
    reports are comparable."""
    return sorted(Counter(failures).items(), key=lambda item: (-item[1], item[0]))[:limit]


def report(runs, jobs, failures, ejections, since, until):
    """The week's report: the issue body's section and the comment carry the same tables."""
    failed, failing = train_rows(runs, jobs)
    jobs_table = job_rows(jobs)
    tests = top_failures(failures)
    builds = f"本周列车构建失败 {failed} 次；" if failed else "本周没有列车构建失败；"
    queue = f"从队列退车 {ejections} 次（`train:failed` / `train:conflict`；重新排队后会再算一次）。"
    blocks = [f"## {since:%Y-%m-%d %H:%M} → {until:%Y-%m-%d %H:%M} UTC", "",
              "### 运行与通过率", "",
              "通过率是成功 ÷（成功 + 失败）：取消的运行被新推送取代，跳过的是无事可做，都不说明构建健不健康。", "",
              table(["工作流 · 事件", "运行数", "成功", "失败", "取消", "跳过", "通过率"], run_rows(runs)), "",
              "### 作业耗时、排队与机器分钟", "",
              "耗时是作业本身，排队是 `started_at − created_at`，即等 runner 的时间；只算跑到结束的作业，矩阵分片合为一个作业。", "",
              table(["作业", "次数", "耗时 p50（分）", "耗时 p90（分）", "排队 p50（秒）", "排队 p90（秒）", "机器分钟"],
                    jobs_table) if jobs_table else "无", "",
              "### 列车失败", "",
              builds + queue, "",
              table(["失败作业", "次数"], failing) if failing else "无", "",
              "### 失败最多的测试", "",
              "Go 的失败名可以按原样抄进 `.github/flaky.toml` 的 `id`。", "",
              table(["测试", "次数"], tests) if tests else "无"]
    return "\n".join(blocks).rstrip()


def replace_section(body, text):
    """The issue body with its report section replaced: the prose above the marker is hand-edited, everything from it
    on is this week's tables, so each run replaces last week's instead of appending."""
    return f"{body.split(MARKER)[0].rstrip()}\n\n{MARKER}\n\n{text}".lstrip("\n")


def run_link(repo):
    """Back to the run that made the comment, so the history says who generated which week."""
    run_id = os.environ.get("GITHUB_RUN_ID")
    if not run_id:
        return ""
    return f"\n\n本次运行：{os.environ.get('GITHUB_SERVER_URL', 'https://github.com')}/{repo}/actions/runs/{run_id}\n"


def parallel(call, items):
    """`call` over the items, a few at once: each call waits on the network, and serially a week's worth takes longer
    than the workflow's 20 minutes. The calls retry, so a request GitHub answers with a 502 is not the report's end."""
    with ThreadPoolExecutor(max_workers=WORKERS) as pool:
        return list(pool.map(call, items))


class Api:
    """`gh api` against one repository, one call at a time (GitHub asks for serial requests from one token); the
    workflow's token carries actions: read and issues: write."""

    def __init__(self, repo):
        self.repo = repo

    def call(self, path, *args, data=None, check=True):
        """The stdout of one `gh api` call. A failed call is retried once: GitHub answers with a 502 now and then, and
        a report that runs once a week has no second chance. `check=False` is for a call that may legitimately 404, an
        expired job log, and returns nothing instead of retrying. Encoding is stated because the runner's locale is C
        and a job log and the issue body carry UTF-8, which text=True alone would decode as ASCII."""
        for attempt in (1, 2):
            result = subprocess.run(["gh", "api", f"repos/{self.repo}/{path}", *args], input=data,
                                    capture_output=True, text=True, encoding="utf-8")
            if result.returncode == 0 or not check:
                return result.stdout
            if attempt == 1:
                time.sleep(RETRY_SECONDS)
        raise RuntimeError(f"gh api {path} exited {result.returncode}: {result.stderr.strip()}")

    def json(self, path):
        return json.loads(self.call(path))

    def array(self, path, query):
        """A paginated endpoint's items: `--jq` prints one JSON object per line, so the pages concatenate safely."""
        return [json.loads(line) for line in self.call(path, "--paginate", "--jq", query).splitlines()]

    def runs(self, workflow, since):
        """The runs of one workflow created since a stamp; a week of ci runs exceeds the 100 a page holds."""
        return self.array(f"actions/workflows/{workflow}.yml/runs?created=>={since:%Y-%m-%dT%H:%M:%SZ}&per_page=100",
                          ".workflow_runs[] | @json")

    def jobs(self, run):
        return self.json(f"actions/runs/{run}/jobs?per_page=100")["jobs"]

    def failures(self, jobs):
        """The failed test names in the logs of the failed jobs, a name once per job. Logs are the slow half of the
        report, so LOG_LIMIT failed jobs' worth is enough for the ten names and keeps the token's hourly allowance."""
        failed = [job for job in jobs if job.get("conclusion") in FAILED][:LOG_LIMIT]
        # --allow-escape-sequences: a log carries colour codes, which gh refuses to print otherwise
        logs = parallel(lambda job: self.call(f"actions/jobs/{job['id']}/logs", "--allow-escape-sequences", check=False),
                        failed)
        return [name for log in logs for name in sorted(failure_names(log))]

    def ejections(self, since):
        """The PRs the train removed from the queue during the week: eject() adds one of these labels, so the
        repository's labeled events count them without reading the train driver's log."""
        total, page = 0, 1
        while page <= 10:
            events = self.json(f"issues/events?per_page=100&page={page}")
            total += sum(1 for event in events if event["event"] == "labeled" and event["label"]["name"] in EJECTED
                         and stamp(event["created_at"]) >= since)
            if len(events) < 100 or stamp(events[-1]["created_at"]) < since:
                break
            page += 1
        return total


def main(argv=None):
    parser = argparse.ArgumentParser(description="Weekly CI health report for the issue that keeps them.")
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY", ""),
                        help="owner/name (default: $GITHUB_REPOSITORY)")
    parser.add_argument("--issue", type=int, default=ISSUE)
    parser.add_argument("--days", type=int, default=7)
    parser.add_argument("--dry-run", action="store_true", help="print the report instead of writing the issue")
    args = parser.parse_args(argv)
    if not args.repo:
        parser.error("--repo or GITHUB_REPOSITORY")
    until = datetime.now(timezone.utc)
    since = until - timedelta(days=args.days)
    api = Api(args.repo)
    runs = [dict(run, workflow=workflow) for workflow in ("ci", "train") for run in api.runs(workflow, since)]
    # The builds are ci runs; train's own runs are the driver, whose job times say when it was idle, not how long
    # anything took
    jobs = [job for run_jobs in parallel(api.jobs, [run["id"] for run in runs if run["workflow"] == "ci"])
            for job in run_jobs]
    text = report(runs, jobs, api.failures(jobs), api.ejections(since), since, until)
    if args.dry_run:
        print(text)
        return 0
    body = api.json(f"issues/{args.issue}").get("body") or ""
    api.call(f"issues/{args.issue}", "-X", "PATCH", "-F", "body=@-", data=replace_section(body, text))
    api.call(f"issues/{args.issue}/comments", "-X", "POST", "-F", "body=@-", data=text + run_link(args.repo))
    print(f"reported {len(runs)} runs to #{args.issue}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
