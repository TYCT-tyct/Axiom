"""Select duration-balanced Playwright shards and reuse green board comparisons by content."""

import argparse
import json
import os
import re
import statistics
import subprocess
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import web_cache


def tests(report: dict, parents: tuple = ()) -> dict:
    found = {}
    for suite in report.get("suites", []):
        # The root suite is the file, already present in the test-list description.
        titles = (*parents, suite["title"]) if parents or suite.get("line", 0) else parents
        found.update(tests(suite, titles))
    for spec in report.get("specs", []):
        for test in spec["tests"]:
            name = " › ".join((f'[{test["projectName"]}]', spec["file"], *parents, spec["title"]))
            found[name] = {"file": spec["file"], "title": spec["title"],
                           "title_path": " › ".join((*parents, spec["title"])), "test": test}
    return found


def partition(found: dict, durations: dict, count: int, suite: str, parallel: set = frozenset()) -> list[list[str]]:
    # Only split files that explicitly opt every test into parallel execution. Other files retain their ordering.
    groups = defaultdict(list)
    for name, item in found.items():
        groups[name if suite == "boards" or item["file"] in parallel else item["file"]].append(name)
    fallback = statistics.median(durations.values()) if durations else 1000
    def weight(names):
        return sum(durations.get(name, fallback) for name in names
                   if found[name].get("test", {}).get("expectedStatus") != "skipped")
    loads, shards = [0] * count, [[] for _ in range(count)]
    for names in sorted(groups.values(), key=lambda names: (-weight(names), sorted(names))):
        index = min(range(count), key=lambda i: (loads[i], i))
        shards[index].extend(names)
        loads[index] += weight(names)
    print("predicted shard seconds: " + ", ".join(f"{load / 1000:.1f}" for load in loads))
    return [sorted(shard) for shard in shards]


def board_for(item: dict) -> str:
    if item["file"] == "boards.spec.ts":
        return item["title"].split()[0]
    return "_harness" if item["file"] == "boards-canvas.spec.ts" else "_all"


def green(report: dict, selected: list[str]) -> bool:
    found = tests(report)
    return not report.get("errors") and set(found) == set(selected) and all(
        item["test"]["status"] == "expected" and item["test"]["results"]
        and all(result["status"] == "passed" for result in item["test"]["results"])
        for item in found.values()
    )


def quarantined(report: dict, selected: list[str], entries: list[dict]) -> list[str]:
    found = tests(report)
    if report.get("errors") or set(found) != set(selected):
        return []
    failed = []
    for name, item in found.items():
        test = item["test"]
        if test["status"] == "unexpected":
            if not any(result["status"] in ("failed", "timedOut") for result in test["results"]):
                return []
            if not any(item["file"] == entry["spec"] and item["title_path"] == entry["title"] for entry in entries):
                return []
            failed.append(name)
        elif test["status"] != "expected" and test.get("expectedStatus") != "skipped":
            # An unexecuted dependent test must not disappear behind a successful isolated retry.
            return []
    return failed


