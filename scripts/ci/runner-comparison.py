"""Compare ci.yml job durations, runner queues and setup using GitHub's measured timestamps."""

import argparse
import json
import re
import subprocess
from datetime import datetime


def jobs(repo: str, run: str) -> dict:
    pages = json.loads(subprocess.check_output([
        "gh", "api", f"repos/{repo}/actions/runs/{run}/jobs?per_page=100", "--paginate", "--slurp",
    ], text=True))
    return {job["name"]: job for page in pages for job in page["jobs"] if job["conclusion"] != "skipped"}


def seconds(start: str, end: str) -> float:
    return (datetime.fromisoformat(end.replace("Z", "+00:00")) -
            datetime.fromisoformat(start.replace("Z", "+00:00"))).total_seconds()


def timings(job: dict) -> tuple:
    setup = sum(seconds(step["started_at"], step["completed_at"]) for step in job["steps"]
                if step["name"] in ("Run ./.github/actions/setup", "Post Run ./.github/actions/setup"))
    return seconds(job["started_at"], job["completed_at"]), seconds(job["created_at"], job["started_at"]), setup


def estimate(duration: float, cpu: dict) -> float:
    # Utilization is a proxy for the parallel CPU-bound share, not a measured four-core speedup.
    share = min(1, cpu["cpu_seconds"] / (cpu["cores"] * cpu["seconds"]))
    return duration - min(duration, cpu["seconds"]) * share * (1 - cpu["cores"] / 4)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default="TYCT-tyct/Axiom")
    parser.add_argument("--hosted", required=True)
    parser.add_argument("--self-hosted", required=True)
    args = parser.parse_args()
    hosted, own = jobs(args.repo, args.hosted), jobs(args.repo, args.self_hosted)
    print(f"托管：[运行](https://github.com/{args.repo}/actions/runs/{args.hosted})；"
          f"自管：[运行](https://github.com/{args.repo}/actions/runs/{args.self_hosted})。单位：秒。\n")
    print("| 作业 | 托管耗时 | 托管排队 | 托管准备 | 自管耗时 | 自管排队 | 公开四核估算 |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    for name in sorted(hosted.keys() | own.keys()):
        h, s, projected = ("—",) * 3, ("—",) * 3, "—"
        if name in hosted:
            job = hosted[name]
            h = timings(job)
            log = subprocess.check_output([
                "gh", "api", f"repos/{args.repo}/actions/jobs/{job['id']}/logs", "--allow-escape-sequences",
            ], text=True)
            if measured := re.search(r"CI_CPU=(\{[^\n]+\})", log):
                projected = f"{estimate(h[0], json.loads(measured[1])):.1f}"
        if name in own:
            s = timings(own[name])
        print(f"| {name} | {h[0]} | {h[1]} | {h[2]} | {s[0]} | {s[1]} | {projected} |")
    print("\n准备包含 setup 及其缓存收尾；排队为 started_at − created_at，不含上游作业等待。"
          "四核估算以准备结束到收尾开始的虚拟机非空闲 CPU 秒 /（核数 × 墙钟秒）近似可并行 CPU 占比，"
          "只将该时段的 CPU 部分按二核到四核减半，其余时间保持实测值。串行工作也会占用 CPU，"
          "所以这是乐观估计；不推算队列、网络、冷缓存或计时门禁，缺 CPU 记录写 —。")


if __name__ == "__main__":
    main()
