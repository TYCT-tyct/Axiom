package pg

import (
	"context"
	"errors"
	"fmt"
)

// unsafeRoles lists the roles the login role can become or inherit from that would defeat row-level security:
// a superuser, a role with BYPASSRLS, one that can create roles or databases (and so grant itself more), or the
// owner of the tables.
const unsafeRoles = `SELECT coalesce(string_agg(rolname, ', ' ORDER BY rolname), '') FROM pg_roles
	WHERE pg_has_role(current_user, oid, 'MEMBER')
	AND (rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolname = 'axiom_owner')`

const appliedVersion = `SELECT coalesce(max(version_id), 0) FROM ` + versionTable + ` WHERE is_applied`

// Check refuses a database the node must not run against: one whose login role escapes row-level security, or one
// that lacks migrations this program carries. A database newer than the program is fine: migrations only expand
// until every running version stops using what a later one removes.
func (db *DB) Check(ctx context.Context) error {
	want, err := latestVersion()
	if err != nil {
		return err
	}
	return db.Tx(ctx, Scope{}, func(tx Tx) error {
		var unsafe string
		if err := tx.QueryRow(ctx, unsafeRoles).Scan(&unsafe); err != nil {
			return fmt.Errorf("audit the database role: %w", err)
		}
		if unsafe != "" {
			return errors.New("the database login can act as roles that bypass row-level security: " + unsafe)
		}
		var have int64
		if err := tx.QueryRow(ctx, appliedVersion).Scan(&have); err != nil {
			return fmt.Errorf("read the schema version (run axiom migrate first): %w", err)
		}
		if have < want {
			return fmt.Errorf("the database has platform migration %d, this program needs %d: run axiom migrate", have, want)
		}
		return nil
	})
}
