package pg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// refuseInvalidIndexes fails the run while an index in Axiom's schemas is invalid. A CREATE INDEX CONCURRENTLY that
// fails leaves its index behind, named but invalid, and a rerun's IF NOT EXISTS would skip it and record the migration
// as applied without a usable index. It runs under the lock and before any pending migration: the migration that
// failed is not recorded yet, so once the index is dropped the rerun builds it again.
func refuseInvalidIndexes(ctx context.Context, db *sql.DB, modules []Module) error {
	rows, err := db.QueryContext(ctx, `SELECT format('%I.%I', n.nspname, c.relname) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT i.indisvalid AND n.nspname = ANY ($1) ORDER BY 1`, append([]string{queueSchema}, moduleNames(modules)...))
	if err != nil {
		return fmt.Errorf("read the invalid indexes: %w", err)
	}
	var drops []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		drops = append(drops, "DROP INDEX CONCURRENTLY "+name+";")
	}
	if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read the invalid indexes: %w", err)
	}
	if len(drops) > 0 {
		return fmt.Errorf("invalid indexes, left by an interrupted CREATE INDEX CONCURRENTLY: run %s as axiom_owner, "+
			"then run axiom migrate again to build them", strings.Join(drops, " "))
	}
	return nil
}
