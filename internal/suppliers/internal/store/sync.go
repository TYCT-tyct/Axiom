package store

import (
	"context"
	"time"
	"uuid"
)

// The statements below take several rows in one round trip with unnest over more arrays than sqlc can type, so they are
// written by hand; each is tested against the real tables.

// SyncEventRow is one event of a step of the sync engine.
type SyncEventRow struct{ Kind, Subject, Old, New, Why string }

const insertSyncEvents = `
INSERT INTO suppliers.sync_events (supplier_id, version, seq, kind, subject, old_value, new_value, why)
SELECT $1::uuid, $2::bigint, e.seq - 1, e.kind, e.subject, e.old_value, e.new_value, e.why
FROM unnest($3::text[], $4::text[], $5::text[], $6::text[], $7::text[]) WITH ORDINALITY
    AS e (kind, subject, old_value, new_value, why, seq)`

// InsertSyncEvents writes the events of the step that stored version, in their order.
func InsertSyncEvents(ctx context.Context, db DBTX, supplier uuid.UUID, version int64, rows []SyncEventRow) error {
	var kind, subject, oldValue, newValue, why []string
	for _, r := range rows {
		kind, subject = append(kind, r.Kind), append(subject, r.Subject)
		oldValue, newValue, why = append(oldValue, r.Old), append(newValue, r.New), append(why, r.Why)
	}
	_, err := db.Exec(ctx, insertSyncEvents, supplier, version, kind, subject, oldValue, newValue, why)
	return err
}

// SyncPlanRow is a version of a cost plan.
type SyncPlanRow struct{ Endpoint, Fingerprint, Schedule, Basis string }

const insertSyncPlans = `
INSERT INTO suppliers.sync_plans (supplier_id, version, seq, endpoint, fingerprint, schedule, basis)
SELECT $1::uuid, $2::bigint, p.seq - 1, p.endpoint, p.fingerprint, p.schedule::jsonb, p.basis
FROM unnest($3::text[], $4::text[], $5::text[], $6::text[]) WITH ORDINALITY
    AS p (endpoint, fingerprint, schedule, basis, seq)`

// InsertSyncPlans writes the cost plans of the step that stored version, in their order.
func InsertSyncPlans(ctx context.Context, db DBTX, supplier uuid.UUID, version int64, rows []SyncPlanRow) error {
	var endpoint, fingerprint, schedule, basis []string
	for _, r := range rows {
		endpoint, fingerprint = append(endpoint, r.Endpoint), append(fingerprint, r.Fingerprint)
		schedule, basis = append(schedule, r.Schedule), append(basis, r.Basis)
	}
	_, err := db.Exec(ctx, insertSyncPlans, supplier, version, endpoint, fingerprint, schedule, basis)
	return err
}

// RateBucket is the count of one bucket: Scope is whom the supplier counts for.
type RateBucket struct{ Scope, Name string }

// RateBucketRow is a bucket as it stands. WindowStart and BlockedUntil are zero when there is none.
type RateBucketRow struct {
	RateBucket
	WindowStart, BlockedUntil time.Time
	Used                      int
}

const ensureRateBuckets = `
INSERT INTO suppliers.rate_buckets (scope, name)
SELECT * FROM unnest($1::text[], $2::text[]) AS b (scope, name) ORDER BY b.scope, b.name
ON CONFLICT (scope, name) DO NOTHING`

// Every statement here that makes, locks or changes several buckets does it in key order, so that two requests that
// name some of the same buckets, in any order, wait for each other and cannot deadlock.

// LockRateBucketsFor makes sure the buckets exist and locks them until the transaction ends. It returns them in key
// order.
func LockRateBucketsFor(ctx context.Context, db DBTX, buckets []RateBucket) ([]RateBucketRow, error) {
	scopes, names := splitBuckets(buckets)
	if _, err := db.Exec(ctx, ensureRateBuckets, scopes, names); err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
SELECT r.scope, r.name, r.window_start, r.used, r.blocked_until
FROM suppliers.rate_buckets r
JOIN unnest($1::text[], $2::text[]) AS b (scope, name) ON b.scope = r.scope AND b.name = r.name
ORDER BY r.scope, r.name
FOR UPDATE OF r`, scopes, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RateBucketRow
	for rows.Next() {
		var r RateBucketRow
		var start, blocked *time.Time
		if err := rows.Scan(&r.Scope, &r.Name, &start, &r.Used, &blocked); err != nil {
			return nil, err
		}
		if start != nil {
			r.WindowStart = *start
		}
		if blocked != nil {
			r.BlockedUntil = *blocked
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const blockRateBuckets = `
INSERT INTO suppliers.rate_buckets AS r (scope, name, blocked_until)
SELECT b.scope, b.name, $3::timestamptz FROM unnest($1::text[], $2::text[]) AS b (scope, name) ORDER BY b.scope, b.name
ON CONFLICT (scope, name) DO UPDATE SET blocked_until = greatest(r.blocked_until, excluded.blocked_until)`

// BlockRateBuckets closes the buckets, making those that do not exist, until the time given, or for as long as they
// already are if that is longer.
func BlockRateBuckets(ctx context.Context, db DBTX, buckets []RateBucket, until time.Time) error {
	scopes, names := splitBuckets(buckets)
	_, err := db.Exec(ctx, blockRateBuckets, scopes, names, until)
	return err
}

func splitBuckets(buckets []RateBucket) (scopes, names []string) {
	for _, b := range buckets {
		scopes, names = append(scopes, b.Scope), append(names, b.Name)
	}
	return scopes, names
}
