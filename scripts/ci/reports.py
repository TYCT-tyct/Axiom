"""Keep browser evidence on the runner, isolated by run, job, shard and attempt, for two days."""

import os
import shutil
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import web_cache


def prune(reports: Path, now: float) -> None:
    for run in reports.iterdir():
        try:
            expired = run.is_dir() and run.stat().st_mtime <= now - 2 * 24 * 60 * 60
            if expired:
                shutil.rmtree(run)
        except FileNotFoundError:
            pass  # Another shard can prune the same expired run concurrently.


def prepare(cache: Path, env: dict) -> Path:
    reports = cache / "reports"
    reports.mkdir(parents=True, exist_ok=True)
    prune(reports, time.time())
    run = reports / env["GITHUB_RUN_ID"]
    target = run / env["GITHUB_JOB"] / f'{env.get("E2E_SHARD_INDEX", "checks")}-{env["GITHUB_RUN_ATTEMPT"]}'
    target.mkdir(parents=True, exist_ok=True)
    os.utime(run, None)
    with open(env["GITHUB_STEP_SUMMARY"], "a") as summary:
        summary.write(f'Browser evidence on `{env["RUNNER_NAME"]}`: `{target}` (retained for 2 days; pruned by subsequent jobs).\n')
    # Tests write straight through the container's persistent mount; evidence survives even a killed runner process.
    if output := env.get("GITHUB_ENV"):
        container = Path("/ci/axiom") / target.relative_to(cache)
        with open(output, "a") as file:
            file.write(f"AXIOM_TEST_RESULTS={container}/test-results\nAXIOM_PLAYWRIGHT_REPORTS={container}/playwright-report\n")
    print(f"Browser evidence: {target}")
    return target


def collect(target: Path, root: Path, failed: bool) -> None:
    for name in ("test-results", "playwright-report"):
        source = root / "web" / name
        if source.is_dir() and (failed or name == "playwright-report"):
            shutil.copytree(source, target / name, dirs_exist_ok=True)
    if not failed and (target / "test-results").is_dir():
        shutil.rmtree(target / "test-results")
    os.utime(target.parents[1], None)


if __name__ == "__main__":
    target = prepare(web_cache.cache_root(), os.environ)
    if sys.argv[1] == "collect":
        collect(target, Path.cwd(), os.environ["JOB_STATUS"] != "success")
