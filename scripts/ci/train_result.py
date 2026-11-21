"""Reuse successful train jobs only for the exact commit fast-forwarded onto main."""

import json
import os
import subprocess
from pathlib import Path


JOBS = {
    "static": ["static"],
    "web": ["web"],
    "e2e": [f"e2e ({i}/4)" for i in range(1, 5)],
    "boards": [f"boards ({i}/4)" for i in range(1, 5)],
    "vitals": ["vitals"],
    "journey": ["journey"],
    "bench": ["bench"],
    "deploy": ["deploy"],
    "signing": ["signing"],
}


def api(path):
    result = subprocess.run(["gh", "api", f"repos/{os.environ['GITHUB_REPOSITORY']}/{path}"],
                            check=True, text=True, capture_output=True, timeout=5)
    return json.loads(result.stdout)


def find(sha, tree):
    statuses = api(f"commits/{sha}/statuses?per_page=100")
    verified = next((s for s in statuses if s["context"] == "train/verified"), None)
    if not verified or verified["state"] != "success":
        return [], ""
    runs = api(f"actions/workflows/ci.yml/runs?head_sha={sha}&event=workflow_dispatch&status=success&per_page=100")
    for run in runs["workflow_runs"]:
        if not (run["head_sha"] == sha and run["head_branch"].startswith("train/main/")
                and run["head_branch"] != "train/main/land" and run["event"] == "workflow_dispatch"
                and run["status"] == "completed" and run["conclusion"] == "success"):
            continue
        if api(f"git/commits/{run['head_sha']}")["tree"]["sha"] != tree:
            continue
        jobs = api(f"actions/runs/{run['id']}/jobs?filter=latest&per_page=100")
        if jobs["total_count"] > len(jobs["jobs"]):
            return [], ""
        results = {job["name"]: job["conclusion"] for job in jobs["jobs"] if job["status"] == "completed"}
        if any(results.get(name) != "success" for name in ("changes", "build", "static")):
            continue
        return [key for key, names in JOBS.items() if all(results.get(name) == "success" for name in names)], run["html_url"]
    return [], ""


def main():
    reuse, url = [], ""
    if os.environ.get("GITHUB_EVENT_NAME") == "push" and os.environ.get("GITHUB_REF") == "refs/heads/main":
        try:
            tree = subprocess.run(["git", "rev-parse", "HEAD^{tree}"], check=True, text=True,
                                  capture_output=True).stdout.strip()
            reuse, url = find(os.environ["GITHUB_SHA"], tree)
        except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
            print(f"cannot verify train result; running all jobs: {error}")
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(f"reuse={json.dumps(reuse)}\n")
    if reuse:
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
            summary.write(f"Reusing [successful train]({url}) on the same commit and tree.\n\n"
                          + "| Job | Result |\n|---|---|\n"
                          + "".join(f"| {name} | success (train) |\n" for name in reuse))


if __name__ == "__main__":
    main()
