package pg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// schemas is Axiom's own schemas, one per module, as an SQL array; the names need no quoting.
var schemas = "ARRAY['" + strings.Join(moduleOrder, "', '") + "']"

// audit is something Check refuses: its query lists the offenders, or returns the empty string.
type audit struct{ query, problem string }

// The roles the login can become or inherit from that row-level security cannot hold: a superuser, a role with
// BYPASSRLS, one with REPLICATION (logical decoding reads every tenant's changes, a base backup copies the
// cluster), one that can create roles or databases (and so grant itself more), the tables' owner, the lookup role
// (which reads every tenant's keys, sessions and emails), and the predefined roles that read or write the server's
// files or run programs on it. It starts from session_user, the login itself: a connection that logs in as an
// administrator and switches to axiom_app can switch back.
var loginAudit = audit{
	`SELECT coalesce(string_agg(rolname, ', ' ORDER BY rolname), '') FROM pg_roles
		WHERE pg_has_role(session_user, oid, 'MEMBER')
		AND (rolsuper OR rolbypassrls OR rolreplication OR rolcreaterole OR rolcreatedb OR rolname IN ('axiom_owner',
			'axiom_lookup', 'pg_read_server_files', 'pg_write_server_files', 'pg_execute_server_program'))`,
	"the database login can act as roles that row-level security cannot hold",
}

// schemaAudits check what migrations built.
var schemaAudits = []audit{
	// The audits below name the lookup role, and a database the first migration never ran on lacks it.
	{
		"SELECT CASE WHEN to_regrole('axiom_lookup') IS NULL THEN 'it does not exist' ELSE '' END",
		"the lookup role axiom_lookup (run axiom migrate first)",
	},
	// Every table in Axiom's schemas but the version tables, which hold no tenant's rows, forces row-level security,
	// or its owner reads every row, and has policies: a table without any shows nothing, a step its migration forgot
	// that would otherwise surface as empty results.
	{
		`SELECT coalesce(string_agg(c.oid::regclass::text, ', ' ORDER BY c.oid::regclass::text), '')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ANY (` + schemas + `) AND c.relkind IN ('r', 'p')
		AND NOT (n.nspname = 'platform' AND c.relname IN (SELECT 'goose_' || m FROM unnest(` + schemas + `) m))
		AND NOT (c.relrowsecurity AND c.relforcerowsecurity AND EXISTS (SELECT FROM pg_policy WHERE polrelid = c.oid))`,
		"tables without forced row-level security and policies",
	},
	// A function that runs as its owner reads past the caller's scope, so in Axiom's schemas only the lookup role may
	// own one; a fixed search_path keeps a caller's objects from standing in for the ones it names; and only the
	// application may run it.
	{
		`SELECT coalesce(string_agg(p.oid::regprocedure::text, ', ' ORDER BY p.oid::regprocedure::text), '')
		FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE n.nspname = ANY (` + schemas + `) AND p.prosecdef AND (p.proowner <> 'axiom_lookup'::regrole
			OR p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, pg_temp']
			OR EXISTS (SELECT FROM aclexplode(coalesce(p.proacl, acldefault('f', p.proowner)))
				WHERE privilege_type = 'EXECUTE' AND grantee NOT IN (p.proowner, 'axiom_app'::regrole)))`,
		"SECURITY DEFINER functions not owned by axiom_lookup, with search_path pg_catalog, pg_temp, run only by axiom_app",
	},
	// The lookup role reads only through its functions: it cannot log in or escape row-level security, owns no table,
	// which its owner would read past the policies of, and can create nothing, which would let it own such a table or
	// a function of its choosing.
	{
		`SELECT coalesce(string_agg(problem, ', '), '') FROM (
		SELECT 'it can log in or escape row-level security' FROM pg_roles
			WHERE rolname = 'axiom_lookup' AND (rolcanlogin OR rolsuper OR rolbypassrls)
		UNION ALL SELECT 'it owns ' || oid::regclass::text FROM pg_class WHERE relowner = 'axiom_lookup'::regrole
		UNION ALL SELECT 'it may create objects in ' || nspname FROM pg_namespace
			WHERE has_schema_privilege('axiom_lookup', oid, 'CREATE')
	) problems (problem)`,
		"the lookup role axiom_lookup breaks its limits",
	},
}

