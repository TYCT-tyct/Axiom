#!/usr/bin/env python3
"""Fail when a pull request's title or description breaks AGENTS.md, or when its head commit has no passing independent review.

Managed by project-workflow: changed only by upgrades.
  check_pr.py describe   reads PR_TITLE, PR_BODY, PR_AUTHOR
  check_pr.py review     reads PR_HEAD, PR_BODY, PR_AUTHOR, and the PR's comments as JSON lines on stdin
Values arrive through the environment, never the command line, so a title or description cannot inject shell.
The review check reads the first lines the review method asks reviewers and authors to write. With one account shared by
the owner and the AI, it proves that a review of this exact commit was recorded and answered, not who wrote it.
"""

import json
import os
import re
import sys
from pathlib import Path

TYPES = ("feat", "fix", "docs", "refactor", "test", "chore", "ci")
TITLE = re.compile(rf"^(?:{'|'.join(TYPES)})(?:\([^()\s]+\))?!?: [^A-Z\s](?:.*[^.\s])?$")
TITLE_CHARS = 72
COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
SECTION = re.compile(r"^## (.+?)\s*$", re.MULTILINE)
TEMPLATE = Path(__file__).resolve().parents[2] / ".github" / "pull_request_template.md"
REVIEW = re.compile(r"^独立评审 · 提交 ([0-9a-f]{7,40}) · .*阻断 ([0-9]+) · 应修 ([0-9]+)")
RESPONSE = re.compile(r"^作者回应 · 提交 ([0-9a-f]{7,40})")
WAIVER = re.compile(r"^免评审[：:][ \t]*\S", re.MULTILINE)
TRUSTED = {"OWNER", "MEMBER", "COLLABORATOR"}  # on a public repository anyone may comment
EXEMPT = {"dependabot[bot]"}  # version bumps: their titles and descriptions are generated and cannot be corrected


def describe(title, body, author, template):
    if author in EXEMPT:
        return []
    problems = []
    if len(title) > TITLE_CHARS or not TITLE.match(title):
        problems.append(f"title {title!r}: write type(scope): summary — type one of {', '.join(TYPES)}, lowercase start, "
                        f"no trailing period, at most {TITLE_CHARS} characters")
    body = COMMENT.sub("", body)  # the template's hints alone must not pass
    sections = dict((match.group(1), match) for match in SECTION.finditer(body))
    for heading in SECTION.findall(template):
        found = sections.get(heading)
        following = SECTION.search(body, found.end()) if found else None
        if not found or not body[found.end():following.start() if following else len(body)].strip():
            problems.append(f"description: fill in the section ## {heading}")
    return problems


def review(head, body, author, comments):
    if author in EXEMPT:
        return []
    first = [(comment["body"].strip().splitlines() or [""])[0] for comment in comments if comment.get("author_association") in TRUSTED]
    reviews = [(index, match) for index, line in enumerate(first) if (match := REVIEW.match(line)) and head.startswith(match.group(1))]
    if not reviews:  # a waiver stands only where no review of this commit was recorded, so it cannot overrule one
        if WAIVER.search(COMMENT.sub("", body)):
            return []
        return [f"no independent review of {head[:7]}: post one whose first line follows the review method, then rerun this check"]
    index, latest = reviews[-1]
    if int(latest.group(2)):
        return [f"the review of {head[:7]} has {latest.group(2)} blocking findings: fix them, then have the new commit reviewed"]
    answered = any((match := RESPONSE.match(line)) and head.startswith(match.group(1)) for line in first[index + 1:])
    if int(latest.group(3)) and not answered:
        return [f"answer the review of {head[:7]} in a comment whose first line is 作者回应 · 提交 {head[:7]}"]
    return []


def main():
    env = lambda name: os.environ.get(name, "")
    if sys.argv[1:] == ["describe"]:
        problems = describe(env("PR_TITLE"), env("PR_BODY"), env("PR_AUTHOR"), TEMPLATE.read_text(encoding="utf-8-sig"))
    elif sys.argv[1:] == ["review"]:
        problems = review(env("PR_HEAD"), env("PR_BODY"), env("PR_AUTHOR"), [json.loads(line) for line in sys.stdin if line.strip()])
    else:
        sys.exit("usage: check_pr.py describe | review")
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
