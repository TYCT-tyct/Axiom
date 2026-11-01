package suppliers

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

var (
	siteA, siteB, accountA = uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	insufficientPrivilege  = "42501"
)

func newRequest() platform.RequestID { return platform.RequestID(uuid.NewV7()) }

// receipt is what an attempt of this world comes back with: its supplier, credential, endpoint and schedule, the
// supplier's charge reference and when it was priced.
func (w *world) receipt(ref string) AttemptReceipt {
	return AttemptReceipt{
		supplier: w.supplier, credential: w.credential, endpoint: w.endpoint, schedule: w.sc,
		ref: ref, pricedAt: w.clock.now(),
	}
}

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

// TestCostChargesOneRequestPerAttempt: a request price is charged once for an executed attempt, whatever the number of
// internal samplings, and the charge is complete even when every token price is zero. The request is not upstream usage,
// so it is added to the schedule's cost after the segments rather than counted in any Usage.
func TestCostChargesOneRequestPerAttempt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		q := store.New()
		id, err := q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota",
			Schedule: []byte(`{"currency":"XXX","rates":{"input":"0","output":"0","request":"1"},"v":1}`),
			Basis:    "published", Fingerprint: "request",
		})
		if err != nil {
			return err
		}
		w.sc = id
		return q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &id})
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	usage := func(counts map[catalog.Item]int64) catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	for name, tc := range map[string]struct {
		usage []catalog.Usage
		flag  CostFlag
	}{
		"one sampling":  {[]catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 50})}, CostComplete},
		"two samplings": {[]catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100}), usage(map[catalog.Item]int64{catalog.ItemOutput: 50})}, CostComplete},
		"no usage":      {nil, CostComplete},
	} {
		cost, err := w.svc.Cost(receipt, tc.usage)
		text, derr := decimal(cost.Native)
		if err != nil || derr != nil || text != "1" || cost.Flag != tc.flag {
			t.Errorf("%s: %q, %v, %v; want one request, %v", name, text, cost.Flag, err, tc.flag)
		}
	}
}

// TestRequestCostReconcilesWithin: the supplier charges exactly the request price and the attempt's cost now includes
// that request, so reconciliation matches instead of judging the cost an under-estimate and delisting the endpoint.
func TestRequestCostReconcilesWithin(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		q := store.New()
		id, err := q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
			SupplierID: w.supplier, EndpointID: w.endpoint, NativeUnit: "quota",
			Schedule: []byte(`{"currency":"XXX","rates":{"input":"0","output":"0","request":"1"},"v":1}`),
			Basis:    "published", Fingerprint: "request",
		})
		if err != nil {
			return err
		}
		w.sc = id
		return q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &id})
	})
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.record(w.attempt(newRequest(), 1, newAPIRef))
	w.ingest(w.charge(newAPIRef, "1", w.clock.now()))
	var logs bytes.Buffer
	w.runReconciliation(w.reconciliation(&logs))
	w.checkReconciled("within", "", logs.String())
}

// A stored attempt shows the prompt the upstream reported beside the items: 100 prompt tokens that are 80 cache reads and
// 80 cache writes would otherwise read as 160 input.
func TestUsageOfKeepsPhysicalInput(t *testing.T) {
	t.Parallel()
	overlap, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemCacheRead: 80, catalog.ItemCacheWrite: 80}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	physical, err := overlap.WithPhysicalInput(100)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := overlap.Recount(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if zero, err = zero.WithPhysicalInput(0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		segments []catalog.Usage
		want     string
	}{
		{"one sampling", []catalog.Usage{physical}, `{"items":{"cache_read":80,"cache_write":80},"physical_input":100,"tier":"standard","unknown":[]}`},
		{"two samplings add up", []catalog.Usage{physical, physical}, `{"items":{"cache_read":160,"cache_write":160},"physical_input":200,"tier":"standard","unknown":[]}`},
		{"no total reported", []catalog.Usage{overlap}, `{"items":{"cache_read":80,"cache_write":80},"tier":"standard","unknown":[]}`},
		{"an explicit zero is not absent", []catalog.Usage{zero}, `{"items":{},"physical_input":0,"tier":"standard","unknown":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := usageOf(tc.segments)
			if err != nil || string(got) != tc.want {
				t.Errorf("usageOf = %s, %v; want %s", got, err, tc.want)
			}
		})
	}
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
	a.Site, a.Account = siteA, accountA
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordAttempts(t.Context(), tx, []AttemptRecord{a}) }); err != nil {
		t.Fatalf("in the tenant's scope: %v", err)
	}
	b := w.attempt(newRequest(), 1, "k2")
	b.Site, b.Account = siteB, accountA
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
		if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return ResolveLostRequest(t.Context(), tx, id) }); err != nil {
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

// TestOnlyALostRequestCanBeResolved: a resolution takes its tenant from the lost request, so one for a request never
// recorded as lost writes nothing, and says so rather than reporting a resolution that is not there.
func TestOnlyALostRequestCanBeResolved(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return ResolveLostRequest(t.Context(), tx, newRequest()) })
	if err == nil {
		t.Error("resolving a request that was never recorded as lost succeeded, want an error")
	}
	if n := w.count("SELECT count(*) FROM suppliers.lost_request_resolutions"); n != 0 {
		t.Errorf("%d resolutions written for a request that was never lost", n)
	}
}
