"""Reject migrations a persistent database could see below its already-applied high-water mark."""
import re
import subprocess
import sys

MIGRATION = re.compile(r"^(.*/migrations)/(\d+)_[^/]*\.sql$")


def versions(paths):
    return {(match[1], int(match[2])) for path in paths if (match := MIGRATION.match(path))}


def out_of_order(taken, added):
    highest = {}
    for directory, version in taken:
        highest[directory] = max(highest.get(directory, 0), version)
    return {(directory, version) for directory, version in added if version <= highest.get(directory, 0)}


def main(base):
    def git(*args):
        return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout.splitlines()
    taken = versions(git("ls-tree", "-r", "--name-only", base))
    added = versions(git("diff", "--name-only", "--no-renames", "--diff-filter=A", f"{base}...HEAD", "--", "*/migrations/*"))
    invalid = out_of_order(taken, added)
    if invalid:
        print("new migrations must be above every version already on the base; renumber: " +
              ", ".join(f"{directory}/{version:05d}" for directory, version in sorted(invalid)))
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1]))
