#!/usr/bin/env python3
"""Fail on broken Markdown links or anchors, docs missing from the docs map and lessons without their evidence;
warn when files outgrow their budgets and when a lesson is due to be promoted or dropped.

Managed by project-workflow: changed only by upgrades. The limits below are defaults; a project records deliberate
departures, each with a comment saying why, under [limits] in .github/project-workflow.toml.
Known limits: Setext headings, HTML and underscore emphasis in headings are not understood, so anchors to such headings may be misjudged.
Windows ignores file-name case, so trust the CI run on Linux. Needs Python 3.11 or later (tomllib).
"""

import os
import re
import sys
import unicodedata
from datetime import date
from pathlib import Path
from urllib.parse import unquote

if sys.version_info < (3, 11):
    sys.exit("check_docs.py needs Python 3.11 or later (tomllib)")
import tomllib

SKIP_DIRS = {".git", "node_modules", ".venv"}
CONFIG = ".github/project-workflow.toml"
SOURCE = re.compile(r"[0-9a-f]{40}")
LIMITS = {
    "doc_lines": 300,  # split a longer doc by subtopic
    "always_loaded_lines": 150,  # AGENTS.md + CONTRIBUTING.md, read in every session
    "lesson_days": 30,  # a lesson not seen again for this long is dropped
}

INLINE_LINK = re.compile(r"\]\(\s*(<[^>]*>|[^)\s]+)")
REFERENCE_DEF = re.compile(r"^ {0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)")
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")
CODE_SPAN = re.compile(r"(`+).*?\1")
SCHEME = re.compile(r"^[a-z][a-z0-9+.-]*:", re.IGNORECASE)
HEADING = re.compile(r"^ {0,3}#{1,6}\s+(.*?)(?:\s+#+)?\s*$")
LAST_RETRO = re.compile(r"^上次复盘[：:]\s*([0-9]{4}-[0-9]{2}-[0-9]{2})\s*$")
FIELDS = ("证据", "哪一层没拦住", "出现")
FIELD = re.compile(rf"^({'|'.join(FIELDS)})[：:]\s*(.*?)\s*$")
DAY = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}")
LOCATED = re.compile(r"^([^:\s]+?)(?::([0-9]+))?: ")


def markdown_files(root):
    for directory, subdirs, files in os.walk(root):
        subdirs[:] = [name for name in subdirs if name not in SKIP_DIRS]
        for name in files:
            if name.endswith(".md"):
                yield Path(directory, name)


def prose_lines(path):
    """Yield (line number, line) outside fenced code blocks."""
    fence = None
    for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):  # -sig: Notepad writes a BOM
        if fence:
            # A fence closes only with the same character, at least as long, and nothing else on the line.
            stripped = line.strip()
            if stripped and set(stripped) == {fence[0]} and len(stripped) >= len(fence):
                fence = None
            continue
        opening = FENCE.match(line)
        if opening:
            fence = opening.group(1)
            continue
        yield number, line


def anchor_of(heading):
    """GitHub's anchor for a heading: keep the rendered text, lowercase it, drop punctuation, turn spaces into hyphens."""
    text = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", r"\1", heading)  # links and images keep their text
    text = re.sub(r"[`*~]", "", text)  # code and emphasis markers
    kept = (c for c in text.lower() if c in " -" or unicodedata.category(c)[0] in "LMN" or unicodedata.category(c) == "Pc")
    return "".join(kept).replace(" ", "-")


def anchors(path):
    seen, result = {}, set()
    for _, line in prose_lines(path):
        heading = HEADING.match(line)
        if heading:
            anchor = anchor_of(heading.group(1))
            count = seen.get(anchor, 0)
            seen[anchor] = count + 1
            result.add(anchor if count == 0 else f"{anchor}-{count}")
    return result


def links(path, root):
    """Yield (line number, target file, anchor) for relative links; the anchor is '' when absent."""
    for number, line in prose_lines(path):
        reference = REFERENCE_DEF.match(line)
        targets = [reference.group(1)] if reference else []
        targets += INLINE_LINK.findall(CODE_SPAN.sub("", line))
        for target in targets:
            target = target.strip("<>")
            if SCHEME.match(target):
                continue  # external URL or mailto
            file_part, _, anchor = target.partition("#")
            file_part = unquote(file_part)
            base = root if file_part.startswith("/") else path.parent  # "/x" is repo-root-relative on GitHub
            yield number, (base / file_part.lstrip("/")).resolve() if file_part else path.resolve(), unquote(anchor)


def line_count(path):
    return len(path.read_text(encoding="utf-8").splitlines()) if path.exists() else 0