def execute(root: Path, cache: Path, suite: str, shard: str, version: str) -> int:
    index, count = map(int, shard.split("/"))
    if not 1 <= index <= count:
        raise ValueError("shard must satisfy 1 <= index <= count")
    command = ["pnpm", "--dir", "web", "exec", "playwright", "test"]
    env = {**os.environ, "AXIOM_VERSION": version, "E2E_SHARD_INDEX": str(index), "E2E_SHARD_COUNT": str(count)}
    if suite == "boards":
        command += ["--config=playwright.boards.config.ts"]
    listed = subprocess.run([*command, "--list", "--reporter=json"], cwd=root, env=env,
                            text=True, capture_output=True, check=True)
    report = json.loads(listed.stdout)
    if report.get("errors"):
        raise ValueError(report["errors"])
    found = tests(report)
    weights = json.loads((root / "scripts/ci/playwright-durations.json").read_text())[suite]
    parallel = set()
    if suite == "e2e":
        for file in {item["file"] for item in found.values()}:
            source = (root / "web/e2e" / file).read_text()
            if re.search(r'''^test\.describe\.configure\(\{\s*mode:\s*["']parallel["']''', source, re.M):
                parallel.add(file)
    selected = partition(found, weights, count, suite, parallel)[index - 1]
    if not selected:
        print(f"{suite} shard {shard}: empty selection")
        return 0
    tools = web_cache.toolchain()
    markers = {}
    if suite == "boards":
        groups = defaultdict(list)
        for name in selected:
            groups[board_for(found[name])].append(name)
        try:
            loaded = subprocess.run(["node", "scripts/ci/boards-inputs.mjs"], cwd=root,
                                    text=True, capture_output=True, check=True)
            inputs = json.loads(loaded.stdout)
            for board, names in groups.items():
                semantic = inputs["inputs"] if board == "_all" else inputs["inputs"].get(board, {})
                key = web_cache.boards_key(root, board, inputs["boards"], semantic, names, tools)
                marker = cache / "boards" / board / key
                if marker.is_file():
                    print(f"boards cache hit {board}: {len(names)} comparisons ({marker})")
                else:
                    print(f"boards cache miss {board}: {len(names)} comparisons")
                    markers[marker] = names
            selected = sorted(name for names in markers.values() for name in names)
        except (OSError, ValueError, subprocess.CalledProcessError) as error:
            # A failed input read can never turn into a pass or a partial selection.
            print(f"cannot read boards cache inputs; running the whole shard: {error}", file=sys.stderr)
            markers = {}
    elif os.environ.get("GATE_STRESS") != "true":
        key = web_cache.e2e_key(root, tools, [*command, *selected])
        marker = cache / "e2e" / key
        if marker.is_file():
            print(f"e2e cache hit: shard {shard} ({marker})")
            return 0
        markers[marker] = selected
    if not selected:
        print(f"boards shard {shard}: all selected comparisons hit the cache")
        return 0
    output = Path(os.environ.get("AXIOM_PLAYWRIGHT_REPORTS", root / "web/playwright-report"))
    output.mkdir(parents=True, exist_ok=True)
    listing, result = output / "selected.txt", output / "report.json"
    listing.write_text("\n".join(selected) + "\n")
    result.unlink(missing_ok=True)
    env["PLAYWRIGHT_JSON_OUTPUT_FILE"] = str(result)
    args = [*command, f"--test-list={listing}", "--reporter=github,json"]
    if evidence := os.environ.get("AXIOM_TEST_RESULTS"):
        args.append(f"--output={evidence}")
    status = subprocess.run(args, cwd=root, env=env).returncode
    if status:
        if suite != "e2e" or not result.is_file():
            return status
        measured = json.loads(result.read_text())
        failed = quarantined(measured, selected, json.loads(os.environ.get("E2E_FLAKY", "[]")))
        if not failed:
            return status
        print("::warning::Quarantined e2e failures: " + ", ".join(failed), flush=True)
        if os.environ.get("E2E_FLAKY_RETRY") != "true":
            return status
        retry_list, retry_result = output / "retry.txt", output / "retry.json"
        retry_list.write_text("\n".join(failed) + "\n")
        retry_result.unlink(missing_ok=True)
        env["PLAYWRIGHT_JSON_OUTPUT_FILE"] = str(retry_result)
        retry_args = [*command, f"--test-list={retry_list}", "--reporter=github,json", "--retries=0"]
        evidence = os.environ.get("AXIOM_TEST_RESULTS", str(root / "web/test-results"))
        retry_args.append(f"--output={evidence}/retry")
        retried = subprocess.run(retry_args, cwd=root, env=env).returncode
        if retried:
            return retried
        if not retry_result.is_file() or not green(json.loads(retry_result.read_text()), failed):
            return 1
        # A quarantined pass must not hide the flake on later PR/main runs via the content cache.
        return 0
    measured = json.loads(result.read_text())
    # e2e has intentionally skipped cases; boards must actually pass every selected comparison.
    if suite == "boards" and not green(measured, selected):
        print("boards did not pass the whole selected set; no passes recorded", file=sys.stderr)
        return 1
    if set(tests(measured)) != set(selected) or measured.get("errors"):
        print("Playwright did not report the selected set; no passes recorded", file=sys.stderr)
        return 1
    for marker in markers:
        web_cache.record_pass(marker)
    durations = {name: sum(result["duration"] for result in item["test"]["results"])
                 for name, item in tests(measured).items() if item["test"]["status"] == "expected"}
    # Kept in the runner report and log, so a maintainer can refresh the checked-in timing snapshot without artifacts.
    entries = sorted(durations.items())
    for start in range(0, len(entries), 100):
        print("CI_TIMINGS=" + json.dumps({suite: dict(entries[start:start + 100])}))
    return 0


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("suite", choices=("boards", "e2e"))
    parser.add_argument("shard")
    parser.add_argument("--version", default="")
    args = parser.parse_args()
    sys.exit(execute(Path.cwd(), web_cache.cache_root(), args.suite, args.shard, args.version))
