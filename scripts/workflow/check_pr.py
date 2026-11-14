#!/usr/bin/env python3
"""Fail when a pull request's title or description breaks AGENTS.md, when it closes an issue whose acceptance boxes are
unticked, or when its head commit has no passing independent review.

Managed by project-workflow: changed only by upgrades.
  check_pr.py describe   reads PR_TITLE, PR_BODY, PR_AUTHOR, REPO and NUMBER to ask GitHub which issues the PR closes,
                         then reads each as GitHub renders it: the boxes come from the rendered HTML, never from Markdown text.
                         Outside GitHub Actions, without NUMBER, it checks the title and description alone: a writer or
                         a pipeline runs it on the text before the PR exists, instead of learning the same from CI
  check_pr.py review     reads PR_HEAD, PR_BODY, PR_AUTHOR, BASE_REF, the PR's comments as JSON lines on stdin, and git history
Values arrive through the environment, never the command line, so a title or description cannot inject shell.
The review check reads the first lines the review method asks reviewers and authors to write. With one account shared by
the owner and the AI, it proves that a review of this commit, or of an earlier one carrying the same change, was recorded
and answered, or that the author answered a review without blocking findings for the commits pushed after it; not who wrote it.
"""

import functools
import json
import os
import re
import subprocess
import sys
import tomllib
from html.parser import HTMLParser
from pathlib import Path

TYPES = ("feat", "fix", "docs", "refactor", "test", "chore", "ci")
TITLE = re.compile(rf"^(?:{'|'.join(TYPES)})(?:\([^()\s]+\))?!?: [^A-Z\s](?:.*[^.\s])?$")
TITLE_CHARS = 72
COMMENT = re.compile(r"<!--.*?-->", re.DOTALL)
# GitHub closes an issue on merge wherever a keyword precedes a reference to it, even in "does not close #12". The check
# reads the raw text: GitHub skips code and comments, but telling them apart takes a Markdown parser, and quoting is rare.
CLOSING = re.compile(r"\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?[ \t]+"
                     r"(?:(?:[\w.-]+/[\w.-]+)?#[0-9]+|https?://github\.com/[\w.-]+/[\w.-]+/issues/[0-9]+)", re.IGNORECASE)
HEADINGS = ("h1", "h2", "h3", "h4", "h5", "h6")
LINE_START = re.compile(r"[ \t]*(?:(?:[-*+>]|[0-9]+[.)])[ \t]*)*")  # indentation and list or quote markers
SECTION = re.compile(r"^## (.+?)\s*$", re.MULTILINE)
ROOT = Path(__file__).resolve().parents[2]
TEMPLATE = ROOT / ".github" / "pull_request_template.md"
CONFIG = ".github/project-workflow.toml"
REVIEW = re.compile(r"^独立评审 · 提交 ([0-9a-f]{7,40}) · (.*)阻断 ([0-9]+) · 应修 ([0-9]+)")  # (.*) names the reviewer
EFFORT = re.compile(r"\s*[（(](?:minimal|low|medium|high|xhigh|max)[）)]$", re.IGNORECASE)
RESPONSE = re.compile(r"^作者回应 · 提交 ([0-9a-f]{7,40})")
WAIVER = re.compile(r"^免评审[：:][ \t]*\S", re.MULTILINE)
# The issues GitHub closes when the PR merges, by any keyword form it honours or by a link set in the PR's sidebar. After an
# edit GitHub updated them within about a second (measured 2026-10-17); a job reading them starts later than that. Linking an
# issue in the sidebar triggers no workflow, so the check is rerun by hand after that (toolbox reference, pr). GitHub
# leaves out the issues the token may not read (cli/cli#11841), such as those in other private repositories.
CLOSES = ("query($owner: String!, $name: String!, $number: Int!) { repository(owner: $owner, name: $name) { pullRequest(number: $number) "
          "{ closingIssuesReferences(first: 100) { pageInfo { hasNextPage } nodes { number repository { nameWithOwner } } } } } }")
TRUSTED = {"OWNER", "MEMBER", "COLLABORATOR"}  # on a public repository anyone may comment
EXEMPT = {"dependabot[bot]"}  # version bumps: their titles and descriptions are generated and cannot be corrected


def title_warnings(title, author):
    """Length only warns: a longer title costs a truncated line in git log, less than another round of the check."""
    if author in EXEMPT or len(title) <= TITLE_CHARS:
        return []
    return [f"title is {len(title)} characters, over {TITLE_CHARS}: git log and GitHub cut it; shorten it unless it reads worse"]


def describe(title, body, author, template):
    if author in EXEMPT:
        return []
    problems = []
    if not TITLE.match(title):
        problems.append(f"title {title!r}: write type(scope): summary — type one of {', '.join(TYPES)}, lowercase start, "
                        "no trailing period")
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


