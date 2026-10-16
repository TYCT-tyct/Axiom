package suppliers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
)

func (w *world) usageOfCharge(ref string) (usage map[string]any) {
	w.t.Helper()
	var raw []byte
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT usage FROM suppliers.charge_records WHERE charge_ref = $1", ref).Scan(&raw)
	})
	if err := jsonx.Unmarshal(raw, &usage); err != nil {
		w.t.Fatal(err)
	}
	return usage
}

// TestAChargeKeepsWhichItemsTheRecordStated: what the supplier stated and what it left out are stored apart: a cached
// input's counts with nothing reported, an output reported as zero, and the usual record with both items; so that only
// what was stated corrects an estimate.
func TestAChargeKeepsWhichItemsTheRecordStated(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	usage := func(counts map[catalog.Item]int64, unknown ...catalog.UnknownUsage) catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, unknown)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	var both, none, zeroOut catalog.ItemSet
	both.Add(catalog.ItemInput)
	both.Add(catalog.ItemOutput)
	zeroOut.Add(catalog.ItemOutput)
	cases := []struct {
		ref      string
		usage    catalog.Usage
		reported catalog.ItemSet
		items    map[string]any
		stated   []any
	}{
		{"u-cached", usage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 50}), none, map[string]any{"input": 100.0, "output": 50.0}, []any{}},
		{"u-zero", usage(nil), zeroOut, map[string]any{"output": 0.0}, []any{"output"}},
		{"u-both", usage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 50}), both, map[string]any{"input": 100.0, "output": 50.0}, []any{"input", "output"}},
	}
	for _, c := range cases {
		ch := w.charge(c.ref, "23", w.clock.now())
		ch.Usage, ch.Reported = c.usage, c.reported
		w.ingest(ch)
		got := w.usageOfCharge(c.ref)
		if cmp.Diff(c.items, got["items"]) != "" || cmp.Diff(c.stated, got["reported"]) != "" {
			t.Errorf("%s: stored %v, want items %v and reported %v", c.ref, got, c.items, c.stated)
		}
	}
	ch := w.charge("u-unknown", "23", w.clock.now())
	ch.Usage = usage(nil, catalog.UnknownUsage{Field: "usage.x", Value: "1"})
	w.ingest(ch)
	if got := w.usageOfCharge("u-unknown")["unknown"]; cmp.Diff([]any{map[string]any{"field": "usage.x", "value": "1"}}, got) != "" {
		t.Errorf("unknown usage stored as %v", got)
	}
}

// TestChargesNotOnTheWalletPauseTheSupplierAndSettleNothing: a charge drawn on a subscription is kept, the supplier
// stops being routed to, and the charge is never matched, held or told to billing, however old, with an attempt that has
// its key waiting.
func TestChargesNotOnTheWalletPauseTheSupplierAndSettleNothing(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, "sub-ref"))
	sub := w.charge("sub-ref", "23", w.clock.now())
	sub.Funding = adapter.FundingOther
	if n := w.ingest(w.charge("wallet-ref", "5", w.clock.now()), sub); n != 1 {
		t.Fatalf("%d charges not on the wallet reported, want 1", n)
	}
	if status := w.count("SELECT count(*) FROM suppliers.suppliers WHERE id = $1 AND status = 'needs_attention' AND status_reason = 'funding_not_wallet' AND config_gen = 1", w.supplier); status != 1 {
		t.Error("the supplier was not paused")
	}
	w.ingest(sub) // the same page again does not pause it twice
	if w.count("SELECT config_gen FROM suppliers.suppliers WHERE id = $1", w.supplier) != 1 {
		t.Error("a repeated page raised the configuration generation again")
	}
	report := w.reconcile(w.clock.now().Add(48 * time.Hour))
	if report.Matched != 0 || len(report.Held) != 1 {
		t.Errorf("report %+v, want only the wallet charge held", report)
	}
	if got := w.chargeOf("sub-ref"); got.State != "new" || got.Revision != 1 || w.events("sub-ref") != 0 {
		t.Errorf("the subscription charge: %+v, %d events", got, w.events("sub-ref"))
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("a paused supplier's endpoint is still routed to: %v", err)
	}
}

// TestARunDoesAtMostItsBatchesAndSaysThereIsMore: 25 pairs and 25 overdue charges, 10 to a batch and 2 batches to a run:
// the first run takes 20 of each and says so, the second takes the rest.
func TestARunDoesAtMostItsBatchesAndSaysThereIsMore(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	var charges []adapter.Charge
	for i := range 25 {
		ref := fmt.Sprintf("pair-%02d", i)
		w.record(w.attempt(newRequest(), 1, ref))
		charges = append(charges, w.charge(ref, "23", w.clock.now()))
		charges = append(charges, w.charge(fmt.Sprintf("lonely-%02d", i), "5", w.clock.now()))
	}
	w.ingest(charges...)
	cfg := DefaultReconcileConfig()
	cfg.Batch, cfg.MaxBatches = 10, 2
	run := func() (r ReconcileReport) {
		w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
			r, err = Reconcile(ctx, tx, w.clock.now().Add(time.Hour), cfg)
			return err
		})
		return r
	}
	first, second := run(), run()
	if first.Matched != 20 || len(first.Held) != 20 || !first.More {
		t.Errorf("first run: matched %d, held %d, more %v; want 20, 20, true", first.Matched, len(first.Held), first.More)
	}
	if second.Matched != 5 || len(second.Held) != 5 || second.More {
		t.Errorf("second run: matched %d, held %d, more %v; want 5, 5, false", second.Matched, len(second.Held), second.More)
	}
}

