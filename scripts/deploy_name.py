"""Name a CI deploy job from its runner and its workspace.

Every deploy check runs its own compose project, image tag and volumes on a host shared by runners of several
repositories, so the name has to be unique among the jobs running at once. The runner name alone is not enough: GitHub
requires it to be unique only within its repository, and sanitising can collapse two different names into one
("a.b" and "a-b"). The workspace path is a runner's own directory on the host, so the next job on that runner finds
the same one and can clean up after a killed job; a short hash of it, lowercased because Windows checkouts differ in
case, joins the sanitised runner name.
"""

import hashlib
import re
import sys

DIGEST = 6  # hex characters of the workspace hash: 24 bits, ample for the few runners of one host
UNSAFE = re.compile(r"[^a-z0-9_-]")


def name(runner: str, workspace: str) -> str:
    job = UNSAFE.sub("-", runner.lower()).strip("-") or "local"
    digest = hashlib.sha256(workspace.lower().encode()).hexdigest()[:DIGEST]
    return f"{job}-{digest}"


def main(argv: list[str]) -> None:
    if len(argv) != 2:
        sys.exit("usage: deploy_name.py <runner> <workspace>")
    print(name(*argv))


if __name__ == "__main__":
    main(sys.argv[1:])
