"""Reuse frontend work by content on the CI host, which keeps its disk between jobs.

The build of the three apps and a passing end-to-end run are stored under a host cache directory, keyed by a hash of
everything that goes into them. Paths are not part of a key, so a checkout in another directory with the same content
still hits; outputs are excluded, so changing only an output does not invalidate. The version string is an input of the
build because the console is stamped with it; the end-to-end key is the inputs its assertions run against (web, api,
all Go code, go.mod/go.sum), not the label, which the tests compare with the same value they are given.
"""

import argparse
import hashlib
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
    files = []
    for base, dirs, names in os.walk(root):
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
    for command in ("node", "pnpm"):
        try:
            result = subprocess.run([command, "--version"], capture_output=True, text=True, check=True)
        except (OSError, subprocess.CalledProcessError):
            versions.append(command + " unknown")
        else:
            versions.append(command + " " + result.stdout.strip())
    return " ".join(versions)


def extra(h, name: str, value: str) -> None:
    h.update(name.encode())
    h.update(b"\0")
    h.update(value.encode())
    h.update(b"\0")


def build_key(root: Path, version: str, tools: str) -> str:
    h = hashlib.sha256()
    extra(h, "format", CACHE_FORMAT)
    extra(h, "web", web_hash(root))
    extra(h, "version", version)
    extra(h, "toolchain", tools)
    extra(h, "platform", platform.platform())
    for name in sorted(os.environ):
        if name != "AXIOM_CACHE_DIR" and name.startswith(("VITE_", "AXIOM_", "NODE_")):
            extra(h, "env " + name, os.environ[name])
    return h.hexdigest()


def e2e_key(root: Path, tools: str) -> str:
    # The version stamp is not an input: the tests compare the console's stamp with the same AXIOM_VERSION they are
    # given, so their pass does not depend on its value. Everything that changes what they check is in web/, api/ or
    # the Go code.
    h = hashlib.sha256()
    extra(h, "format", CACHE_FORMAT)
    extra(h, "inputs", e2e_inputs_hash(root))
    extra(h, "toolchain", tools)
    extra(h, "platform", platform.platform())
    return h.hexdigest()


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
        if target.exists():
            return False  # another job saved the same key first
        os.replace(temp, target)
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
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, saved {target}")
    elif complete(target):
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, another job saved it")
    else:
        print(f"web build cache miss {key[:12]}: built in {elapsed:.1f}s, not saved (missing dist)")
    return 0


def e2e(root: Path, cache: Path, version: str, command: list[str], tools: str) -> int:
    key = e2e_key(root, tools)
    marker = cache / "e2e" / key
    if marker.is_file():
        print(f"e2e cache hit {key[:12]}: skipped {command}, passed before at {marker}")
        return 0
    start = time.monotonic()
    status = run(command, root, version)
    if status:
        return status
    elapsed = time.monotonic() - start
    marker.parent.mkdir(parents=True, exist_ok=True)
    temp = marker.with_name(marker.name + f".{os.getpid()}.tmp")
    temp.write_text(CACHE_FORMAT + "\n", encoding="utf-8")
    os.replace(temp, marker)
    print(f"e2e cache miss {key[:12]}: passed in {elapsed:.1f}s, saved {marker}")
    return 0


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    for action in ("build", "e2e"):
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
    return e2e(root, cache, args.version, command, tools)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
