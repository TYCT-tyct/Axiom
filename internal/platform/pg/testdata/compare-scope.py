"""Compare RLS rewrites on PostgreSQL 18.6; run under the machine's heavy-test lock.

Only the experiment's container is created/removed. Full JSON plans go to --output;
stdout reports the median of three EXPLAIN execution times, never a timing gate.
"""
import argparse
import hashlib
import json
import pathlib
import statistics
import subprocess
import time
import uuid


def run(output):
    name = "axiom-rls-comparison-" + uuid.uuid4().hex[:8]

    def sql(statement):
        result = subprocess.run(
            ["docker", "exec", "-i", name, "psql", "-XqAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres"],
            input=statement, text=True, capture_output=True, check=True,
        )
        return result.stdout

    subprocess.run([
        "docker", "run", "--rm", "-d", "--name", name, "--cpus", "2", "--memory", "1g",
        "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:18.6-alpine",
    ], check=True, capture_output=True)
    try:
        # initdb's temporary server listens on a socket; only the final server accepts TCP.
        for _ in range(300):
            ready = subprocess.run([
                "docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres",
            ], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("PostgreSQL did not accept TCP connections")
        sql("""
            CREATE ROLE app;
            CREATE SCHEMA platform;
            GRANT USAGE ON SCHEMA platform TO app;
            CREATE TABLE history(site_id uuid, account_id uuid, seq bigint, payload text);
            INSERT INTO history SELECT md5('site')::uuid,
                CASE WHEN n <= 100000 THEN md5('target')::uuid
                    ELSE md5(((n-100001)/1000)::text)::uuid END,
                n, repeat('x',80) FROM generate_series(1,2000000) n;
            CREATE INDEX history_account_seq ON history(account_id,seq);
            ANALYZE history;
            ALTER TABLE history ENABLE ROW LEVEL SECURITY;
            ALTER TABLE history FORCE ROW LEVEL SECURITY;
            GRANT SELECT ON history TO app;
            CREATE FUNCTION platform.flag() RETURNS boolean LANGUAGE sql STABLE PARALLEL SAFE
                RETURN coalesce(current_setting('axiom.scope_platform',true),'')='on';
            CREATE FUNCTION platform.sites() RETURNS uuid[] LANGUAGE sql STABLE PARALLEL SAFE
                RETURN coalesce(nullif(current_setting('axiom.scope_sites',true),'')::uuid[],'{}');
            CREATE FUNCTION platform.accounts() RETURNS uuid[] LANGUAGE sql STABLE PARALLEL SAFE
                RETURN coalesce(nullif(current_setting('axiom.scope_accounts',true),'')::uuid[],'{}');
        """)
        variants = {
            "old": "platform.flag() OR site_id=ANY(platform.sites()) OR account_id=ANY(platform.accounts())",
            "stable_bool": "coalesce(nullif(current_setting('axiom.scope_platform',true),'')::boolean,false) "
                           "OR site_id=ANY(platform.sites()) OR account_id=ANY(platform.accounts())",
            "policy_initplan": "(SELECT platform.flag()) OR site_id=ANY((SELECT platform.sites())::uuid[]) "
                               "OR account_id=ANY((SELECT platform.accounts())::uuid[])",
        }
        def accounts(names):
            return "{" + ",".join(hashlib.md5(n.encode()).hexdigest() for n in names) + "}"

        scopes = [
            ("platform", "on", "{}"), ("account", "off", accounts(["target"])),
            ("filtered", "off", "{}"), ("many_accounts", "off", accounts(["target"] + list(map(str, range(15))))),
        ]
        pages = [
            ("count", "SELECT count(*) FROM history"),
            ("first", "SELECT * FROM history WHERE account_id=md5('target')::uuid "
                      "AND seq<2100000 ORDER BY seq DESC LIMIT 101"),
            ("deep", "SELECT * FROM history WHERE account_id=md5('target')::uuid "
                     "AND seq<25000 ORDER BY seq DESC LIMIT 101"),
        ]
        results = []
        print(sql("SHOW server_version;"), flush=True)
        for variant, expression in variants.items():
            sql("DROP POLICY IF EXISTS tenant ON history; CREATE POLICY tenant ON history USING (" + expression + ");")
            for scope, flag, allowed in scopes:
                prefix = f"""BEGIN; SET LOCAL ROLE app;
                    SELECT set_config('axiom.scope_platform','{flag}',true),
                        set_config('axiom.scope_sites','{{}}',true),
                        set_config('axiom.scope_accounts','{allowed}',true);
                    SET LOCAL max_parallel_workers_per_gather=0; """
                for page, query in pages:
                    plans = []
                    for _ in range(3):
                        raw = sql(prefix + "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) " + query + "; ROLLBACK;")
                        plans.append(json.loads(raw[raw.index("[\n"):])[0])
                    plan = plans[-1]
                    root = plan["Plan"]
                    result = dict(
                        variant=variant, scope=scope, page=page,
                        ms=round(statistics.median(p["Execution Time"] for p in plans), 3),
                        blocks=root["Shared Hit Blocks"] + root["Shared Read Blocks"], plan=plan,
                    )
                    results.append(result)
                    print(json.dumps({k: v for k, v in result.items() if k != "plan"}), flush=True)
        output.write_text(json.dumps(results, indent=2) + "\n")
    finally:
        subprocess.run(["docker", "rm", "-f", name], check=True, capture_output=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    run(parser.parse_args().output)