// loadHistory fills the tables with twenty thousand finished attempts and charges and twenty of each still waiting.
func (w *world) loadHistory() {
	w.t.Helper()
	supplier, credential, endpoint, schedule := w.supplier.String(), w.credential.String(), w.endpoint.String(), w.sc.String()
	attempts := `INSERT INTO suppliers.attempt_costs (request_id, attempt_seq, supplier_id, credential_id, endpoint_id, cost_schedule_id, charge_ref, usage,
			priced_at, finished_at, cost_flag, estimated_native%s)
		SELECT gen_random_uuid(), 1, '` + supplier + `', '` + credential + `', '` + endpoint + `', '` + schedule + `', %s || g, '{}', now(),
			now() - interval '%s', 'complete', 1%s FROM generate_series(1, %d) g`
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		for _, statement := range []string{
			fmt.Sprintf(attempts, ", charge_record_id, state", "'h'", "3 days", ", gen_random_uuid(), 'matched'", 20000),
			`INSERT INTO suppliers.charge_records (supplier_id, charge_ref, occurred_at, amount_native, usage, funding, upstream_model, state,
					attribution, attempt_request_id, attempt_seq)
				SELECT supplier_id, charge_ref, now() - interval '3 days', 1, '{}', 'wallet', 'm', 'matched', 'attempt', request_id, attempt_seq
				FROM suppliers.attempt_costs WHERE state = 'matched'`,
			`INSERT INTO suppliers.charge_records (supplier_id, charge_ref, occurred_at, amount_native, usage, funding, upstream_model)
				SELECT '` + supplier + `', 'n' || g, now() - interval '2 hours', 1, '{}', 'wallet', 'm' FROM generate_series(1, 20) g`,
			fmt.Sprintf(attempts, "", "'p'", "2 hours", "", 20),
			"ANALYZE suppliers.charge_records", "ANALYZE suppliers.attempt_costs",
		} {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

// explain is the plan the database makes for query, with sequential scans off when scanOff is set.
func (w *world) explain(query string, scanOff bool) string {
	w.t.Helper()
	var plan strings.Builder
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		if scanOff {
			if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, "EXPLAIN "+query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan.WriteString(line + "\n")
		}
		return rows.Err()
	})
	return plan.String()
}

// TestReconciliationQueriesFollowTheBacklogNotTheHistory: with twenty thousand finished rows and a few waiting, the
// planner reads the waiting sets through their partial indexes; for the set of charges not yet matched, a table this
// small is cheaper to read whole, so the check is that the index can serve the query.
func TestReconciliationQueriesFollowTheBacklogNotTheHistory(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.loadHistory()
	cases := []struct {
		index, query string
		scanOff      bool
	}{
		{"charge_records_new", `SELECT c.id FROM suppliers.charge_records c WHERE c.state = 'new' AND c.funding = 'wallet'
			AND c.occurred_at < now() - interval '30 minutes' ORDER BY c.occurred_at, c.id LIMIT 500`, false},
		{"attempt_costs_pending", `SELECT request_id FROM suppliers.attempt_costs WHERE state = 'pending' AND charge_ref IS NOT NULL
			AND finished_at < now() - interval '30 minutes' ORDER BY finished_at, request_id, attempt_seq LIMIT 500`, false},
		{"charge_records_unmatched", `SELECT cr.id FROM suppliers.charge_records cr WHERE cr.state <> 'matched' AND cr.funding = 'wallet'
			ORDER BY cr.id LIMIT 500`, true},
	}
	for _, c := range cases {
		if plan := w.explain(c.query, c.scanOff); !strings.Contains(plan, c.index) {
			t.Errorf("the plan does not use %s:\n%s", c.index, plan)
		}
	}
}

// TestWaitingAttemptsAreAllReportedOverSeveralRuns: more attempts than a batch, none of which ever finds its charge, are
// all reported by runs that go on from where the last stopped, once each; and the run after the last begins again.
func TestWaitingAttemptsAreAllReportedOverSeveralRuns(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	want := map[string]bool{}
	for i := range 25 {
		ref := fmt.Sprintf("waiting-%02d", i)
		w.record(w.attempt(newRequest(), 1, ref))
		want[ref] = true
	}
	cfg := DefaultReconcileConfig()
	cfg.Batch = 10
	seen := map[string]int{}
	var batches []int
	for range 3 {
		var report ReconcileReport
		w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
			report, err = Reconcile(ctx, tx, w.clock.now().Add(time.Hour), cfg)
			return err
		})
		batches = append(batches, len(report.Unmatched))
		for _, u := range report.Unmatched {
			seen[u.Ref]++
		}
		cfg.After = report.Next
	}
	if cmp.Diff([]int{10, 10, 5}, batches) != "" || len(seen) != 25 || cfg.After != (ReconcileCursor{}) {
		t.Errorf("batches %v, %d distinct attempts reported, final cursor %+v; want 10, 10, 5, all 25 and a cursor back at the start", batches, len(seen), cfg.After)
	}
	for ref, n := range seen {
		if n != 1 || !want[ref] {
			t.Errorf("%s reported %d times", ref, n)
		}
	}
}
