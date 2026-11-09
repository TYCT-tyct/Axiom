"""Fail when a branch adds a build output, a binary or a file too large to review.

#358 committed a 35 MB local build output (axiom.exe) for several commits and no check caught it, and a large file stays
in the history once merged. A file fails here when it is over LIMIT bytes, when its suffix is that of a build output, an
archive or a binary asset, or when its first SNIFF bytes hold a NUL byte, which is how git itself decides that a file is
binary. The suffix counts on its own because a build that failed halfway leaves a small or empty axiom.exe that no sniff
finds; the NUL byte catches a format that no suffix announces.

What the branch adds or changes against origin/main is what counts, so a file merged long ago does not fail every pull
request. The worktree is the other side, not HEAD: a staged or an unstaged change is checked before it is committed, and
a file the branch has not added yet is checked too, because a `git add` would take it. .gitignore is what keeps a local
build from failing the check: an ignored file is not committed and not looked at. On main, and where origin/main does not
exist (a partial clone), every file is checked instead, untracked ones included. ALLOWED names, by directory and with the
reason, the files this repository keeps on purpose.

usage: python scripts/check_artifacts.py   (from the repository root; `just artifacts`)
"""

import fnmatch
import os
import subprocess
import sys

MAIN = "refs/remotes/origin/main"
LIMIT = 1 << 20  # 1 MB: the largest file committed is a generated contract of 378 KB; a bigger one no diff can review
SNIFF = 8000  # the bytes git reads to call a file binary; a NUL byte anywhere in them is enough

# Build outputs, archives and binary assets, by suffix: a diff cannot review one, and one of a few bytes is still the
# wrong file in the tree.
BINARY_SUFFIXES = frozenset((
    ".a", ".apk", ".avi", ".bin", ".bmp", ".bz2", ".class", ".db", ".dll", ".dylib", ".eot", ".exe", ".gif", ".gz",
    ".ico", ".jar", ".jpeg", ".jpg", ".lib", ".mov", ".mp3", ".mp4", ".o", ".obj", ".ogg", ".otf", ".pdb", ".pdf",
    ".png", ".pyc", ".rar", ".so", ".sqlite", ".sqlite3", ".tar", ".test", ".tgz", ".tif", ".tiff", ".ttf", ".wasm",
    ".webm", ".webp", ".whl", ".woff", ".woff2", ".xz", ".zip", ".zst",
))

# What the rules below refuse and the tree keeps on purpose: a path pattern, matched against the path from the
# repository root (* crosses directories, as fnmatch matches), and the one-line reason it is committed.
ALLOWED = (
    ("internal/protocol/testdata/*",
     "recorded streams keep the upstream's bytes (.gitattributes), and a planned one is a 16 MiB output"),
    ("internal/platform/*api/*.gen.go",
     "the Go just generate writes from api/*.yaml, where a change is reviewed; the generated check writes it again and "
     "fails on any difference, and the admin contract's server passed 1 MB with its operations"),
    ("internal/observability/testdata/spill/*",
     "call log spills an earlier version wrote, framed in binary lengths and checksums; TestRehearsalSpillsReplay "
     "proves this version still replays them and go test -update rewrites them, and the backup rehearsal loads them"),
)


def git(*args):
    """What a git command writes; a failed command ends the check, so an empty list from it never passes."""
    out = subprocess.run(["git", *args], stdout=subprocess.PIPE)
    if out.returncode:
        sys.exit(f"git {args[0]} failed with status {out.returncode}")
    return out.stdout.decode()


def commit(ref):
    """The commit ref names, or None when it does not exist; any other rev-parse failure ends the check."""
    out = subprocess.run(["git", "rev-parse", "--verify", "--quiet", ref], stdout=subprocess.PIPE)
    if out.returncode > 1:  # 1 is a missing ref
        sys.exit(f"git rev-parse {ref} failed with status {out.returncode}")
    return out.stdout.decode().strip() or None


def candidates():
    """The files this branch adds or changes, untracked ones included; every file on main and without origin/main."""
    main, head = commit(MAIN), commit("HEAD")
    if main is None or main == head:
        changed = git("ls-files", "-z")
    else:
        # The merge base as the one commit, so main's own later changes are not this branch's, and the worktree as the
        # other side: three dots would end at HEAD and miss a staged or an unstaged change, which is what a run before
        # the commit has to see.
        base = git("merge-base", MAIN, "HEAD").strip()
        changed = git("diff", "--name-only", "--no-renames", "--diff-filter=AM", "-z", base)
    # A file a `git add` would take is still untracked; --exclude-standard leaves out the ignored build outputs, so a
    # local `go build` passes a check that .gitignore already keeps out of the commit.
    untracked = git("ls-files", "--others", "--exclude-standard", "-z")
    return [path for path in changed.split("\0") + untracked.split("\0") if path]


def problem(path):
    """Why the file may not be committed, or None."""
    if any(fnmatch.fnmatchcase(path, pattern) for pattern, _ in ALLOWED):
        return None
    size = os.path.getsize(path)
    if size > LIMIT:
        return f"{size} bytes is over the 1 MB limit"
    if os.path.splitext(path.lower())[1] in BINARY_SUFFIXES:
        return "a build output, archive or binary asset"
    with open(path, "rb") as file:
        if b"\0" in file.read(SNIFF):
            return "binary content"
    return None


def main():
    problems = [(path, found) for path in candidates() if (found := problem(path))]
    if problems:
        print("build outputs, binaries and files over 1 MB are not committed; one that belongs here goes in ALLOWED in "
              "scripts/check_artifacts.py with its reason:")
        print("\n".join(f"{path}: {found}" for path, found in problems))
        sys.exit(1)


if __name__ == "__main__":
    main()