// Check refuses a database the node must not run against: one that lacks migrations this program carries, of
// platform or of the modules given, or one that row-level security does not hold (the audits). A database newer than
// the program is fine: migrations only expand until every running version stops using what a later one removes.
func (db *DB) Check(ctx context.Context, modules ...Module) error {
	all, err := ordered(modules)
	if err != nil {
		return err
	}
	return redactConnect("AXIOM_DATABASE_URL", db.Tx(ctx, Scope{}, func(tx Tx) error {
		if err := loginAudit.run(ctx, tx); err != nil {
			return err
		}
		for _, m := range all {
			if err := checkVersion(ctx, tx, m); err != nil {
				return err
			}
		}
		if err := checkQueue(ctx, tx); err != nil {
			return err
		}
		for _, a := range schemaAudits {
			if err := a.run(ctx, tx); err != nil {
				return err
			}
		}
		return nil
	}))
}

// checkQueue refuses a database whose queue tables lack a migration of the River this program carries.
func checkQueue(ctx context.Context, tx Tx) error {
	migrator, err := queueMigrator()
	if err != nil {
		return err
	}
	result, err := migrator.ValidateTx(ctx, tx.tx, nil)
	if err != nil {
		return fmt.Errorf("read the version of the queue (run axiom migrate first): %w", err)
	}
	if !result.OK {
		return fmt.Errorf("the queue's tables are not at the version of this program: run axiom migrate (%s)", strings.Join(result.Messages, "; "))
	}
	return nil
}

func (a audit) run(ctx context.Context, tx Tx) error {
	var offenders string
	if err := tx.QueryRow(ctx, a.query).Scan(&offenders); err != nil {
		return fmt.Errorf("audit the database: %w", err)
	}
	if offenders != "" {
		return errors.New(a.problem + ": " + offenders)
	}
	return nil
}

// checkVersion refuses a database that lacks a migration of m.
func checkVersion(ctx context.Context, tx Tx, m Module) error {
	want, err := latestVersion(m)
	if err != nil {
		return err
	}
	var have int64
	applied := "SELECT coalesce(max(version_id), 0) FROM " + versionTable(m.Name) + " WHERE is_applied"
	if err := tx.QueryRow(ctx, applied).Scan(&have); err != nil {
		return fmt.Errorf("read the version of %s (run axiom migrate first): %w", m.Name, err)
	}
	if have < want {
		return fmt.Errorf("the database has %s migration %d, this program needs %d: run axiom migrate", m.Name, have, want)
	}
	return nil
}

// redactConnect turns a failed connection into an error that names the variable and the kind of failure, but none
// of the URL's parts: pgx reports the user, database and host, and a secret may sit in any of them by mistake.
// Errors other than a failed connection pass unchanged.
func redactConnect(variable string, err error) error {
	if !errors.As(err, new(*pgconn.ConnectError)) {
		return err
	}
	if server, ok := errors.AsType[*pgconn.PgError](err); ok {
		return fmt.Errorf("%s: the server refused the connection (SQLSTATE %s)", variable, server.Code)
	}
	if errors.As(err, new(*net.DNSError)) {
		return fmt.Errorf("%s: the host name does not resolve", variable)
	}
	if network, ok := errors.AsType[*net.OpError](err); ok {
		return fmt.Errorf("%s: cannot reach the server: %w", variable, network.Err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: the server did not finish the connection within its time limit", variable)
	}
	return fmt.Errorf("%s: cannot connect to the database", variable)
}
