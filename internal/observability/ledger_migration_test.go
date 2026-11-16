package observability

import (
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

type partitionColumn struct {
	relation string
	present  bool
}

// callLogColumn reports whether ledger_entry_ids is still a live column of every relation in call_log's partition tree,
// parent and default partition included.
func callLogColumn(t *testing.T, db *pg.DB) []partitionColumn {
	t.Helper()
	var relations []partitionColumn
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT c.relname, coalesce(a.attname IS NOT NULL, false)
			FROM pg_partition_tree('observability.call_log') AS p
			JOIN pg_class AS c ON c.oid = p.relid
			LEFT JOIN pg_attribute AS a
				ON a.attrelid = p.relid AND a.attname = 'ledger_entry_ids' AND a.attnum > 0 AND NOT a.attisdropped
			ORDER BY p.level, c.relname`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var relation partitionColumn
			if err := rows.Scan(&relation.relation, &relation.present); err != nil {
				return err
			}
			relations = append(relations, relation)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return relations
}

// TestBaselineOmitsLedgerEntryIDs checks the parent and every partition.
func TestBaselineOmitsLedgerEntryIDs(t *testing.T) {
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	const id = "00000000-0000-4000-8000-000000000204"
	if err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO observability.call_log (id, at, site_id, account_id,
			workspace_id, key_id, ip, request_class, used_tools, has_image, has_file, model, endpoint_id, attempt_count,
			usage, service_tier, price_nano, cost_nano, currency, free_of_charge, latency_ms, first_token_ms,
			consent_version, training_allowed)
		VALUES ('`+id+`', now(), '00000000-0000-4000-8000-0000000000a1', '00000000-0000-4000-8000-0000000000a2',
			'00000000-0000-4000-8000-0000000000a3', '00000000-0000-4000-8000-0000000000a4', '203.0.113.7', 'chat',
			false, false, false, 'claude-opus', '00000000-0000-4000-8000-0000000000a5', 0, '{}', 'standard', 0, 0,
			'USD', true, 0, 0, '2026-10', true)`); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(t.Context(), database.Migrator, Module()); err != nil {
		t.Fatal(err)
	}
	for _, relation := range callLogColumn(t, db) {
		if relation.present {
			t.Errorf("%s has ledger_entry_ids", relation.relation)
		}
	}
	var calls int
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		return tx.QueryRow(t.Context(), `SELECT count(*)::int FROM observability.call_log WHERE id = $1::uuid`, id).Scan(&calls)
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("%d calls survived the repeated migration, want 1", calls)
	}
}
