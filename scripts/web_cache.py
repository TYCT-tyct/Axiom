"""Reuse frontend work by content on a persistent host or in the Actions cache.

The build of the three apps and passing browser checks are stored under a host cache directory, keyed by a hash of
everything that goes into them. Paths are not part of a key, so a checkout in another directory with the same content
still hits; outputs are excluded, so changing only an output does not invalidate. The version string is an input of the
build because the console is stamped with it; the end-to-end key is the inputs its assertions run against (web, api,
all Go code, go.mod/go.sum), not the label, which the tests compare with the same value they are given.
"""

import argparse
import hashlib
import json
import os
import platform
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path

CACHE_FORMAT = "1"
APPS = ("site", "console", "admin")
# Build outputs, dependencies and test reports; everything else under web/ is an input.
SKIP_DIRS = {"node_modules", "dist", ".vite", "test-results", "playwright-report", "coverage"}


def cache_root(override: str | None = None) -> Path:
    if override:
        return Path(override)
    if configured := os.environ.get("AXIOM_CACHE_DIR"):
        return Path(configured)
    if os.name == "nt":
        base = Path(os.environ.get("LOCALAPPDATA") or Path.home() / "AppData" / "Local")
    else:
        base = Path(os.environ.get("XDG_CACHE_HOME") or Path.home() / ".cache")
    return base / "axiom"


def add_file(h, root: Path, path: Path) -> None:
    h.update(os.path.relpath(path, root).replace(os.sep, "/").encode())
    h.update(b"\0")
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    h.update(b"\0")


def walk_files(root: Path, skip: set[str] = frozenset()) -> list[Path]:
    def failed(error):
        raise error

    files = []
    for base, dirs, names in os.walk(root, onerror=failed):
        dirs[:] = sorted(d for d in dirs if d not in skip and d != ".git")
        for name in sorted(names):
            files.append(Path(base) / name)
    return files


def web_hash(root: Path) -> str:
    h = hashlib.sha256()
    for path in walk_files(root / "web", SKIP_DIRS):
        add_file(h, root, path)
    return h.hexdigest()


def e2e_inputs_hash(root: Path) -> str:
    h = hashlib.sha256()
    for path in walk_files(root / "web", SKIP_DIRS):
        add_file(h, root, path)
    if (root / "api").is_dir():
        for path in walk_files(root / "api"):
            add_file(h, root, path)
    for path in walk_files(root, {"node_modules"}):
        if path.suffix == ".go" or path.name in ("go.mod", "go.sum"):
            add_file(h, root, path)
    return h.hexdigest()


def toolchain() -> str:
    versions = []
    for command in ("node", "pnpm", "go"):
        args = [command, "version"] if command == "go" else [command, "--version"]
        try:
            result = subprocess.run(args, capture_output=True, text=True, check=True)
        except (OSError, subprocess.CalledProcessError):
            versions.append(command + " unknown")
        else:
            versions.append(command + " " + result.stdout.strip())
    return " ".join(versions)


def script_hash() -> str:
    return hashlib.sha256(Path(__file__).resolve().read_bytes()).hexdigest()


ENV_PREFIXES = ("VITE_", "NODE_", "AXIOM_")
ENV_EXCLUDED = {"AXIOM_CACHE_DIR", "AXIOM_WEB_PORT", "AXIOM_VERSION", "AXIOM_TEST_RESULTS", "AXIOM_PLAYWRIGHT_REPORTS"}


def env_inputs():
    """The environment variables Vite or the e2e tests read; one rule for both keys."""
    for name in sorted(os.environ):
        if name.startswith(ENV_PREFIXES) and name not in ENV_EXCLUDED:
            yield name, os.environ[name]


def extra(h, name: str, value: str) -> None:
    h.update(name.encode())
    h.update(b"\0")
    h.update(value.encode())
    h.update(b"\0")


def build_key(root: Path, version: str, tools: str) -> str:
    h = hashlib.sha256()
    extra(h, "format", CACHE_FORMAT)
    extra(h, "script", script_hash())
    extra(h, "web", web_hash(root))
    extra(h, "version", version)
    extra(h, "toolchain", tools)
    extra(h, "platform", platform.platform())
    for name, value in env_inputs():
        extra(h, "env " + name, value)
    return h.hexdigest()


