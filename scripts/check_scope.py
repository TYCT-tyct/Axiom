"""Fail when code outside internal/platform/pg sets the tenant scope or switches the database role.

Row-level security catches a forgotten condition, not SQL that widens its own transaction's scope, which any role may
do, and a role switched for the session stays on a pooled connection. So, anywhere but internal/platform/pg, in Go and
SQL files, tests and migrations included, committed or new:

- set_config and the scope settings' bare names (scope_platform, scope_sites, scope_accounts) fail wherever they
  appear, in any case. Matching the names alone, not axiom.<name>, keeps quoting, escapes and line breaks around the
  dot from hiding them.
- SET ROLE and RESET ROLE (also SET LOCAL ROLE and SET SESSION ROLE, the role bare or quoted) fail in SQL files and in
  Go string literals, as the database would read them: Go escapes decoded (and carriage returns dropped from raw
  strings), strings read before comments (a comment marker inside a quote, an escape string or a dollar quote is
  text), and comments, white space and line breaks between the words ignored. A statement inside a comment does
  nothing, so it does not fail.

SQL assembled at run time, or from adjacent string literals, is left to review.

usage: python scripts/check_scope.py   (from the repository root; `just scope`)
"""
import ast
import re
import subprocess
import sys

EXEMPT = ":!internal/platform/pg/"

SETTING = re.compile(r"set_config|scope_(platform|sites|accounts)", re.IGNORECASE)
ROLE_SWITCH = re.compile(r'\b(re)?set\s+((local|session)\s+)?"?role\b', re.IGNORECASE)

# What a Go file is made of, up to its strings: comments and rune literals are matched only to be skipped, so a quote
# inside either does not start a string.
GO_TOKEN = re.compile(r"""//[^\n]*|/\*.*?\*/|'(?:\\.|[^'\\\n])+'|(?P<string>"(?:\\.|[^"\\\n])*"|`[^`]*`)""", re.DOTALL)

# What can hide a word in SQL. Outside a comment: line comments, the opener of a block comment, and the quotes inside
# which comment markers are text: escape strings (backslash escapes a quote), plain strings and quoted names (a
# doubled quote is a quote), and dollar-quoted strings ($$...$$, $tag$...$tag$; a tag is not part of a longer name).
# Inside a block comment, which Postgres nests: only its openers and closers.
OUTSIDE_COMMENT = re.compile(
    r"""--[^\n]*|/\*|(?<![\w$])[eE]'(?:\\.|''|[^'\\])*'|'(?:''|[^'])*'|"(?:""|[^"])*\""""
    r"""|(?<![\w$])(?:\$\$(?P<plain>.*?)\$\$|\$(?P<tag>[A-Za-z_]\w*)\$(?P<tagged>.*?)\$(?P=tag)\$)""", re.DOTALL)
INSIDE_COMMENT = re.compile(r"/\*|\*/")


def candidate_files():
    """The Go and SQL files in the working tree outside the exempt directory, new ones included."""
    out = subprocess.run(["git", "grep", "--untracked", "-I", "-l", "-z", "-e", "", "--", "*.go", "*.sql", EXEMPT],
                         stdout=subprocess.PIPE, check=False)
    if out.returncode > 1:  # 1 only means that no file matched
        sys.exit(out.returncode)
    return [name.decode() for name in out.stdout.split(b"\0") if name]


def blank_comments(sql):
    """sql with every comment replaced by spaces, so offsets and line breaks stay where they were."""
    chars, depth, start, pos = list(sql), 0, 0, 0

    def blank(begin, end):
        chars[begin:end] = [c if c == "\n" else " " for c in chars[begin:end]]

    while match := (INSIDE_COMMENT if depth else OUTSIDE_COMMENT).search(sql, pos):
        pos, text = match.end(), match.group()
        if text == "/*":
            start = start if depth else match.start()
            depth += 1
        elif text == "*/":
            depth -= 1
            if depth == 0:
                blank(start, pos)
        elif text.startswith("--"):
            blank(match.start(), pos)
        elif text.startswith("$"):  # its body may be a DO block: comments inside it are comments
            body = "plain" if match.group("plain") is not None else "tagged"
            chars[match.start(body):match.end(body)] = blank_comments(match.group(body))
    return "".join(chars)


def sql_texts(path, source):
    """(line, text) of each piece of SQL in the file: all of an .sql file, each string literal of a Go file."""
    if path.endswith(".sql"):
        return [(1, source)]
    literals = [(token.start(), token.group("string")) for token in GO_TOKEN.finditer(source) if token.group("string")]
    return [(source.count("\n", 0, start) + 1, literal[1:-1].replace("\r", "") if literal[0] == "`" else ast.literal_eval(literal))
            for start, literal in literals]


def main():
    problems = []
    for path in candidate_files():
        with open(path, encoding="utf-8", errors="replace", newline="") as file:
            source = file.read()
        for number, line in enumerate(source.split("\n"), 1):
            if SETTING.search(line):
                problems.append(f"{path}:{number}:{line.rstrip()}")
        for first, sql in sql_texts(path, source):
            for switch in ROLE_SWITCH.finditer(blank_comments(sql)):
                # a Go literal is reported at the line it starts on, since its escapes move the text
                line = first + (sql.count("\n", 0, switch.start()) if path.endswith(".sql") else 0)
                problems.append(f"{path}:{line}:a role switch")
    if problems:
        print("only internal/platform/pg may set the tenant scope or the role:")
        print("\n".join(problems))
        sys.exit(1)


if __name__ == "__main__":
    main()