class Acceptance(HTMLParser):
    """Counts the task-list checkboxes GitHub leaves unticked under each heading that names 验收 (the issue templates'
    验收标准), down to the next heading of the same or a higher level, so subheadings stay inside. The input is the issue's
    body_html, which GitHub renders: comments, code and inline code are already what a reader sees, so no Markdown or comment
    rules are guessed here, and whatever GitHub does with a description is exactly what this counts."""
    def __init__(self):
        super().__init__()
        self.count = 0
        self.section = None  # the level of the acceptance heading still open, or None
        self.heading = None  # the level of the heading being read, or None
        self.text = []

    def handle_starttag(self, tag, attrs):
        if tag in HEADINGS:
            self.heading, self.text = int(tag[1]), []
        elif tag == "input" and self.section is not None:
            attrs = dict(attrs)  # GitHub renders a task list's box as <input type="checkbox">, checked when ticked
            if attrs.get("type") == "checkbox" and "checked" not in attrs:
                self.count += 1

    def handle_endtag(self, tag):
        if tag in HEADINGS and self.heading is not None:
            level, text = self.heading, "".join(self.text)
            self.heading = None
            if self.section is None or level <= self.section:
                self.section = level if "验收" in text else None

    def handle_data(self, data):
        if self.heading is not None:
            self.text.append(data)


def unticked_acceptance(html):
    """The unticked acceptance boxes in an issue description as GitHub renders it; see Acceptance."""
    boxes = Acceptance()
    boxes.feed(html)
    boxes.close()
    return boxes.count


def closing(reply):
    """(repository, number) for each issue in a closingIssuesReferences reply, and the problems it shows. gh exits non-zero
    on a reply with errors, so the pull request is there."""
    references = reply["data"]["repository"]["pullRequest"]["closingIssuesReferences"]
    found = [(node["repository"]["nameWithOwner"], node["number"]) for node in references["nodes"]]
    return found, ["this PR closes more than 100 issues; the check reads only the first 100"] if references["pageInfo"]["hasNextPage"] else []


def unfinished(closes, issue):
    """Problems for each issue the PR closes, as (repository, number) pairs from GitHub, whose acceptance criteria still have
    unticked boxes. issue(repository, number) returns the issue as GitHub's API does, body_html included; issues without an
    acceptance section pass. This proves the boxes were ticked, not that the evidence is there: that stays with the review."""
    problems = []
    for repo, number in closes:
        found = issue(repo, number)
        if "body_html" not in found:  # the request did not ask for rendered HTML: fail rather than count nothing
            problems.append(f"could not read {repo}#{number} as GitHub renders it: no body_html in the reply")
        elif count := unticked_acceptance(found["body_html"] or ""):
            problems.append(f"{repo}#{number} still has {count} unticked acceptance boxes: tick each with its evidence, or stop "
                            f"closing it (write Refs {repo}#{number}, or unlink it in the PR's sidebar); then rerun the pr check")
    return problems


def waiver_allowed(root):
    """Whether the project accepts 免评审 lines: [review] waiver in its config, true unless set. check_docs.py reports a
    malformed config and fails the PR, so this only reads what it can."""
    try:
        return tomllib.loads((root / CONFIG).read_text(encoding="utf-8-sig")).get("review", {}).get("waiver", True) is not False
    except (OSError, UnicodeDecodeError, tomllib.TOMLDecodeError, AttributeError):
        return True


def change(base, commit):
    """The PR's own change at a commit: its diff from where it meets the base branch, with its context lines but without line
    numbers and blob ids. It stays equal while the changed lines and their neighbours do, as when the base is merged in
    elsewhere; a base change next to them, or an edit moved among other neighbours, makes it differ. Binary files compare by content, line endings count. None when git cannot tell (missing history)."""
    git = lambda *args: subprocess.run(["git", *args], capture_output=True, check=True).stdout
    try:
        diff = git("diff", "--no-color", "--no-ext-diff", "--no-renames", "--binary", git("merge-base", base, commit).decode().strip(),
                   commit)
    except (OSError, subprocess.CalledProcessError):
        return None
    return [line for line in diff.split(b"\n") if not line.startswith((b"index ", b"@@"))]