def e2e_key(root: Path, tools: str, command: list[str]) -> str:
    # The version stamp is not an input: the tests compare the console's stamp with the same AXIOM_VERSION they are
    # given, so their pass does not depend on its value. Everything that changes what they check is in web/, api/ or
    # the Go code, and the command: a pass of one spec file must not stand for the whole suite.
    h = hashlib.sha256()
    extra(h, "format", CACHE_FORMAT)
    extra(h, "command", "\0".join(command))
    extra(h, "script", script_hash())
    extra(h, "inputs", e2e_inputs_hash(root))
    for name in ("justfile", "scripts/ci/playwright.py", "scripts/ci/web-container.sh", ".github/workflows/ci.yml"):
        path = root / name
        if path.is_file():
            add_file(h, root, path)
    extra(h, "toolchain", tools)
    extra(h, "platform", platform.platform())
    for name, value in env_inputs():
        extra(h, "env " + name, value)
    return h.hexdigest()


def remote_cache(action: str, target: Path) -> None:
    if not os.environ.get("ACTIONS_CACHE_CLIENT"):
        return
    # Keep archive paths relative, so another checkout restores under its own cache root.
    target.parent.mkdir(parents=True, exist_ok=True)
    script = Path(__file__).resolve().parent / "ci/actions-cache.mjs"
    command = ["node", str(script), action, target.name]
    if action == "restore":
        # A failed extraction can leave a pass marker behind. Publish only a fully restored archive.
        with tempfile.TemporaryDirectory(prefix=".actions-cache-", dir=target.parent) as directory:
            result = subprocess.run(command, cwd=directory)
            source = Path(directory) / target.name
            if result.returncode == 0 and source.exists():
                if target.is_dir():
                    shutil.rmtree(target)
                os.replace(source, target)
    else:
        result = subprocess.run(command, cwd=target.parent)
    if result.returncode:
        print(f"::warning::Actions cache {action} failed for {target.name}; using local cache")


def cached_pass(marker: Path) -> bool:
    if not marker.is_file():
        remote_cache("restore", marker)
    return marker.is_file()


def complete(target: Path) -> bool:
    return (target / ".complete").is_file() and all((target / app / "dist").is_dir() for app in APPS)


def restore(root: Path, target: Path) -> None:
    for app in APPS:
        into = root / "web" / app / "dist"
        shutil.rmtree(into, ignore_errors=True)
        shutil.copytree(target / app / "dist", into)


def save(root: Path, target: Path) -> bool:
    if not all((root / "web" / app / "dist").is_dir() for app in APPS):
        return False
    target.parent.mkdir(parents=True, exist_ok=True)
    temp = Path(tempfile.mkdtemp(prefix=".web-build-", dir=target.parent))
    try:
        for app in APPS:
            shutil.copytree(root / "web" / app / "dist", temp / app / "dist")
        (temp / ".complete").write_text(CACHE_FORMAT + "\n", encoding="utf-8")
        try:
            os.replace(temp, target)
        except OSError:
            if complete(target):
                return False  # another job saved the same key first
            raise
        return True
    finally:
        if temp.exists():
            shutil.rmtree(temp, ignore_errors=True)


def run(command: list[str], root: Path, version: str) -> int:
    env = dict(os.environ)
    env["AXIOM_VERSION"] = version
    try:
        subprocess.run(command, cwd=root, env=env, check=True)
    except subprocess.CalledProcessError as error:
        return error.returncode
    return 0


def build(root: Path, cache: Path, version: str, command: list[str], tools: str) -> int:
    key = build_key(root, version, tools)
    target = cache / "web-build" / key
    if not complete(target):
        remote_cache("restore", target)
    if complete(target):
        restore(root, target)
        print(f"web build cache hit {key[:12]}: restored {target}")
        return 0
    start = time.monotonic()
    status = run(command, root, version)
    if status:
        return status
    elapsed = time.monotonic() - start
    if save(root, target):
        remote_cache("save", target)
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, saved {target}")
    elif complete(target):
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, another job saved it")
    else:
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, not saved (missing dist)")
    return 0


def e2e(root: Path, cache: Path, version: str, command: list[str], tools: str) -> int:
    key = e2e_key(root, tools, command)
    return passing(root, cache / "e2e" / key, version, command, "e2e")


