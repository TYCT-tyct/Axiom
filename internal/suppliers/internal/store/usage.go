package store

import (
	"context"
	"time"
	"uuid"
)

// UsageRecordRow is one record from a one-read usage queue, in the adapter's sanitised stored form.
type UsageRecordRow struct {
	ID        uuid.UUID
	RequestID *string
	Record    []byte
}

const insertUsageRecords = `
INSERT INTO suppliers.usage_records (supplier_id, request_id, record, received_at)
SELECT $1::uuid, r.request_id, r.record, r.received_at
FROM unnest($2::text[], $3::jsonb[], $4::timestamptz[]) AS r(request_id, record, received_at)
ON CONFLICT (supplier_id, request_id) WHERE request_id IS NOT NULL DO NOTHING`

// InsertUsageRecords stages a popped batch. A repeated request id is kept once: the supplier's queue is consumed once,
// but a crash after the pop and before the response was stored can make the collector see the same record again.
func InsertUsageRecords(ctx context.Context, db DBTX, supplier uuid.UUID, rows []UsageRecordRow) error {
	if len(rows) == 0 {
		return nil
	}
	request := make([]*string, len(rows))
	record := make([][]byte, len(rows))
	at := make([]time.Time, len(rows))
	for i, r := range rows {
		request[i], record[i], at[i] = r.RequestID, r.Record, time.Now()
	}
	_, err := db.Exec(ctx, insertUsageRecords, supplier, request, record, at)
	return err
}

const listNewUsageRecords = `
SELECT id, request_id, record FROM suppliers.usage_records
WHERE supplier_id = $1 AND state = 'new'
ORDER BY id
LIMIT $2`

// ListNewUsageRecords reads the oldest records still waiting to be parsed and ingested.
func ListNewUsageRecords(ctx context.Context, db DBTX, supplier uuid.UUID, limit int) ([]UsageRecordRow, error) {
	rows, err := db.Query(ctx, listNewUsageRecords, supplier, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRecordRow
	for rows.Next() {
		var r UsageRecordRow
		if err := rows.Scan(&r.ID, &r.RequestID, &r.Record); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const markUsageRecordsRejected = `
UPDATE suppliers.usage_records SET state = 'rejected', reason = $3
WHERE supplier_id = $1 AND id = ANY($2::uuid[]) AND state = 'new'`

// MarkUsageRecordsRejected keeps a record that could not be parsed for a person to inspect.
func MarkUsageRecordsRejected(ctx context.Context, db DBTX, supplier uuid.UUID, ids []uuid.UUID, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Exec(ctx, markUsageRecordsRejected, supplier, ids, reason)
	return err
}

const deleteUsageRecords = `
DELETE FROM suppliers.usage_records WHERE supplier_id = $1 AND id = ANY($2::uuid[])`

// DeleteUsageRecords removes records whose charge rows are committed.
func DeleteUsageRecords(ctx context.Context, db DBTX, supplier uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := db.Exec(ctx, deleteUsageRecords, supplier, ids)
	return err
}

// UsageSupplier is a supplier that has at least one staged usage record waiting to be parsed. Kind is the adapter
// kind stored on the supplier, not a current sync status: a removed or paused supplier still has to replay what it
// staged before it went away.
type UsageSupplier struct {
	ID   uuid.UUID
	Kind string
}

const listUsageSuppliers = `
SELECT s.id, s.kind
FROM suppliers.suppliers s
WHERE EXISTS (
    SELECT 1 FROM suppliers.usage_records u
    WHERE u.supplier_id = s.id AND u.state = 'new'
)
ORDER BY s.id`

// ListUsageSuppliers lists suppliers with staged records, regardless of status or login. It is the offline replay
// path: the parser is the adapter's own, so a removed supplier's already-paid usage can still become a charge.
func ListUsageSuppliers(ctx context.Context, db DBTX) ([]UsageSupplier, error) {
	rows, err := db.Query(ctx, listUsageSuppliers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSupplier
	for rows.Next() {
		var s UsageSupplier
		if err := rows.Scan(&s.ID, &s.Kind); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
