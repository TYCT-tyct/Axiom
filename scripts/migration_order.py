"""Keep module-local migration history append-only, except the proved pre-launch squash."""
import re
import subprocess
import sys
from pathlib import PurePosixPath

from compare_migrations import BASELINE, SOURCE

MIGRATION = re.compile(r"^(.*/migrations)/(\d+)_[^/]*\.sql$")


def git(*args):
    return subprocess.check_output(["git", *args], text=True).splitlines()


def files(ref):
    result = {}
    for line in git("ls-tree", "-r", ref):
        info, path = line.split("\t")
        if MIGRATION.match(path):
            result[path] = info.split()[2]
    return result


def is_squash(history, before, after):
    """Only replace the exact frozen history, entirely, with one baseline per module."""
    directories = {str(PurePosixPath(p).parent) for p in history}
    return bool(history) and before == history and set(after) == {f"{d}/{BASELINE}" for d in directories}


def baseline_transition(base, head="HEAD"):
    before, after = files(base), files(head)
    if not after or any(not p.endswith("/" + BASELINE) for p in after) or any(p.endswith("/" + BASELINE) for p in before):
        return False
    return is_squash(files(SOURCE), before, after)


def versions(paths):
    return {(match[1], int(match[2])) for path in paths if (match := MIGRATION.match(path))}


def out_of_order(taken, added):
    highest = {}
    for directory, version in taken:
        highest[directory] = max(highest.get(directory, 0), version)
    return {(directory, version) for directory, version in added if version <= highest.get(directory, 0)}


def main(base):
    # Require shared history, including for a squash; a missing base must not pass.
    merge_base = git("merge-base", base, "HEAD")[0]
    before, after = files(base), files("HEAD")
    if baseline_transition(base):
        print("pre-production baseline transition: CI must pass compare_migrations.py")
        return 0
    changed = git("diff", "--name-only", "--no-renames", "--diff-filter=a", f"{merge_base}..HEAD", "--", "*/migrations/*")
    if changed:
        print("merged migrations were changed or removed:\n" + "\n".join(changed))
        return 1
    added = git("diff", "--name-only", "--no-renames", "--diff-filter=A", f"{merge_base}..HEAD", "--", "*/migrations/*")
    invalid = out_of_order(versions(before), versions(added))
    if invalid:
        print("new migrations must be above every version already on the base in their module; renumber: " +
              ", ".join(f"{directory}/{version:05d}" for directory, version in sorted(invalid)))
        return 1
    if len(versions(after)) != len(after):
        print("duplicate migration versions in a module")
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
