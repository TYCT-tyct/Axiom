package suppliers

import (
	"context"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
)

// A request id of each system's own form: new-api's is the id it prints in X-Oneapi-Request-Id; sub2api's is "client:"
// before the id it prints in X-Client-Request-ID. They are opaque strings to reconciliation, equal or not.
const newAPIRef = "20261014045347-sample-newapi-request"

func subRef() string { return "client:" + uuid.NewV7().String() }

func (w *world) charge(ref, amount string, at time.Time) adapter.Charge {
	w.t.Helper()
	u, err := catalog.NewUsage(nil, catalog.ServiceTierStandard, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return adapter.Charge{
		Ref: adapter.ChargeRef(ref), Key: "7", OccurredAt: at, Amount: adapter.Amount(amount), Usage: u,
		Funding: adapter.FundingWallet, Model: "gpt",
	}
}

func (w *world) ingest(charges ...adapter.Charge) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return RecordCharges(ctx, tx, w.supplier, charges) })
}

func (w *world) reconcile(at time.Time) (report ReconcileReport) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		report, err = Reconcile(ctx, tx, at, DefaultReconcileConfig())
		return err
	})
	return report
}

type chargeState struct {
	State, Attribution string
	Revision           int
	Attempt            bool
	Lost               bool
}

func (w *world) chargeOf(ref string) (s chargeState) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		var attribution *string
		var attempt, lost *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT state, attribution, revision, attempt_request_id, lost_request_id FROM suppliers.charge_records
			WHERE supplier_id = $1 AND charge_ref = $2`, w.supplier, ref).Scan(&s.State, &attribution, &s.Revision, &attempt, &lost)
		if attribution != nil {
			s.Attribution = *attribution
		}
		s.Attempt, s.Lost = attempt != nil, lost != nil
		return err
	})
	return s
}

func (w *world) events(ref string) (n int) {
	return w.count(`SELECT count(*) FROM suppliers.charge_events e JOIN suppliers.charge_records c ON c.id = e.charge_record_id
		WHERE c.charge_ref = $1`, ref)
}

// TestChargesAreMatchedToTheirAttemptsByEitherSystemsKey: a charge and the attempt that has its key become one pair,
// whether the key is new-api's or sub2api's, and billing is told once with the verdict.
func TestChargesAreMatchedToTheirAttemptsByEitherSystemsKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	sub := subRef()
	a1, a2 := newRequest(), newRequest()
	w.record(w.attempt(a1, 1, newAPIRef), w.attempt(a2, 1, sub))
	w.ingest(w.charge(newAPIRef, "23", w.clock.now()), w.charge(sub, "22.5", w.clock.now()), w.charge("someone-elses", "9", w.clock.now()))
	report := w.reconcile(w.clock.now())
	if report.Matched != 2 || len(report.Delisted) != 0 || len(report.Over) != 0 {
		t.Fatalf("report %+v, want two matches within the tolerance", report)
	}
	for _, ref := range []string{newAPIRef, sub} {
		if got := w.chargeOf(ref); cmp.Diff(chargeState{State: "matched", Attribution: "attempt", Revision: 2, Attempt: true}, got) != "" {
			t.Errorf("%s: %+v", ref, got)
		}
		if w.events(ref) != 1 {
			t.Errorf("%s: %d events, want 1", ref, w.events(ref))
		}
	}
	if got := w.chargeOf("someone-elses"); got.State != "new" || got.Revision != 1 {
		t.Errorf("a charge nothing of ours has the key of: %+v", got)
	}
	// Run again, and from another node at the same time of day: nothing is matched twice, no event is added.
	if again := w.reconcile(w.clock.now()); again.Matched != 0 {
		t.Errorf("a second run matched %d", again.Matched)
	}
	if w.events(newAPIRef) != 1 {
		t.Error("a second run added an event")
	}
}

// TestACommonKeyMatchesNeither: two attempts with one key cannot be told apart, so the charge is not given to either
// and is held when the grace passes.
func TestACommonKeyMatchesNeither(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, "shared"), w.attempt(newRequest(), 1, "shared"))
	w.ingest(w.charge("shared", "23", w.clock.now()))
	if report := w.reconcile(w.clock.now()); report.Matched != 0 {
		t.Fatalf("matched %d against a key two attempts have", report.Matched)
	}
	later := w.clock.now().Add(time.Hour)
	report := w.reconcile(later)
	if len(report.Held) != 1 || len(report.Unmatched) != 2 {
		t.Errorf("held %d and %d attempts waiting, want 1 and 2", len(report.Held), len(report.Unmatched))
	}
}

// TestOrderDoesNotMatter and a late attempt: the charge may come first; if the grace passes first it is held, books
// nothing, and is still matched when its attempt arrives, as a newer revision that billing takes over the older.
func TestAChargeWaitsForItsAttemptAndIsHeldPastTheGrace(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	id := newRequest()
	w.ingest(w.charge(newAPIRef, "23", w.clock.now()))
	if r := w.reconcile(w.clock.now()); r.Matched != 0 || len(r.Held) != 0 {
		t.Fatalf("a charge with no attempt yet: %+v", r)
	}
	if r := w.reconcile(w.clock.now().Add(31 * time.Minute)); len(r.Held) != 1 {
		t.Fatalf("held %d after the grace, want 1", len(r.Held))
	}
	if got := w.chargeOf(newAPIRef); got.State != "held" || got.Revision != 2 || got.Attribution != "" {
		t.Fatalf("held charge: %+v", got)
	}
	w.record(w.attempt(id, 1, newAPIRef)) // the settlement was only late
	if r := w.reconcile(w.clock.now().Add(32 * time.Minute)); r.Matched != 1 {
		t.Fatalf("matched %d when the attempt came, want 1", r.Matched)
	}
	if got := w.chargeOf(newAPIRef); cmp.Diff(chargeState{State: "matched", Attribution: "attempt", Revision: 3, Attempt: true}, got) != "" {
		t.Errorf("after the late attempt: %+v", got)
	}
	if w.events(newAPIRef) != 2 {
		t.Errorf("%d events, want the held revision and the matched one", w.events(newAPIRef))
	}
}

// TestAnEstimateTooLowDelistsTheEndpointAndOneTooHighOnlyReports: a charge above the estimate and its tolerance means
// the cost we hold is not the cost, so the endpoint is no longer routed to; below, it is only reported; an estimate that
// left something out, or had no usage, is not judged.
func TestAnEstimateTooLowDelistsTheEndpointAndOneTooHighOnlyReports(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, amount string
		flag         CostFlag
		delisted     bool
		over         bool
	}{
		{"exactly the estimate", "22.5", CostComplete, false, false},
		{"the supplier's rounding", "23", CostComplete, false, false},
		{"within one percent", "22.7", CostComplete, false, false},
		{"above the tolerance", "24", CostComplete, true, false},
		{"far above", "400", CostComplete, true, false},
		{"below the tolerance", "21", CostComplete, false, true},
		{"above but the estimate left an item out", "400", CostUnpriced, false, false},
		{"above but there was no usage", "400", CostNoUsage, false, false},
	}
	for _, c := range cases {
		w := newWorld(t, 1)
		a := w.attempt(newRequest(), 1, newAPIRef)
		a.Cost.Flag = c.flag
		w.record(a)
		w.ingest(w.charge(newAPIRef, c.amount, w.clock.now()))
		report := w.reconcile(w.clock.now())
		known := w.count("SELECT count(*) FROM suppliers.endpoints WHERE id = $1 AND cost_state = 'known'", w.endpoint) == 1
		if (len(report.Delisted) == 1) != c.delisted || known == c.delisted || (len(report.Over) == 1) != c.over {
			t.Errorf("%s: delisted %v over %v, endpoint still known %v; want delisted %v over %v", c.name, report.Delisted, report.Over, known, c.delisted, c.over)
		}
		if c.delisted {
			if reason := w.count("SELECT count(*) FROM suppliers.endpoints WHERE id = $1 AND cost_state_reason = 'under_estimated'", w.endpoint); reason != 1 {
				t.Errorf("%s: no reason on the delisted endpoint", c.name)
			}
		}
	}
}

func (w *world) chargeID(ref string) (c uuid.UUID) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "SELECT id FROM suppliers.charge_records WHERE charge_ref = $1", ref).Scan(&c)
	})
	return c
}

// assignToLost gives the held charge to a lost request; assignToNobody settles it as no one's. Both report whether the
// charge was held.
func (w *world) assignToLost(ref string, lost platform.RequestID) (ok bool) {
	w.t.Helper()
	charge := w.chargeID(ref)
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		ok, err = AssignToLostRequest(ctx, tx, charge, uuid.UUID(lost))
		return err
	})
	return ok
}

func (w *world) assignToNobody(ref string) (ok bool) {
	w.t.Helper()
	charge := w.chargeID(ref)
	w.tx(func(ctx context.Context, tx pg.Tx) (err error) {
		ok, err = AssignUnattributed(ctx, tx, charge)
		return err
	})
	return ok
}

// TestHeldChargesAreAttributedByAPerson: a held charge is given to a lost request, or to no one, once; the same charge
// cannot be assigned again, and one that is not held cannot be assigned at all.
func TestHeldChargesAreAttributedByAPerson(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.ingest(w.charge("ref-lost", "23", w.clock.now()), w.charge("ref-nobody", "23", w.clock.now()), w.charge("ref-new", "1", w.clock.now()))
	id := newRequest()
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordLostRequest(t.Context(), tx, id, siteA, accountA, nil) }); err != nil {
		t.Fatal(err)
	}
	if w.assignToNobody("ref-new") {
		t.Error("a charge that is not held was assigned")
	}
	w.reconcile(w.clock.now().Add(time.Hour)) // all three held
	if !w.assignToLost("ref-lost", id) || !w.assignToNobody("ref-nobody") {
		t.Fatal("a held charge was not assigned")
	}
	if w.assignToNobody("ref-nobody") {
		t.Error("a charge already assigned was assigned again")
	}
	if got := w.chargeOf("ref-lost"); cmp.Diff(chargeState{State: "assigned", Attribution: "lost_request", Revision: 3, Lost: true}, got) != "" {
		t.Errorf("assigned to a lost request: %+v", got)
	}
	if got := w.chargeOf("ref-nobody"); cmp.Diff(chargeState{State: "assigned", Attribution: "unattributed", Revision: 3}, got) != "" {
		t.Errorf("unattributed: %+v", got)
	}
}

// TestAnAttemptThatTurnsUpTakesAChargeOverFromAPersonsDecision: a charge given to a lost request, or to no one, is
// matched to the real attempt when it is recorded, in one change that clears the old attribution.
func TestAnAttemptThatTurnsUpTakesAChargeOverFromAPersonsDecision(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.ingest(w.charge("ref-lost", "23", w.clock.now()), w.charge("ref-nobody", "23", w.clock.now()))
	id := newRequest()
	own := pg.NewScope(false, []uuid.UUID{siteA}, nil)
	if err := w.db.Tx(t.Context(), own, func(tx pg.Tx) error { return RecordLostRequest(t.Context(), tx, id, siteA, accountA, nil) }); err != nil {
		t.Fatal(err)
	}
	w.reconcile(w.clock.now().Add(time.Hour))
	if !w.assignToLost("ref-lost", id) || !w.assignToNobody("ref-nobody") {
		t.Fatal("a held charge was not assigned")
	}
	// The node came back: the real attempts are recorded and the charges find them.
	w.record(w.attempt(id, 1, "ref-lost"), w.attempt(newRequest(), 1, "ref-nobody"))
	if r := w.reconcile(w.clock.now().Add(2 * time.Hour)); r.Matched != 2 {
		t.Fatalf("matched %d, want both", r.Matched)
	}
	for _, ref := range []string{"ref-lost", "ref-nobody"} {
		if got := w.chargeOf(ref); cmp.Diff(chargeState{State: "matched", Attribution: "attempt", Revision: 4, Attempt: true}, got) != "" {
			t.Errorf("%s after its attempt: %+v", ref, got)
		}
	}
}

// TestTwoRunsAtOnceMatchEachPairOnce: matchers on two nodes at the same moment give each attempt one charge and each
// charge one attempt, and neither fails.
func TestTwoRunsAtOnceMatchEachPairOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	var attempts []AttemptRecord
	var charges []adapter.Charge
	for i := range 30 {
		ref := subRef()
		attempts = append(attempts, w.attempt(newRequest(), platform.AttemptSeq(1+i%3), ref))
		charges = append(charges, w.charge(ref, "23", w.clock.now()))
	}
	// Attempts of one request must be distinct in (request, seq): spread over requests.
	for i := range attempts {
		attempts[i].Request = newRequest()
	}
	w.record(attempts...)
	w.ingest(charges...)
	var wg sync.WaitGroup
	matched := make([]int, 4)
	errs := make([]error, 4)
	for i := range 4 {
		wg.Go(func() {
			errs[i] = w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
				r, err := Reconcile(t.Context(), tx, w.clock.now(), DefaultReconcileConfig())
				matched[i] = r.Matched
				return err
			})
		})
	}
	wg.Wait()
	total := 0
	for i := range 4 {
		if errs[i] != nil {
			t.Fatalf("run %d: %v", i, errs[i])
		}
		total += matched[i]
	}
	if total != 30 {
		t.Errorf("the runs matched %d pairs between them, want 30", total)
	}
	if n := w.count("SELECT count(*) FROM suppliers.charge_records WHERE state = 'matched' AND revision = 2"); n != 30 {
		t.Errorf("%d charges matched at revision 2, want 30", n)
	}
	if n := w.count("SELECT count(*) FROM suppliers.charge_events"); n != 30 {
		t.Errorf("%d events, want 30", n)
	}
}