def review(head, body, author, comments, allow_waiver=True, same_change=lambda commit: False, ancestor=lambda commit, later: False):
    """same_change(commit) says whether an earlier commit carries the same change as head, so a review of it still holds;
    ancestor(commit, later) whether commit is later or in its history."""
    if author in EXEMPT:
        return []
    first = [(comment["body"].strip().splitlines() or [""])[0] for comment in comments if comment.get("author_association") in TRUSTED]
    # reviews of the head, and of earlier commits whose change it carries: later commits that only merged the base branch
    # in keep them, so a blocking review of such a commit still counts after another reviewer passes the head
    reviews = [(index, match) for index, line in enumerate(first)
               if (match := REVIEW.match(line)) and (head.startswith(match.group(1)) or same_change(match.group(1)))]
    if not reviews:
        # After a review without blocking findings, the author's answer naming the head records the commits pushed since,
        # such as fixes for its should-fix findings or a merge of the base with conflicts: only blocking ones need another look
        # each reviewer's review of the newest commit in head's history, of one commit the latest posted: never a review of
        # another PR's commit, nor of an older commit posted after a blocking one; last is the newest of them
        last, own = None, {}
        for index, line in enumerate(first):
            if (match := REVIEW.match(line)) and ancestor(match.group(1), head):
                if last is None or ancestor(last[1].group(1), match.group(1)):
                    last = (index, match)
                if (mine := own.get(reviewer(match))) is None or ancestor(mine[1].group(1), match.group(1)):
                    own[reviewer(match)] = (index, match)
        clean = last is not None and not any(int(match.group(3)) for _, match in own.values())
        if clean and any((match := RESPONSE.match(line)) and (head.startswith(match.group(1)) or same_change(match.group(1)))
                         for line in first[last[0] + 1:]):
            return []
        if WAIVER.search(COMMENT.sub("", body)):  # a waiver stands only where no review of this commit was recorded
            if allow_waiver:
                return []
            return [f"this project does not accept 免评审 ([review] waiver = false in {CONFIG}): get an independent review of {head[:7]}"]
        answer = (f"; or, when the commits since {last[1].group(1)[:7]} only address its review or merge the base in, answer that "
                  f"review in a comment whose first line is 作者回应 · 提交 {head[:7]}") if clean else ""
        return [f"no independent review of {head[:7]}: a reviewer other than the author, on a different model, posts the full "
                f"review in a comment whose first line is 独立评审 · 提交 {head[:7]} · <model> · 只读 · 阻断 <n> · 应修 <n> · 细节 <n> "
                f"(open findings by grade){answer}; then rerun this check (on an organization's repository, a member's comments count only "
                f"while the membership is public)"]
    # Each reviewer's latest review stands: a later clean review by another reviewer does not clear an earlier blocking one
    # (#67), while a reviewer re-posting its own review of the commit, as when the owner accepts a finding, replaces it
    latest = {reviewer(match): (index, match) for index, match in reviews}
    for index, match in latest.values():
        reviewed = match.group(1)
        if int(match.group(3)):
            return [f"the review of {reviewed[:7]} has {match.group(3)} blocking findings: fix them, then have the new commit reviewed"]
        answered = any((answer := RESPONSE.match(line)) and (reviewed.startswith(answer.group(1)) or answer.group(1).startswith(reviewed)
                                                             or head.startswith(answer.group(1)))
                       for line in first[index + 1:])
        if int(match.group(4)) and not answered:
            return [f"answer the review of {reviewed[:7]} in a comment whose first line is 作者回应 · 提交 {reviewed[:7]}"]
    return []


def reviewer(match):
    """The reviewer as named on the first line: its model, without 只读 and a trailing effort level in brackets, so one
    reviewer's re-review at another effort still replaces its own review while two models stay apart."""
    name = re.sub(r"[\s·，,]*(只读)?[\s·，,]*$", "", match.group(2))
    return EFFORT.sub("", name).strip()


def main():
    env = lambda name: os.environ.get(name, "")
    if sys.argv[1:] == ["describe"]:
        problems = describe(env("PR_TITLE"), env("PR_BODY"), env("PR_AUTHOR"), TEMPLATE.read_text(encoding="utf-8-sig"))
        for warning in title_warnings(env("PR_TITLE"), env("PR_AUTHOR")):
            print(f"::warning::{warning}" if env("GITHUB_ACTIONS") == "true" else f"warning: {warning}")
        if not env("NUMBER") and env("GITHUB_ACTIONS") != "true":  # no PR yet; in CI a missing number stays a failure
            print("no PR number: the issues this PR would close are not checked")
        elif env("PR_AUTHOR") not in EXEMPT:  # a failed request is a problem, so an outage fails the check instead of passing it
            # body_html is GitHub's own rendering: that is what a reader sees, so the acceptance boxes are read from it
            gh = lambda *args: subprocess.run(["gh", *args], capture_output=True, check=True).stdout
            issue = lambda repo, number: json.loads(gh("api", "-H", "Accept: application/vnd.github.html+json", f"repos/{repo}/issues/{number}"))
            owner, name = env("REPO").split("/")
            try:
                closes, capped = closing(json.loads(gh(
                    "api", "graphql", "-f", f"query={CLOSES}", "-f", f"owner={owner}", "-f", f"name={name}", "-F", f"number={env('NUMBER')}")))
                problems += capped + unfinished(closes, issue)
            except subprocess.CalledProcessError as error:
                problems.append(f"could not read the issues this PR closes: {error.stderr.decode().strip()}")
    elif sys.argv[1:] == ["review"]:
        base = f"origin/{env('BASE_REF')}"
        mine = functools.cache(lambda: change(base, env("PR_HEAD")))  # only needed when no review names the head
        same_change = lambda commit: mine() is not None and change(base, commit) == mine()
        # exit 0 only when it is; a commit git does not have (a review posted to the wrong PR) is not
        ancestor = lambda commit, later: subprocess.run(["git", "merge-base", "--is-ancestor", commit, later], capture_output=True).returncode == 0
        problems = review(env("PR_HEAD"), env("PR_BODY"), env("PR_AUTHOR"), [json.loads(line) for line in sys.stdin if line.strip()],
                          waiver_allowed(ROOT), same_change, ancestor)
    else:
        sys.exit("usage: check_pr.py describe | review")
    for problem in problems:
        print(problem)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
