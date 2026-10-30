package secret

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
)

// EnvelopeStore describes an existing secret.Sealed column group, never supplied by a command-line caller.
// Purpose and the qualified table name are the existing secret.Location AAD; ID is the row's primary key.
type EnvelopeStore struct {
	Schema, Table, ID, Prefix, Purpose string
}

func (s EnvelopeStore) name() string  { return s.Schema + "." + s.Table }
func (s EnvelopeStore) table() string { return pgx.Identifier{s.Schema, s.Table}.Sanitize() }
func (s EnvelopeStore) column(suffix string) string {
	return pgx.Identifier{s.Prefix + suffix}.Sanitize()
}
func (s EnvelopeStore) id() string { return pgx.Identifier{s.ID}.Sanitize() }

// checkInventory fails closed when a new module adds an envelope without registering its AAD.
func (r Rotation) checkInventory(ctx context.Context, tx pg.Tx) error {
	rows, err := tx.Query(ctx, `SELECT table_schema, table_name, column_name FROM information_schema.columns
		WHERE column_name = 'wrapped_dek' OR column_name LIKE '%\_wrapped_dek' ESCAPE '\'`)
	if err != nil {
		return fmt.Errorf("keyring: cannot inspect envelope inventory: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, column string
		if err := rows.Scan(&schema, &table, &column); err != nil {
			return fmt.Errorf("keyring: cannot read envelope inventory: %w", err)
		}
		if !slices.ContainsFunc(r.Stores, func(s EnvelopeStore) bool {
			return s.Schema == schema && s.Table == table && s.Prefix == strings.TrimSuffix(column, "wrapped_dek")
		}) {
			return errors.New("keyring: an envelope store is not registered")
		}
	}
	if rows.Err() != nil {
		return fmt.Errorf("keyring: cannot read envelope inventory: %w", rows.Err())
	}
	// A registered store whose module migration forgot the fence would accept writes retirement cannot see. Fail at
	// startup instead of relying on that migration being deployed correctly.
	for _, store := range r.Stores {
		var fenced bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT FROM pg_catalog.pg_trigger t
			JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2
			  AND t.tgname = 'fence_envelope_key_version'
			  AND t.tgfoid = 'platform.fence_envelope_key_version()'::regprocedure
			  AND t.tgenabled IN ('O', 'A'))`,
			store.Schema, store.Table).Scan(&fenced); err != nil {
			return fmt.Errorf("keyring: cannot inspect retirement fences: %w", err)
		}
		if !fenced {
			return fmt.Errorf("keyring: envelope store %s has no retirement fence", store.table())
		}
	}
	return nil
}

func (r Rotation) checkReferences(ctx context.Context, tx pg.Tx, ring *keySnapshot, status KeyStatus) error {
	if err := r.checkInventory(ctx, tx); err != nil {
		return err
	}
	for _, store := range r.Stores {
		if err := store.checkReferences(ctx, tx, ring, status); err != nil {
			return err
		}
	}
	return nil
}

func (s EnvelopeStore) checkReferences(ctx context.Context, tx pg.Tx, ring *keySnapshot, status KeyStatus) error {
	column := s.column("key_version")
	rows, err := tx.Query(ctx, "SELECT DISTINCT "+column+" FROM "+s.table()+" WHERE "+column+" IS NOT NULL")
	if err != nil {
		return fmt.Errorf("keyring: cannot read envelope references: %w", err)
	}
	defer rows.Close()
	var invalid []uint32
	for rows.Next() {
		var version uint32
		if err := rows.Scan(&version); err != nil {
			return fmt.Errorf("keyring: cannot read envelope version: %w", err)
		}
		if _, ok := ring.keys[version]; !ok || !slices.Contains(status.Readable, version) {
			invalid = append(invalid, version)
		}
	}
	if rows.Err() != nil {
		return fmt.Errorf("keyring: cannot read envelope references: %w", rows.Err())
	}
	if len(invalid) != 0 {
		slices.Sort(invalid)
		return fmt.Errorf("keyring: referenced versions unavailable or retired: %v", invalid)
	}
	return nil
}

func (r Rotation) unreferenced(ctx context.Context, tx pg.Tx, version uint32) error {
	if err := r.checkInventory(ctx, tx); err != nil {
		return err
	}
	// Retirement holds the exclusive fence lock while it runs this check, and every envelope write holds that lock
	// shared to commit, so no writer can appear between the check and the tombstone; one that arrives later is refused
	// by the retirement marker. Retirement still grants no broader table privilege to the application's column-limited
	// role.
	for _, store := range r.Stores {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+store.table()+" WHERE "+store.column("key_version")+" = $1)",
			int64(version)).Scan(&exists); err != nil {
			return fmt.Errorf("keyring: cannot read envelope references: %w", err)
		}
		if exists {
			return fmt.Errorf("%w: version %d", ErrReferenced, version)
		}
	}
	return nil
}

// RewrapCursor is where one run of batches stopped: the store it was rewrapping and the last row it committed there.
// It lives only in that command's memory; a new run starts from the zero value, which is still correct because the
// stored key_version is the checkpoint and the rows before it were committed.
type RewrapCursor struct {
	store int
	after string
}

// RewrapBatch commits at most limit envelopes from one store and its audit entry together, continuing after cursor. A
// zero count means no store has a remaining old version. The stored version is the checkpoint, so an interrupted run
// needs no durable cursor: starting it from the zero value is still correct.
func (r Rotation) RewrapBatch(ctx context.Context, cursor RewrapCursor, limit int) (int, RewrapCursor, error) {
	if limit < 1 || limit > 1000 {
		return 0, RewrapCursor{}, errors.New("keyring: batch size must be between 1 and 1000")
	}
	var count int
	var next RewrapCursor
	err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		snapshot, err := loadSnapshot(r.Path)
		if err != nil {
			return err
		}
		status, err := checkVersions(ctx, tx, snapshot, 0)
		if err != nil {
			return err
		}
		if err := r.checkInventory(ctx, tx); err != nil {
			return err
		}
		snapshot = snapshot.withActive(status.Active)
		count, next, err = r.rewrapStores(ctx, tx, snapshot, cursor, limit)
		// Another run or activation can leave old rows behind the cursor. Confirm completion from the beginning
		// under the lifecycle lock; the extra pass is linear, rather than a rescan on every batch.
		if err == nil && count == 0 && cursor != (RewrapCursor{}) {
			count, next, err = r.rewrapStores(ctx, tx, snapshot, RewrapCursor{}, limit)
		}
		return err
	})
	if err != nil {
		return 0, RewrapCursor{}, err
	}
	return count, next, nil
}

// rewrapStores takes the first store that still has an old envelope, so one batch touches one table and its audit entry
// names the store it came from. Stores before the cursor are exhausted; a store the cursor finished is passed over.
func (r Rotation) rewrapStores(ctx context.Context, tx pg.Tx, ring *keySnapshot, cursor RewrapCursor, limit int) (int, RewrapCursor, error) {
	for i := cursor.store; i < len(r.Stores); i++ {
		store := r.Stores[i]
		after := ""
		if i == cursor.store {
			after = cursor.after
		}
		count, last, err := store.rewrap(ctx, tx, ring, after, limit)
		if err != nil {
			return 0, RewrapCursor{}, err
		}
		if count == 0 {
			continue
		}
		next := RewrapCursor{store: i, after: last}
		if count < limit {
			next = RewrapCursor{store: i + 1}
		}
		return count, next, rotationAudit(ctx, tx, "rewrap", ring.latest, map[string]any{"store": store.name(), "count": count})
	}
	return 0, RewrapCursor{store: len(r.Stores)}, nil
}

type storedEnvelope struct {
	id     string
	sealed Sealed
}

// batch reads the old envelopes after the cursor. The range is on the id column itself, so the primary key index
// carries it instead of rescanning the rows every batch already committed.
func (s EnvelopeStore) batch(ctx context.Context, tx pg.Tx, active uint32, after string, limit int) ([]storedEnvelope, error) {
	query := "SELECT " + s.id() + "::text, " + s.column("ciphertext") + ", " + s.column("nonce") +
		", " + s.column("wrapped_dek") + ", " + s.column("key_version") + " FROM " + s.table() +
		" WHERE " + s.column("key_version") + " <> $1"
	args := []any{int64(active), limit}
	if after != "" {
		query += " AND " + s.id() + " > $3"
		args = append(args, after)
	}
	query += " ORDER BY " + s.id() + " LIMIT $2 FOR UPDATE"
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("keyring: cannot read rewrap batch: %w", err)
	}
	defer rows.Close()
	var batch []storedEnvelope
	for rows.Next() {
		var row storedEnvelope
		if err := rows.Scan(&row.id, &row.sealed.Ciphertext, &row.sealed.Nonce, &row.sealed.WrappedDEK, &row.sealed.KeyVersion); err != nil {
			return nil, fmt.Errorf("keyring: cannot decode envelope: %w", err)
		}
		batch = append(batch, row)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("keyring: cannot read rewrap batch: %w", rows.Err())
	}
	return batch, nil
}

func (s EnvelopeStore) rewrap(ctx context.Context, tx pg.Tx, ring *keySnapshot, after string, limit int) (int, string, error) {
	batch, err := s.batch(ctx, tx, ring.latest, after, limit)
	if err != nil {
		return 0, "", err
	}
	for _, row := range batch {
		sealed, err := ring.rewrap(row.sealed, Location{Table: s.name(), RowID: row.id, Purpose: s.Purpose})
		if err != nil {
			return 0, "", fmt.Errorf("keyring: rewrap version %d: %w", row.sealed.KeyVersion, err)
		}
		if _, err := tx.Exec(ctx, "UPDATE "+s.table()+" SET "+s.column("wrapped_dek")+" = $1, "+
			s.column("key_version")+" = $2 WHERE "+s.id()+" = $3", sealed.WrappedDEK, int64(sealed.KeyVersion), row.id); err != nil {
			return 0, "", fmt.Errorf("keyring: cannot update envelope: %w", err)
		}
	}
	if len(batch) == 0 {
		return 0, "", nil
	}
	return len(batch), batch[len(batch)-1].id, nil
}
