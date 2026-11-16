"""Prove the pre-production squash against its immutable Git source (PostgreSQL 18).

Run with PostgreSQL client tools on PATH and PGHOST/PGPORT/PGUSER pointing at a
throwaway superuser cluster, or --container NAME for a disposable postgres container.
Only the two databases this script creates are removed. Future migrations run on
both sides, so the proof continues to exercise the baseline after the squash lands.
"""
import argparse
import difflib
import re
import subprocess
import uuid
from pathlib import Path

SOURCE = "3ddbe584176d31727340dbfbe2bf1e6a094e384c"
BASELINE = "00001_baseline.sql"
ROOT = Path(__file__).resolve().parents[1]


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True)


def migration_paths(ref):
    return sorted(p for p in git("ls-tree", "-r", "--name-only", ref).splitlines()
                  if re.fullmatch(r"internal/.*/migrations/\d+_[^/]+\.sql", p))


def module_order():
    source = (ROOT / "internal/platform/pg/migrate.go").read_text()
    return re.findall(r'"([a-z]+)"', source.split("var moduleOrder = []string{")[1].split("}")[0])


def module(path):
    return path.split("/")[1]


def command(prefix, program, *args, input=None):
    result = subprocess.run([*prefix, program, *args], input=input, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f"{program}: {result.stderr}")
    return result.stdout


def sql(prefix, db, text):
    return command(prefix, "psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-d", db, input=text)


def controlled_inputs(prefix, db):
    # Keep the same builtin OIDs, so defaults and SQL functions retain their real
    # dependencies. Restore the definitions before dumping; no values are masked.
    functions = ("now()", "statement_timestamp()", "clock_timestamp()", "uuidv7()")
    restore = sql(prefix, db, "SELECT pg_get_functiondef(oid) || ';' FROM pg_proc WHERE oid IN (" +
                  ",".join(f"'pg_catalog.{f}'::regprocedure" for f in functions) + ");")
    sql(prefix, db, """
CREATE SEQUENCE public.comparison_uuid;
GRANT USAGE ON SEQUENCE public.comparison_uuid TO axiom_owner;
CREATE OR REPLACE FUNCTION pg_catalog.uuidv7() RETURNS uuid
LANGUAGE sql VOLATILE PARALLEL SAFE AS
$$ SELECT ('019a862e-0000-7000-8000-' || lpad(nextval('public.comparison_uuid')::text, 12, '0'))::uuid $$;
""" + "\n".join(f"CREATE OR REPLACE FUNCTION pg_catalog.{f} RETURNS timestamptz "
                  "LANGUAGE sql STABLE PARALLEL SAFE AS $$ SELECT '2026-11-16 00:00:00+00'::timestamptz $$;"
                  for f in functions[:3]))
    return restore + "\nDROP SEQUENCE public.comparison_uuid;"


def apply(prefix, db, files):
    order = module_order()
    names = sorted({module(p) for p in files}, key=order.index)
    sql(prefix, db, "".join(f"CREATE SCHEMA {name} AUTHORIZATION axiom_owner;" for name in names))
    restore = controlled_inputs(prefix, db)
    for name in names:
        # psql interprets the SQL and dollar quoting; Goose annotations only select
        # the Up section and whether that file runs in a transaction.
        statements = ["SET ROLE axiom_owner; SET axiom.scope_platform = 'on';"]
        for path, body in sorted(files.items()):
            if module(path) != name:
                continue
            up = body.split("-- +goose Down")[0]
            transactional = "-- +goose NO TRANSACTION" not in up
            statements.extend((["BEGIN;"] if transactional else []) + [up] +
                              (["COMMIT;"] if transactional else []))
        try:
            sql(prefix, db, "\n".join(statements))
        except RuntimeError as error:
            raise RuntimeError(f"{db}/{name}: {error}") from error
    sql(prefix, db, restore)


