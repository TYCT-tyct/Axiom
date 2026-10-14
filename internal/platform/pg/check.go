package pg

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
)

// unsafeRoles lists the roles the login can become or inherit from that row-level security cannot hold: a superuser,
// a role with BYPASSRLS, one with REPLICATION (logical decoding reads every tenant's changes, a base backup copies
// the cluster), one that can create roles or databases (and so grant itself more), the tables' owner, and the
// predefined roles that read or write the server's files or run programs on it. It starts from session_user,
// the login itself: a connection that logs in as an administrator and switches to axiom_app can switch back.
const unsafeRoles = `SELECT coalesce(string_agg(rolname, ', ' ORDER BY rolname), '') FROM pg_roles
	WHERE pg_has_role(session_user, oid, 'MEMBER')
	AND (rolsuper OR rolbypassrls OR rolreplication OR rolcreaterole OR rolcreatedb OR rolname IN
		('axiom_owner', 'pg_read_server_files', 'pg_write_server_files', 'pg_execute_server_program'))`

const appliedVersion = `SELECT coalesce(max(version_id), 0) FROM ` + versionTable + ` WHERE is_applied`

// Check refuses a database the node must not run against: one whose login escapes row-level security, or one
// that lacks migrations this program carries. A database newer than the program is fine: migrations only expand
// until every running version stops using what a later one removes.
func (db *DB) Check(ctx context.Context) error {
	want, err := latestVersion()
	if err != nil {
		return err
	}
	return redactConnect("AXIOM_DATABASE_URL", db.Tx(ctx, Scope{}, func(tx Tx) error {
		var unsafe string
		if err := tx.QueryRow(ctx, unsafeRoles).Scan(&unsafe); err != nil {
			return fmt.Errorf("audit the database role: %w", err)
		}
		if unsafe != "" {
			return errors.New("the database login can act as roles that row-level security cannot hold: " + unsafe)
		}
		var have int64
		if err := tx.QueryRow(ctx, appliedVersion).Scan(&have); err != nil {
			return fmt.Errorf("read the schema version (run axiom migrate first): %w", err)
		}
		if have < want {
			return fmt.Errorf("the database has platform migration %d, this program needs %d: run axiom migrate", have, want)
		}
		return nil
	}))
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
	return fmt.Errorf("%s: cannot connect to the database", variable)
}
