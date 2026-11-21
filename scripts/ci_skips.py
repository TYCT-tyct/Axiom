"""Decide which CI work a pull request can skip from the files base..head changed.

Light changes run Markdown checks (including repository script tests), scope checks and train secrets checks. Outside the
light lane, the Go tests are skipped for web/-only changes, the end-to-end tests when neither the frontend nor the Go
code that serves it changed, a benchmark comparison only for Markdown-only and web/-only changes, the deployment check only
when every changed file is clearly outside the container, and the web vitals only when nothing the pages are built from
changed. The decisions come from the file list, never a guess about what reads what. A failed diff runs the build, the
frontend checks, the timings, the comparison and the deployment check; *-needed actions use exit code 10 for "not needed", so any other outcome runs them.
"""

import fnmatch
import subprocess
import sys
from pathlib import PurePosixPath

from workflow.changes import ROOT, code_changed, lanes

NOT_NEEDED = 10


def changed_paths(base: str, head: str = "HEAD") -> list[str]:
    out = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", f"{base}...{head}"],
        capture_output=True, text=True, check=True,
    ).stdout
    return [line for line in out.splitlines() if line]


def all_md(paths: list[str]) -> bool:
    return bool(paths) and all(path.lower().endswith(".md") for path in paths)


def web_only(paths: list[str]) -> bool:
    return bool(paths) and all(path.startswith("web/") for path in paths)


def needs_benchmark(paths: list[str]) -> bool:
    """Run the comparison for anything but Markdown-only and web/-only changes."""
    return not (all_md(paths) or web_only(paths))


def needs_web(paths: list[str]) -> bool:
    """PR frontend checks: API generation and build/toolchain inputs count as frontend changes too."""
    return not paths or any(
        path.startswith(("web/", "api/", ".github/actions/setup/"))
        or path in {"justfile", "scripts/web_cache.py", "scripts/ci/web-container.sh", "scripts/ci/runner-host.sh",
                    ".github/workflows/ci.yml", "go.mod", "go.sum"}
        for path in paths
    )


# The web/ files the Go program reads when it serves the frontend: the content security policy it sends and the layout of
# the build it embeds (internal/platform/webui). No Go test reads anything else under web/.
GO_READS_WEB = {"web/vite.shared.ts", "web/scripts/embed.ts", "web/scripts/compress.ts"}


def needs_go(paths: list[str]) -> bool:
    """Go tests: any change outside web/ that is not Markdown, and the web/ files the Go program reads. A web/-only change
    leaves every Go package as main has it, so its tests would only rerun what main passed (main runs them anyway)."""
    return not paths or any(
        not (path.lower().endswith(".md") or (path.startswith("web/") and path not in GO_READS_WEB)) for path in paths)


def needs_e2e(paths: list[str]) -> bool:
    """End-to-end tests drive the built pages against API mocks, served by the Go program's webui package: they can only
    change with the frontend and its build inputs (needs_web), with that serving code, or with the CI scripts that shard
    and run them. A change elsewhere in Go meets the same mocks, so the journey, which runs a real node, tests it."""
    return needs_web(paths) or any(path.startswith(("internal/platform/webui/", "scripts/ci/")) for path in paths)


def needs_vitals(paths: list[str]) -> bool:
    """PR and train timings: every file under web/ (the apps, the shared packages, theme.js, and the clients that
    web/packages/api/package.json generates into web/packages/api/src) and the api/*.yaml contracts those clients are
    generated from. A diff outside these ships the same page code and assets, so a changed timing there measures the
    runner (#503)."""
    return not paths or any(
        path.startswith("web/") or (path.startswith("api/") and path.endswith(".yaml")) for path in paths)


def light_only(paths: list[str]) -> bool:
    """Use the managed lane rules, preserving the existing Markdown-only lane."""
    return not code_changed(paths, *lanes(ROOT))


def irrelevant_to_deployment(path: str) -> bool:
    """Whether a changed file is clearly outside the image and the deployment check; anything unsure is relevant."""
    parts = PurePosixPath(path).parts
    name = parts[-1]
    return (
        name.lower().endswith(".md")
        or name.endswith("_test.go")
        or "testdata" in parts[:-1]
        or parts[0] == ".github"
        or (parts[0] == "scripts" and any(fnmatch.fnmatchcase(path, pattern) for pattern in lanes(ROOT)[0]))
        or parts[:2] in (("web", "e2e"), ("web", "journey"))
        or path in ("web/playwright.config.ts", "web/playwright.journey.config.ts")
    )


def needs_deploy_check(paths: list[str]) -> bool:
    """Run it unless every changed file is irrelevant: the container is built from production Go, go.mod, web/, deploy/
    and the justfile, and a startup, config or embedding change there breaks it without breaking a unit test."""
    return not (paths and all(irrelevant_to_deployment(path) for path in paths))


def main(argv: list[str]) -> int:
    if len(argv) not in (2, 3):
        print("usage: ci_skips.py md-only|light-only|bench-needed|deploy-needed|web-needed|vitals-needed|go-needed|e2e-needed"
              " BASE [HEAD]", file=sys.stderr)
        return 2
    action, base = argv[0], argv[1]
    head = argv[2] if len(argv) == 3 else "HEAD"
    try:
        paths = changed_paths(base, head)
        if action == "light-only":
            return 0 if light_only(paths) else 1
    except (subprocess.CalledProcessError, OSError, ValueError) as error:
        print(f"cannot diff {base}...{head}: {error}", file=sys.stderr)
        return 1 if action in ("md-only", "light-only") else 0
    if action == "md-only":
        return 0 if all_md(paths) else 1
    if action == "bench-needed":
        return 0 if needs_benchmark(paths) else NOT_NEEDED
    if action == "web-needed":
        return 0 if needs_web(paths) else NOT_NEEDED
    if action == "vitals-needed":
        return 0 if needs_vitals(paths) else NOT_NEEDED
    if action == "go-needed":
        return 0 if needs_go(paths) else NOT_NEEDED
    if action == "e2e-needed":
        return 0 if needs_e2e(paths) else NOT_NEEDED
    if action == "deploy-needed":
        return 0 if needs_deploy_check(paths) else NOT_NEEDED
    print(f"unknown action {action}", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