def limits(root):
    """Return (limits, errors): the defaults with the project's departures applied; a mistake is an error, never a silent default."""
    path = root / CONFIG
    if not path.exists():
        return dict(LIMITS), []
    try:
        config = tomllib.loads(path.read_text(encoding="utf-8-sig"))
    except (UnicodeDecodeError, tomllib.TOMLDecodeError) as error:
        return dict(LIMITS), [f"{CONFIG}: not valid UTF-8 TOML: {error}"]
    result, errors = dict(LIMITS), [f"{CONFIG}: unknown key {key}; known: lanes, limits, merge, modules, review, source" for key in config
                                    if key not in ("lanes", "limits", "merge", "modules", "review", "source")]
    if "source" in config and not (isinstance(config["source"], str) and SOURCE.fullmatch(config["source"])):
        errors.append(f"{CONFIG}: source must be a full 40-character commit hash")
    modules = config.get("modules", [])  # read by manage.py, which knows the module names; the shape is checked here too
    if not (isinstance(modules, list) and all(isinstance(name, str) and name for name in modules) and len(set(modules)) == len(modules)):
        errors.append(f"{CONFIG}: modules must be a list of distinct module names")
    review = config.get("review", {})  # read by check_pr.py; validated here, where every config mistake is reported
    if not isinstance(review, dict) or set(review) - {"waiver"} or not isinstance(review.get("waiver", True), bool):
        errors.append(f"{CONFIG}: [review] takes only waiver = true or false")
    merge = config.get("merge", {})  # read by manage.py protect and verify; validated here with the rest
    if not isinstance(merge, dict) or set(merge) - {"strict"} or not isinstance(merge.get("strict", True), bool):
        errors.append(f"{CONFIG}: [merge] takes only strict = true or false")
    lanes = config.get("lanes", {})  # read by changes.py, which runs the slow jobs whenever it cannot read them
    if not isinstance(lanes, dict) or set(lanes) - {"light", "code"} or not all(
            isinstance(value, list) and all(isinstance(item, str) and item for item in value) for value in lanes.values()):
        errors.append(f"{CONFIG}: [lanes] takes only light and code, each a list of path patterns")
    configured = config.get("limits", {})
    if not isinstance(configured, dict):
        return result, errors + [f"{CONFIG}: limits must be a table: [limits]"]
    for name, value in configured.items():
        if name not in LIMITS:
            errors.append(f"{CONFIG}: unknown limit {name}; known: {', '.join(sorted(LIMITS))}")
        elif type(value) is not int or value < 1:
            errors.append(f"{CONFIG}: limit {name} must be a positive whole number, not {value!r}")
        else:
            result[name] = value
    return result, errors


def day_of(text):
    try:
        return date.fromisoformat(text)
    except ValueError:
        return None


def check_lessons(path, rel, days, today):
    """Return (errors, warnings) for docs/lessons.md; the format in the retro method is what admits a lesson.

    Raw lines, not prose_lines: the format has no code blocks, and a stray fence must not hide the entries after it."""
    errors, warnings, entries = [], [], []
    lines = path.read_text(encoding="utf-8-sig").splitlines()
    first = LAST_RETRO.match(lines[0]) if lines else None
    if not first or not day_of(first.group(1)):
        errors.append(f"{rel}:1: the first line must be 上次复盘：<YYYY-MM-DD>")
    for number, line in enumerate(lines, 1):
        if line.startswith("## "):
            entries.append((number, line[3:].strip(), {}))
        elif entries and (field := FIELD.match(line)):
            entries[-1][2][field.group(1)] = field.group(2)
    for number, title, fields in entries:
        seen = sorted(set(DAY.findall(fields.get("出现", ""))))  # the same day written twice is one occurrence
        missing = [name for name in FIELDS if not (seen if name == "出现" else fields.get(name))]
        if missing:
            errors.append(f"{rel}:{number}: lesson {title!r} is missing {', '.join(missing)}; 出现 lists dates as YYYY-MM-DD")
            continue
        if not all(map(day_of, seen)):
            errors.append(f"{rel}:{number}: lesson {title!r} has an impossible date in 出现")
            continue
        last = day_of(seen[-1])
        if len(seen) > 1:
            warnings.append(f"{rel}:{number}: lesson {title!r} happened {len(seen)} times; turn it into a check or a rule, then drop it")
        elif (today - last).days > days:
            warnings.append(f"{rel}:{number}: lesson {title!r} not seen for {(today - last).days} days, over {days}; drop it")
    return errors, warnings


def check(root, today=None):
    """Return (errors, warnings)."""
    root = root.resolve()
    rel = lambda path: os.path.relpath(path, root).replace(os.sep, "/")
    limit, errors = limits(root)
    warnings, anchor_cache = [], {}

    for path in sorted(markdown_files(root)):
        for number, target, anchor in links(path, root):
            if not target.exists():
                errors.append(f"{rel(path)}:{number}: broken link to {rel(target)}")
            elif anchor and target.suffix == ".md":
                if target not in anchor_cache:
                    anchor_cache[target] = anchors(target)
                if anchor not in anchor_cache[target]:
                    errors.append(f"{rel(path)}:{number}: no heading for #{anchor} in {rel(target)}")
        if line_count(path) > limit["doc_lines"]:
            warnings.append(f"{rel(path)}: {line_count(path)} lines, over {limit['doc_lines']}; split it by subtopic")

    docs_map = root / "docs" / "README.md"
    mapped = {target for _, target, _ in links(docs_map, root)} if docs_map.exists() else set()
    for path in sorted((root / "docs").rglob("*.md")):
        path = path.resolve()
        if path != docs_map and path not in mapped:
            errors.append(f"{rel(path)}: not listed in docs/README.md")

    loaded = line_count(root / "AGENTS.md") + line_count(root / "CONTRIBUTING.md")
    if loaded > limit["always_loaded_lines"]:
        warnings.append(f"AGENTS.md + CONTRIBUTING.md: {loaded} lines, over {limit['always_loaded_lines']}; both load in every session")
    lessons = root / "docs" / "lessons.md"
    if lessons.exists():
        found = check_lessons(lessons, rel(lessons), limit["lesson_days"], today or date.today())
        errors += found[0]
        warnings += found[1]
    return errors, warnings


def main():
    errors, warnings = check(Path(__file__).resolve().parents[2])
    annotate = os.environ.get("GITHUB_ACTIONS") == "true"  # annotations show findings on the PR, at the line when known
    for level, findings in (("warning", warnings), ("error", errors)):
        for finding in findings:
            if not annotate:
                print(f"warning: {finding}" if level == "warning" else finding)
                continue
            where = LOCATED.match(finding)
            place = f" file={where.group(1)}" + (f",line={where.group(2)}" if where.group(2) else "") if where else ""
            print(f"::{level}{place}::{finding}")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
