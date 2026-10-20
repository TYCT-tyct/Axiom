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
		return errors.New("keyring: cannot inspect envelope inventory")
	}
	defer rows.Close()
	for rows.Next() {
		var schema, table, column string
		if err := rows.Scan(&schema, &table, &column); err != nil {
			return errors.New("keyring: cannot read envelope inventory")
		}
		if !slices.ContainsFunc(r.Stores, func(s EnvelopeStore) bool {
			return s.Schema == schema && s.Table == table && s.Prefix == strings.TrimSuffix(column, "wrapped_dek")
		}) {
			return errors.New("keyring: an envelope store is not registered")
		}
	}
	if rows.Err() != nil {
		return errors.New("keyring: cannot read envelope inventory")
	}
	return nil
}

func (r Rotation) checkReferences(ctx context.Context, tx pg.Tx, ring *Keyring, status KeyStatus) error {
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

func (s EnvelopeStore) checkReferences(ctx context.Context, tx pg.Tx, ring *Keyring, status KeyStatus) error {
	column := s.column("key_version")
	rows, err := tx.Query(ctx, "SELECT DISTINCT "+column+" FROM "+s.table()+" WHERE "+column+" IS NOT NULL")
	if err != nil {
		return errors.New("keyring: cannot read envelope references")
	}
	defer rows.Close()
	var invalid []uint32
	for rows.Next() {
		var version uint32
		if err := rows.Scan(&version); err != nil {
			return errors.New("keyring: cannot read envelope version")
		}
		if _, ok := ring.keys[version]; !ok || !slices.Contains(status.Readable, version) {
			invalid = append(invalid, version)
		}
	}
	if rows.Err() != nil {
		return errors.New("keyring: cannot read envelope references")
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
	// M2 retirement requires stopped writers. The lifecycle lock serializes other rotation commands,
	// but intentionally grants no broader table privilege to the application's column-limited role.
	for _, store := range r.Stores {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM "+store.table()+" WHERE "+store.column("key_version")+" = $1)",
			int64(version)).Scan(&exists); err != nil {
			return errors.New("keyring: cannot read envelope references")
		}
		if exists {
			return fmt.Errorf("keyring: version %d is still referenced", version)
		}
	}
	return nil
}

// RewrapBatch commits at most limit envelopes from one store and its audit entry together. A zero result means
// no store has a remaining old version. The stored version is the checkpoint, so interrupted runs need no cursor.
func (r Rotation) RewrapBatch(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("keyring: batch size must be between 1 and 1000")
	}
	var count int
	err := r.transaction(ctx, func(ctx context.Context, tx pg.Tx) error {
		ring, err := Load(r.Path)
		if err != nil {
			return err
		}
		status, err := checkVersions(ctx, tx, ring)
		if err != nil {
			return err
		}
		if err := r.checkInventory(ctx, tx); err != nil {
			return err
		}
		ring.latest = status.Active
		count, err = r.rewrapStores(ctx, tx, ring, limit)
		return err
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// rewrapStores takes the first store that still has an old envelope, so one batch touches one table and its audit entry
// names the store it came from; an empty store is passed over.
func (r Rotation) rewrapStores(ctx context.Context, tx pg.Tx, ring *Keyring, limit int) (int, error) {
	for _, store := range r.Stores {
		count, err := store.rewrap(ctx, tx, ring, limit)
		if err != nil {
			return 0, err
		}
		if count == 0 {
			continue
		}
		return count, rotationAudit(ctx, tx, "rewrap", ring.latest, map[string]any{"store": store.name(), "count": count})
	}
	return 0, nil
}

type storedEnvelope struct {
	id     string
	sealed Sealed
}

func (s EnvelopeStore) batch(ctx context.Context, tx pg.Tx, active uint32, limit int) ([]storedEnvelope, error) {
	rows, err := tx.Query(ctx, "SELECT "+s.id()+"::text, "+s.column("ciphertext")+", "+s.column("nonce")+
		", "+s.column("wrapped_dek")+", "+s.column("key_version")+" FROM "+s.table()+
		" WHERE "+s.column("key_version")+" <> $1 ORDER BY "+s.id()+" LIMIT $2 FOR UPDATE", int64(active), limit)
	if err != nil {
		return nil, errors.New("keyring: cannot read rewrap batch")
	}
	defer rows.Close()
	var batch []storedEnvelope
	for rows.Next() {
		var row storedEnvelope
		if err := rows.Scan(&row.id, &row.sealed.Ciphertext, &row.sealed.Nonce, &row.sealed.WrappedDEK, &row.sealed.KeyVersion); err != nil {
			return nil, errors.New("keyring: cannot decode envelope")
		}
		batch = append(batch, row)
	}
	if rows.Err() != nil {
		return nil, errors.New("keyring: cannot read rewrap batch")
	}
	return batch, nil
}

func (s EnvelopeStore) rewrap(ctx context.Context, tx pg.Tx, ring *Keyring, limit int) (int, error) {
	batch, err := s.batch(ctx, tx, ring.latest, limit)
	if err != nil {
		return 0, err
	}
	for _, row := range batch {
		sealed, err := ring.Rewrap(row.sealed, Location{Table: s.name(), RowID: row.id, Purpose: s.Purpose})
		if err != nil {
			return 0, fmt.Errorf("keyring: rewrap version %d: %w", row.sealed.KeyVersion, err)
		}
		if _, err := tx.Exec(ctx, "UPDATE "+s.table()+" SET "+s.column("wrapped_dek")+" = $1, "+
			s.column("key_version")+" = $2 WHERE "+s.id()+" = $3", sealed.WrappedDEK, int64(sealed.KeyVersion), row.id); err != nil {
			return 0, errors.New("keyring: cannot update envelope")
		}
	}
	return len(batch), nil
}
