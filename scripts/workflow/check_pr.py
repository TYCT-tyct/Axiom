#!/usr/bin/env python3
"""Fail when a pull request's title or description breaks AGENTS.md, when it closes an issue whose acceptance boxes are
unticked, or when its head commit has no passing independent review.

Managed by project-workflow: changed only by upgrades.
  check_pr.py describe   reads PR_TITLE, PR_BODY, PR_AUTHOR, and REPO to fetch the issues it closes through gh
  check_pr.py review     reads PR_HEAD, PR_BODY, PR_AUTHOR, and the PR's comments as JSON lines on stdin
Values arrive through the environment, never the command line, so a title or description cannot inject shell.
The review check reads the first lines the review method asks reviewers and authors to write. With one account shared by
the owner and the AI, it proves that a review of this exact commit was recorded and answered, not who wrote it.
"""

import json
import os
import re
import subprocess
import sys
import tomllib
from pathlib import Path

TYPES = ("feat", "fix", "docs", "refactor", "test", "chore", "ci")
TITLE = re.compile(rf"^(?:{'|'.join(TYPES)})(?:\([^()\s]+\))?!?: [^A-Z\s](?:.*[^.\s])?$")
TITLE_CHARS = 72
COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
# GitHub closes an issue on merge wherever a keyword precedes a reference to it, even in "does not close #12". The check
# reads the raw text: GitHub skips code and comments, but telling them apart takes a Markdown parser, and quoting is rare.
CLOSING = re.compile(r"\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?[ \t]+"
                     r"(?:(?:[\w.-]+/[\w.-]+)?#[0-9]+|https?://github\.com/[\w.-]+/[\w.-]+/issues/[0-9]+)", re.IGNORECASE)
ISSUE_REF = re.compile(r"(?:([\w.-]+/[\w.-]+)?#|https?://github\.com/([\w.-]+/[\w.-]+)/issues/)([0-9]+)$")
# Issue forms render each field as a heading; the issue templates call this one 验收标准
ACCEPTANCE = re.compile(r"^#{1,6}[ \t]+[^\n]*验收[^\n]*\n(.*?)(?=^#{1,6}[ \t]|\Z)", re.MULTILINE | re.DOTALL)
UNTICKED = re.compile(r"^[ \t]*[-*+][ \t]+\[ \]", re.MULTILINE)
LINE_START = re.compile(r"[ \t]*(?:(?:[-*+>]|[0-9]+[.)])[ \t]*)*")  # indentation and list or quote markers
SECTION = re.compile(r"^## (.+?)\s*$", re.MULTILINE)
ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = ROOT / ".github" / "pull_request_template.md"
CONFIG = ".github/project-workflow.toml"
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
    for match in CLOSING.finditer(body):
        if not LINE_START.fullmatch(body, body.rfind("\n", 0, match.start()) + 1, match.start()):
            problems.append(f"description: {match.group(0)!r}: a closing keyword may only start a line, in code and comments too "
                            "(elsewhere GitHub closes the issue on merge, negated or not); write Refs #N, or start a line with "
                            "Closes #N when this PR completes the issue")
    body = COMMENT.sub("", body)  # the template's hints alone must not pass
    sections = dict((match.group(1), match) for match in SECTION.finditer(body))
    for heading in SECTION.findall(template):
        found = sections.get(heading)
        following = SECTION.search(body, found.end()) if found else None
        if not found or not body[found.end():following.start() if following else len(body)].strip():
            problems.append(f"description: fill in the section ## {heading}")
    return problems


def unfinished(body, repo, issue):
    """Problems for each issue of this repository the PR closes whose acceptance criteria still have unticked boxes.
    issue(number) returns the issue as GitHub's API does; pull requests and issues without an acceptance section pass.
    This proves the boxes were ticked, not that the evidence is there: that stays with the review."""
    problems, seen = [], set()
    for match in CLOSING.finditer(COMMENT.sub("", body)):
        ref = ISSUE_REF.search(match.group(0))
        elsewhere = (ref.group(1) or ref.group(2) or repo).lower() != repo.lower()
        number = int(ref.group(3))
        if elsewhere or number in seen:
            continue
        seen.add(number)
        found = issue(number)
        section = ACCEPTANCE.search((found.get("body") or "").replace("\r\n", "\n"))
        if "pull_request" not in found and section and (count := len(UNTICKED.findall(section.group(1)))):
            problems.append(f"#{number} still has {count} unticked acceptance boxes: tick each with its evidence, or write "
                            f"Refs #{number} instead; then edit this description or rerun the pr check")
    return problems


def waiver_allowed(root):
    """Whether the project accepts 免评审 lines: [review] waiver in its config, true unless set. check_docs.py reports a
    malformed config and fails the PR, so this only reads what it can."""
    try:
        return tomllib.loads((root / CONFIG).read_text(encoding="utf-8-sig")).get("review", {}).get("waiver", True) is not False
    except (OSError, UnicodeDecodeError, tomllib.TOMLDecodeError, AttributeError):
        return True


def review(head, body, author, comments, allow_waiver=True):
    if author in EXEMPT:
        return []
    first = [(comment["body"].strip().splitlines() or [""])[0] for comment in comments if comment.get("author_association") in TRUSTED]
    reviews = [(index, match) for index, line in enumerate(first) if (match := REVIEW.match(line)) and head.startswith(match.group(1))]
    if not reviews:  # a waiver stands only where no review of this commit was recorded, so it cannot overrule one
        if WAIVER.search(COMMENT.sub("", body)):
            if allow_waiver:
                return []
            return [f"this project does not accept 免评审 ([review] waiver = false in {CONFIG}): get an independent review of {head[:7]}"]
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
        if env("PR_AUTHOR") not in EXEMPT:  # a failed request is a problem, so an outage fails the check instead of passing it
            issue = lambda number: json.loads(subprocess.run(["gh", "api", f"repos/{env('REPO')}/issues/{number}"],
                                                             capture_output=True, check=True).stdout)
            try:
                problems += unfinished(env("PR_BODY"), env("REPO"), issue)
            except subprocess.CalledProcessError as error:
                problems.append(f"could not read an issue this PR closes: {error.stderr.decode().strip()}")
    elif sys.argv[1:] == ["review"]:
        problems = review(env("PR_HEAD"), env("PR_BODY"), env("PR_AUTHOR"), [json.loads(line) for line in sys.stdin if line.strip()],
                          waiver_allowed(ROOT))
    else:
        sys.exit("usage: check_pr.py describe | review")
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