def passing(root: Path, marker: Path, version: str, command: list[str], label: str) -> int:
    if cached_pass(marker):
        print(f"{label} cache hit: passed before at {marker}")
        return 0
    start = time.monotonic()
    status = run(command, root, version)
    if status:
        return status
    elapsed = time.monotonic() - start
    record_pass(marker)
    print(f"{label} cache miss: passed in {elapsed:.1f}s, saved {marker}")
    return 0


def record_pass(marker: Path) -> None:
    marker.parent.mkdir(parents=True, exist_ok=True)
    # Container PIDs repeat across concurrent jobs sharing this host cache.
    with tempfile.NamedTemporaryFile(mode="w", dir=marker.parent, delete=False) as file:
        file.write(CACHE_FORMAT + "\n")
        temp = Path(file.name)
    try:
        os.replace(temp, marker)
        remote_cache("save", marker)
    finally:
        temp.unlink(missing_ok=True)


def vitals(root: Path, cache: Path, version: str, command: list[str], tools: str) -> int:
    h = hashlib.sha256()
    extra(h, "build", build_key(root, version, tools))
    # The overview gate compares with BASE; main (no comparison) cannot certify a train's relative gate.
    extra(h, "base", os.environ.get("BASE", ""))
    extra(h, "server", e2e_inputs_hash(root))
    extra(h, "command", "\0".join(command))
    for name in ("justfile", "scripts/ci/vitals-base.sh", "scripts/ci/web-container.sh", ".github/workflows/ci.yml"):
        add_file(h, root, root / name)
    if output := os.environ.get("AXIOM_TEST_RESULTS"):
        command = [*command, f"--output={output}"]
    return passing(root, cache / "vitals" / h.hexdigest(), version, command, "vitals")


def boards_key(root: Path, board: str, boards: list[str], semantic: dict, selected: list[str], tools: str) -> str:
    h = hashlib.sha256()
    extra(h, "script", script_hash())
    extra(h, "toolchain", tools)
    extra(h, "platform", platform.platform())
    extra(h, "board", board)
    extra(h, "selection", json.dumps(sorted(selected)))
    extra(h, "semantic", json.dumps(semantic, sort_keys=True))
    for name, value in env_inputs():
        extra(h, "env " + name, value)
    package = root / "web/packages/boards"
    paths = []
    for path in walk_files(package, SKIP_DIRS):
        relative = path.relative_to(package).parts
        # Region modules and the generated demo index are evaluated once and partitioned by board by the caller.
        if relative[0] == "manifest" or relative == ("regions.ts",) or relative == ("src", "demo.ts"):
            continue
        if board != "_all" and len(relative) > 2 and relative[0] == "src" and relative[1] in boards and relative[1] != board:
            continue
        if board != "_all" and relative[0] == "inputs" and path.name.split(".")[0] in boards and path.name.split(".")[0] != board:
            continue
        paths.append(path)
    for path in (root / "web/scripts").glob("boards*"):
        paths += walk_files(path, SKIP_DIRS) if path.is_dir() else [path]
    paths += list((root / "web/e2e").glob("boards*.spec.ts"))
    paths += walk_files(root / "web/e2e/design")
    paths += [path for path in (root / "web").iterdir() if path.is_file()]
    paths += [root / name for name in (
        "web/e2e/collect-errors.ts", "web/e2e/apps.ts", "web/scripts/free-port.ts",
        "web/scripts/fonts/sources.json", "web/packages/ui/src/css/fonts/fonts.json",
        "scripts/ci/playwright.py", "scripts/ci/boards-inputs.mjs", "scripts/ci/web-container.sh",
        ".github/workflows/ci.yml",
    )]
    # An explicit design override is also an input, including split boards and pinned engines.
    if design := os.environ.get("AXIOM_DESIGN_DIR"):
        paths += walk_files(Path(design))
    for path in sorted(set(paths)):
        add_file(h, root, path)
    return h.hexdigest()


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    for action in ("build", "e2e", "vitals"):
        p = sub.add_parser(action)
        p.add_argument("--version", default="")
        p.add_argument("--repo", default=".")
        p.add_argument("--cache-dir")
        p.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args(argv)
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a command after -- is required")
    root = Path(args.repo).resolve()
    cache = cache_root(args.cache_dir)
    tools = toolchain()
    if args.action == "build":
        return build(root, cache, args.version, command, tools)
    if args.action == "vitals":
        return vitals(root, cache, args.version, command, tools)
    return e2e(root, cache, args.version, command, tools)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
