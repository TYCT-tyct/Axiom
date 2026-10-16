package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

var (
	siteA, siteB, accountA = uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	insufficientPrivilege  = "42501"
)

func usageOf100x50(t *testing.T) []catalog.Usage {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 50}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	return []catalog.Usage{u}
}

// attempt is a finished attempt of 100 in and 50 out, which the schedule prices at 22.5 quota.
func (w *world) attempt(id platform.RequestID, seq platform.AttemptSeq, ref string) AttemptRecord {
	w.t.Helper()
	usage := usageOf100x50(w.t)
	receipt := w.receipt(ref)
	cost, err := w.svc.Cost(receipt, usage)
	if err != nil {
		w.t.Fatal(err)
	}
	return AttemptRecord{Request: id, Seq: seq, Receipt: receipt, Usage: usage, Cost: cost, FinishedAt: w.clock.now()}
}

func (w *world) record(attempts ...AttemptRecord) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return RecordAttempts(ctx, tx, attempts) })
}

func (w *world) count(sql string, args ...any) (n int) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return tx.QueryRow(ctx, sql, args...).Scan(&n) })
	return n
}

// TestRecordingAttemptsIsOneStatementAndRepeatable: a request's attempts are written together with their cost and key;
// settling again after a crash, even with different figures, leaves what was written; a wrong attempt number writes
// nothing.
func TestRecordingAttemptsIsOneStatementAndRepeatable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	first := []AttemptRecord{w.attempt(id, 1, ""), w.attempt(id, 2, "ref-2"), w.attempt(id, 3, "ref-3")}
	w.record(first...)
	again := first[1]
	again.Cost.Flag = CostNoUsage
	again.Receipt.ref = "other"
	w.record(again, first[0])
	if n := w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1", uuid.UUID(id)); n != 3 {
		t.Errorf("%d attempts after recording twice, want 3", n)
	}
	var ref, flag, est string
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT charge_ref, cost_flag, estimated_native::text FROM suppliers.attempt_costs WHERE request_id = $1 AND attempt_seq = 2", uuid.UUID(id)).Scan(&ref, &flag, &est)
	})
	if ref != "ref-2" || flag != "complete" || est != "22.5" {
		t.Errorf("attempt 2 is %q %q %q after a second write, want its first values", ref, flag, est)
	}
	if n := w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1 AND charge_ref IS NULL", uuid.UUID(id)); n != 1 {
		t.Errorf("%d attempts without a key, want the one that got none", n)
	}
	bad := newRequest()
	err := w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return RecordAttempts(t.Context(), tx, []AttemptRecord{w.attempt(bad, 1, "x"), w.attempt(bad, 4, "y")})
	})
	if err == nil || w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id = $1", uuid.UUID(bad)) != 0 {
		t.Errorf("an attempt number 4: %v", err)
	}
}

// TestAttemptRecordsFollowTheRequestsTenantAndCannotBeChanged: billing writes them in the request's tenant scope, which
// refuses another tenant's; nobody changes a figure or removes a row, and the only change is setting the match.
func TestAttemptRecordsFollowTheRequestsTenantAndCannotBeChanged(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	a := w.attempt(id, 1, "k")
	a.Site, a.Account = &siteA, &accountA
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordAttempts(t.Context(), tx, []AttemptRecord{a}) }); err != nil {
		t.Fatalf("in the tenant's scope: %v", err)
	}
	b := w.attempt(newRequest(), 1, "k2")
	b.Site, b.Account = &siteB, &accountA
	err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordAttempts(t.Context(), tx, []AttemptRecord{b}) })
	if pg.SQLState(err) != insufficientPrivilege {
		t.Errorf("another tenant's attempt from site A: %v, want row-level security to refuse", err)
	}
	other := pg.NewScope(false, []uuid.UUID{siteB}, nil)
	var seen int
	if err := w.db.Tx(t.Context(), other, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM suppliers.attempt_costs").Scan(&seen)
	}); err != nil || seen != 0 {
		t.Errorf("site B sees %d of site A's attempts: %v", seen, err)
	}
	for _, statement := range []string{
		"UPDATE suppliers.attempt_costs SET estimated_native = 0", "UPDATE suppliers.attempt_costs SET charge_ref = 'x'",
		"UPDATE suppliers.attempt_costs SET cost_flag = 'complete'", "DELETE FROM suppliers.attempt_costs",
		"UPDATE suppliers.charge_records SET amount_native = 0", "DELETE FROM suppliers.charge_records",
		"UPDATE suppliers.charge_events SET payload = '{}'", "DELETE FROM suppliers.charge_events",
		"UPDATE suppliers.cost_schedules SET schedule = '{}'", "DELETE FROM suppliers.cost_schedules",
	} {
		err := w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { _, err := tx.Exec(t.Context(), statement); return err })
		if pg.SQLState(err) != insufficientPrivilege {
			t.Errorf("%s: %v, want permission denied", statement, err)
		}
	}
}

// TestLostRequestsAreOnlyEverAdded: a lost request and its resolution are written once, in the request's tenant scope;
// writing again changes nothing; neither is changed or removed.
func TestLostRequestsAreOnlyEverAdded(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	route := []uuid.UUID{w.endpoint}
	for range 2 {
		if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordLostRequest(t.Context(), tx, id, siteA, accountA, route) }); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return ResolveLostRequest(t.Context(), tx, id, siteA, accountA) }); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"lost_requests", "lost_request_resolutions"} {
		if n := w.count(fmt.Sprintf("SELECT count(*) FROM suppliers.%s", table)); n != 1 {
			t.Errorf("%d rows in %s after writing twice, want 1", n, table)
		}
		for _, statement := range []string{"UPDATE suppliers.%s SET site_id = site_id", "DELETE FROM suppliers.%s"} {
			err := w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
				_, err := tx.Exec(t.Context(), fmt.Sprintf(statement, table))
				return err
			})
			if pg.SQLState(err) != insufficientPrivilege {
				t.Errorf(statement+": %v, want permission denied", table, err)
			}
		}
	}
	err := w.db.Tx(t.Context(), pg.NewScope(false, []uuid.UUID{siteB}, nil), func(tx pg.Tx) error {
		return RecordLostRequest(t.Context(), tx, newRequest(), siteA, accountA, nil)
	})
	if pg.SQLState(err) != insufficientPrivilege {
		t.Errorf("another tenant's lost request: %v, want row-level security to refuse", err)
	}
}