def normalized_dump(dump):
    # Sort whole pg_dump objects and independent ACL statements. Column order,
    # function bodies, policies, grants, owners and NOT VALID stay byte-identical.
    dump = re.sub(r"^\\(?:un)?restrict .*\n", "", dump, flags=re.M)
    blocks = re.split(r"(?m)^--\n-- Name: ", dump)
    for i, block in enumerate(blocks):
        if "; Type: ACL;" in block.split("\n")[0]:
            header, body = block.split("\n--\n", 1)
            blocks[i] = header + "\n--\n\n" + "\n".join(sorted(body.strip().splitlines()))
    return "\n".join(sorted(block.strip() for block in blocks)) + "\n"


def snapshots(prefix, db):
    schema = command(prefix, "pg_dump", "--schema-only", db)
    # Rows are compared as sorted JSON, including every column and NULL.
    # Include all tables (even empty ones) and all sequence states.
    tables = sql(prefix, db, """SELECT quote_ident(n.nspname)||'.'||quote_ident(c.relname)
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%'
AND c.relkind IN ('r','p','S') ORDER BY 1;""").splitlines()
    query = " UNION ALL ".join(f"SELECT '{table}' || E'\\t' || row_to_json(ROW(t.*))::text FROM ONLY {table} t"
                              for table in tables)
    rows = sql(prefix, db, query + ";").splitlines()
    memberships = sql(prefix, db, """SELECT r.rolname, m.rolname, a.admin_option, a.inherit_option, a.set_option
FROM pg_auth_members a JOIN pg_roles r ON r.oid=a.roleid JOIN pg_roles m ON m.oid=a.member
WHERE r.rolname LIKE 'axiom_%' ORDER BY 1,2;""")
    return {"schema": normalized_dump(schema), "rows": "\n".join(tables + sorted(rows)), "roles": memberships}


def compare(prefix):
    old = {p: git("show", f"{SOURCE}:{p}") for p in migration_paths(SOURCE)}
    current = {str(p.relative_to(ROOT)): p.read_text() for p in ROOT.glob("internal/**/migrations/*.sql")}
    # Later append-only migrations belong on both sides of the fixed baseline proof.
    baseline_paths = {str(Path(p).parent / BASELINE) for p in old}
    old.update({p: body for p, body in current.items() if p not in baseline_paths})
    suffix = uuid.uuid4().hex[:12]
    databases = [f"migration_{suffix}_{side}" for side in ("old", "new")]
    sql(prefix, "postgres", """
DO $$ BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='axiom_owner') THEN
        CREATE ROLE axiom_owner; CREATE ROLE axiom_app; CREATE ROLE axiom_lookup;
        CREATE ROLE axiom_retention_job; CREATE ROLE axiom_retention;
    END IF;
END $$;
GRANT axiom_lookup TO axiom_owner WITH INHERIT FALSE, SET TRUE;
GRANT axiom_retention_job TO axiom_owner WITH ADMIN TRUE, INHERIT TRUE, SET TRUE;
""")
    results = []
    try:
        for db, files in zip(databases, (old, current)):
            sql(prefix, "postgres", f"CREATE DATABASE {db};")
            sql(prefix, "postgres", "REVOKE axiom_retention_job FROM axiom_retention;")
            apply(prefix, db, files)
            results.append(snapshots(prefix, db))
        for kind in results[0]:
            before, after = results[0][kind], results[1][kind]
            if before != after:
                print("".join(difflib.unified_diff(before.splitlines(True), after.splitlines(True),
                                                 fromfile=f"history/{kind}", tofile=f"baseline/{kind}")))
                raise RuntimeError(f"migration {kind} differ")
            print(f"identical {kind}: {len(before.encode())} bytes")
        print(f"{len(migration_paths(SOURCE))} historical migrations == {len(current)} current migrations")
    finally:
        for db in databases:
            sql(prefix, "postgres", f"DROP DATABASE IF EXISTS {db} WITH (FORCE);")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", help="run the PostgreSQL tools via docker exec")
    args = parser.parse_args()
    compare(["docker", "exec", "-i", "-u", "postgres", args.container] if args.container else [])
