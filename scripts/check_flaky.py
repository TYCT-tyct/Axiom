"""Check Go and Playwright quarantine issues; export open e2e entries to the browser job."""

import json
import os
import sys
import tomllib
from pathlib import Path

import train


def e2e_entries(text):
    entries = tomllib.loads(text).get("e2e", [])
    for entry in entries:
        if not (set(entry) == {"spec", "title", "issue"} and isinstance(entry["spec"], str)
                and isinstance(entry["title"], str) and isinstance(entry["issue"], int)):
            raise ValueError('each [[e2e]] needs spec = "file.spec.ts", title = "suite › test" and issue = <number>')
    return entries


def open_e2e(repo, entries):
    opened, stale = [], []
    for entry in entries:
        issue = json.loads(train.sh("gh", "api", f"repos/{repo}/issues/{entry['issue']}").stdout)
        if issue["state"] == "open" and "pull_request" not in issue:
            opened.append(entry)
        else:
            stale.append(f"{train.FLAKY}: {entry['spec']} › {entry['title']}: #{entry['issue']} is not an open issue; remove the entry")
    return opened, stale


def main():
    repo = os.environ["GITHUB_REPOSITORY"]
    text = Path(train.FLAKY).read_text()
    entries, stale = open_e2e(repo, e2e_entries(text))
    if sys.argv[1:] == ["e2e"]:
        print(json.dumps(entries))
        return 0
    status = train.flaky_check(repo)
    for line in stale:
        print(line)
    return 1 if stale else status


if __name__ == "__main__":
    sys.exit(main())
