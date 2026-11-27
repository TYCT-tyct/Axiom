"""Reject a migration that would lock or rewrite a populated table for long (internal/platform/pg/README.md「在线迁移」).

Only the Up section's statements are read, outside comments, strings and dollar-quoted bodies, so a function or DO
body is not inspected. A table counts as new when the same file creates it. A statement preceded by a comment
`-- migration-safety: <why it is bounded>` is exempt, for the small table whose size a reviewer can confirm.
"""
import re

WAIVER = "migration_safety_waiver"
TOKENS = re.compile(r"--[^\n]*|/\*.*?\*/|'(?:[^']|'')*'|\$(\w*)\$.*?\$\1\$", re.S)
NO_TRANSACTION = re.compile(r"^--\s*\+goose\s+NO\s+TRANSACTION\s*$", re.M | re.I)
DOWN = re.compile(r"^--\s*\+goose\s+Down\b", re.M | re.I)
NAME = r'([\w."]+)'
CREATE_TABLE = re.compile(r"^create (?:unlogged )?table (?:if not exists )?" + NAME)
CREATE_INDEX = re.compile(r"^create (?:unique )?index (concurrently )?(?:if not exists )?(?:(?!on )[\w\"]+ )?on (only )?" + NAME)
ALTER_TABLE = re.compile(r"^alter table (?:if exists )?(?:only )?" + NAME + r" (.*)")
ADD_CONSTRAINT = re.compile(r"^add constraint ([\w\"]+) (?:foreign key|check)\b")
VALIDATE = re.compile(r"^validate constraint ([\w\"]+)$")
UPDATE = re.compile(r"^update (?:only )?" + NAME)
INSERT_SELECT = re.compile(r"^insert into " + NAME + r".*\bselect\b.*\bfrom\b")


def statements(text):
    """The Up section's statements, normalised to lower case and single spaces, each with whether it is waived."""
    up = DOWN.split(text, maxsplit=1)[0]

    def blank(match):
        token = match[0]
        if token.startswith("--") and re.match(r"--\s*migration-safety:\s*\S", token):
            return " " + WAIVER + " "
        return " " if token.startswith(("--", "/*")) else "''"

    for raw in TOKENS.sub(blank, up).split(";"):
        statement = " ".join(raw.lower().split())
        waived = WAIVER in statement
        statement = " ".join(statement.replace(WAIVER, " ").split())
        if statement:
            yield statement, waived


def actions(alteration):
    """An ALTER TABLE's actions, split on the commas outside parentheses."""
    depth, start, parts = 0, 0, []
    for i, char in enumerate(alteration):
        depth += {"(": 1, ")": -1}.get(char, 0)
        if char == "," and depth == 0:
            parts.append(alteration[start:i].strip())
            start = i + 1
    return parts + [alteration[start:].strip()]


def problems(text):
    transactional = not NO_TRANSACTION.search(text)
    parsed = list(statements(text))
    created = {m[1] for s, _ in parsed if (m := CREATE_TABLE.match(s))}
    added = set()
    found = []
    for statement, waived in parsed:
        if waived:
            continue
        if transactional and re.search(r"\bconcurrently\b", statement):
            found.append(f"CONCURRENTLY needs `-- +goose NO TRANSACTION`: {statement[:80]}")
        if (m := CREATE_INDEX.match(statement)) and not m[1] and not m[2] and m[3] not in created:
            found.append(f"index on existing table {m[3]} without CONCURRENTLY (or ON ONLY for a partitioned table)")
        if transactional and (m := UPDATE.match(statement) or INSERT_SELECT.match(statement)) and m[1] not in created:
            found.append(f"backfill of existing table {m[1]} in a transactional migration: batch it in its own commits")
        if not (m := ALTER_TABLE.match(statement)) or m[1] in created:
            continue
        for action in actions(m[2]):
            if c := ADD_CONSTRAINT.match(action):
                added.add(c[1])
                if not action.endswith("not valid"):
                    found.append(f"constraint {c[1]} on existing table {m[1]} without NOT VALID")
            if transactional and (v := VALIDATE.match(action)) and v[1] in added:
                found.append(f"constraint {v[1]} validated in the transaction that added it: validate it in a later migration")
    return found
