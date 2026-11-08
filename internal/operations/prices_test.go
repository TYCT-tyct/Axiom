package operations_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// envelope is the envelope every test sets unless it says otherwise: a margin of 20%, a model moved by 10% a day, 3
// publications of 2 models a day.
func envelope(site uuid.UUID) operations.PriceEnvelope {
	return operations.PriceEnvelope{Site: site, MinMargin: 2000, ModelDailyChange: 1000, DailyChanges: 3, DailyModels: 2}
}

func (db opsDB) setEnvelope(t *testing.T, e operations.PriceEnvelope, rev int64) (operations.PriceEnvelope, error) {
	t.Helper()
	var set operations.PriceEnvelope
	err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		set, err = operations.SetPriceEnvelope(t.Context(), tx, owner, e, rev, "prices")
		return err
	})
	return set, err
}

// pricing is a publication the agent proposes and what it moves.
type pricing struct {
	p operations.Proposal
	r operations.Repricing
}

// publish is the agent publishing model's price, moving it by change and keeping margin, nil for either unmeasured.
func publish(site, model uuid.UUID, change, margin *big.Rat) pricing {
	p := operations.Proposal{
		Credential: uuid.NewV7(), Key: uuid.NewV7(), Tool: "price_publish", Target: operations.Target{Kind: "model", ID: model.String()},
		Params: map[string]any{"candidate_id": uuid.NewV7().String()}, Risk: operations.RiskPrice, Reason: "cost fell",
	}
	return pricing{p, operations.Repricing{Site: site, Model: model, Change: change, Margin: margin, Summary: map[string]any{"model_id": model.String()}}}
}

func (db opsDB) proposePrice(t *testing.T, pr pricing) (operations.Operation, error) {
	t.Helper()
	return db.in(t, func(tx pg.Tx) (operations.Operation, error) {
		op, _, err := operations.ProposePrice(t.Context(), tx, pr.p, pr.r)
		return op, err
	})
}

// seedPrices puts today's use in a site's counters: its publications and models, and model's moves and publications.
func (db opsDB) seedPrices(t *testing.T, site, model uuid.UUID, changes, models, moved, count int) {
	t.Helper()
	if err := pgtest.Run(t.Context(), db.superuser, fmt.Sprintf(`INSERT INTO operations.price_counters
		(site_id, day_start, model_id, change_bps, count, models) VALUES
		('%[1]s', date_trunc('day', now(), 'UTC'), NULL, 0, %[3]d, %[4]d),
		('%[1]s', date_trunc('day', now(), 'UTC'), '%[2]s', %[5]d, %[6]d, 0)`, site, model, changes, models, moved, count)); err != nil {
		t.Fatal(err)
	}
}

// priceCounters is a site's counters today: the site's publications/models, then each model's moves#publications.
func (db opsDB) priceCounters(t *testing.T, site uuid.UUID) string {
	t.Helper()
	return db.text(t, `SELECT coalesce(string_agg(CASE WHEN model_id IS NULL THEN count || '/' || models ELSE change_bps || '#' || count END,
		' ' ORDER BY model_id NULLS FIRST), '') FROM operations.price_counters WHERE site_id = '`+site.String()+`'`)
}

// TestPriceEnvelopeDimensions: each limit of the envelope sends a publication over it to the owner and counts
// nothing; one within every limit is counted in the model's and the site's rows; a site without an envelope changes no
// price. Every case is its own site.
func TestPriceEnvelopeDimensions(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	pct := func(n int64) *big.Rat { return big.NewRat(n, 100) }
	type r = []operations.Reason
	for _, c := range []envelopeCase{
		{"within every limit", 0, 0, 0, 0, pct(5), pct(30), "approved", "1/1 500#1", 0, nil},
		{"at every limit", 2, 1, 500, 0, pct(5), pct(20), "approved", "3/2 1000#1", 0, nil},
		{"a fraction of a basis point counts as one", 0, 0, 999, 0, big.NewRat(1, 30000), pct(30), "approved", "1/1 1000#1", 0, nil},
		{"a moved model is not another model", 1, 2, 200, 1, pct(5), pct(30), "approved", "2/2 700#2", 0, nil},
		{"under the margin", 0, 0, 0, 0, pct(5), big.NewRat(1999, 10000), "pending_approval", "", 0, r{operations.ReasonMinMargin}},
		{"an unknown cost", 0, 0, 0, 0, pct(5), nil, "pending_approval", "", 0, r{operations.ReasonUnknownCost}},
		{"a move no share measures", 0, 0, 0, 0, nil, pct(30), "pending_approval", "", 0, r{operations.ReasonUnmeasuredChange}},
		{"over the model's daily move", 0, 0, 600, 1, pct(5), pct(30), "pending_approval", "", 0, r{operations.ReasonModelDailyChange}},
		{"over the daily publications", 3, 1, 0, 0, pct(1), pct(30), "pending_approval", "", 0, r{operations.ReasonDailyChanges}},
		{"over the daily models", 2, 2, 0, 0, pct(1), pct(30), "pending_approval", "", 0, r{operations.ReasonDailyModels}},
		{"outside every part", 3, 2, 0, 0, nil, nil, "pending_approval", "", 0, r{
			operations.ReasonUnmeasuredChange, operations.ReasonUnknownCost, operations.ReasonDailyChanges, operations.ReasonDailyModels,
		}},
		// The largest envelope holds a move of its own size; a larger one, which the day counts as one more, never fits.
		{"a move the size of the largest envelope", 0, 0, 0, 0, big.NewRat(10_000, 1), pct(30), "approved", "1/1 100000000#1", 100_000_000, nil},
		{"a move beyond the largest envelope", 0, 0, 0, 0, big.NewRat(20_000, 1), pct(30), "pending_approval", "", 100_000_000, r{operations.ReasonModelDailyChange}},
		{"a day that counted a move beyond it", 1, 1, 100_000_001, 1, big.NewRat(0, 1), pct(30), "pending_approval", "", 100_000_000, r{operations.ReasonModelDailyChange}},
	} {
		db.weigh(t, c)
	}
	if _, err := db.proposePrice(t, publish(uuid.NewV7(), uuid.NewV7(), pct(1), pct(30))); !errors.Is(err, operations.ErrNoPriceEnvelope) {
		t.Errorf("a site without an envelope: %v", err)
	}
}

// envelopeCase is today's use of a site's counters, a publication weighed against it, where it should start, what the
// counters should hold after it and the reasons one that waits is shown.
type envelopeCase struct {
	name                          string
	changes, models, moved, count int
	change, margin                *big.Rat
	want, counters                string
	limit                         int64 // the model's daily move, when not the envelope's
	reasons                       []operations.Reason
}

// weigh proposes c's publication in a site of its own: one that waits counts nothing.
func (db opsDB) weigh(t *testing.T, c envelopeCase) {
	t.Helper()
	site, model := uuid.NewV7(), uuid.NewV7()
	e := envelope(site)
	if c.limit != 0 {
		e.ModelDailyChange = c.limit
	}
	if _, err := db.setEnvelope(t, e, 0); err != nil {
		t.Fatal(err)
	}
	want := c.counters
	if c.want == string(operations.StatePending) {
		want = "0/0 0#0"
	}
	if c.changes+c.models+c.moved+c.count > 0 {
		db.seedPrices(t, site, model, c.changes, c.models, c.moved, c.count)
		if c.want == string(operations.StatePending) {
			want = db.priceCounters(t, site)
		}
	}
	op, err := db.proposePrice(t, publish(site, model, c.change, c.margin))
	switch {
	case err != nil:
		t.Fatalf("%s: %v", c.name, err)
	case string(op.State) != c.want:
		t.Errorf("%s: %s, want %s", c.name, op.State, c.want)
	case op.State == operations.StatePending && !db.summaryHas(t, op, "envelope"):
		t.Errorf("%s: the owner is not shown the envelope", c.name)
	}
	expectReasons(t, c.name, op, c.reasons)
	if got := db.priceCounters(t, site); got != want {
		t.Errorf("%s: the counters are %s, want %s", c.name, got, want)
	}
}

// expectReasons checks why op waits, none when it does not.
func expectReasons(t *testing.T, name string, op operations.Operation, want []operations.Reason) {
	t.Helper()
	var got []operations.Reason
	if op.Approval != nil {
		got = op.Approval.Reasons
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s: it waits for %v, want %v", name, got, want)
	}
}

// summaryHas reports whether the summary op's approval shows the owner has the member.
func (db opsDB) summaryHas(t *testing.T, op operations.Operation, member string) bool {
	t.Helper()
	return db.text(t, `SELECT (summary::jsonb ? '`+member+`')::text FROM operations.approvals WHERE id = '`+op.Approval.ID.String()+`'`) == "true"
}

// TestSetPriceEnvelope: an envelope is made at revision 1 and changed from the revision read, with both audited, and
// reads back as changed; an old revision and a limit out of range are refused.
func TestSetPriceEnvelope(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site := uuid.NewV7()
	set, err := db.setEnvelope(t, envelope(site), 0)
	if err != nil || set.Rev != 1 {
		t.Fatalf("made: %+v %v", set, err)
	}
	wider := envelope(site)
	wider.ModelDailyChange = 2000
	if set, err = db.setEnvelope(t, wider, 1); err != nil || set.Rev != 2 || set.ModelDailyChange != 2000 {
		t.Fatalf("changed: %+v %v", set, err)
	}
	if _, err := db.setEnvelope(t, envelope(site), 1); !errors.Is(err, operations.ErrEnvelopeChanged) {
		t.Errorf("an old revision: %v", err)
	}
	if _, err := db.setEnvelope(t, envelope(uuid.NewV7()), 1); !errors.Is(err, operations.ErrEnvelopeChanged) {
		t.Errorf("a revision of an envelope not made: %v", err)
	}
	for _, bad := range []func(*operations.PriceEnvelope){
		func(e *operations.PriceEnvelope) { e.MinMargin = 10001 },
		func(e *operations.PriceEnvelope) { e.MinMargin = -1 },
		func(e *operations.PriceEnvelope) { e.DailyModels = -1 },
		func(e *operations.PriceEnvelope) { e.ModelDailyChange = 100_000_001 },
	} {
		e := envelope(site)
		bad(&e)
		if _, err := db.setEnvelope(t, e, 2); !errors.Is(err, operations.ErrBadEnvelope) {
			t.Errorf("%+v: %v", e, err)
		}
	}
	db.expectEnvelope(t, site, 2, 2000)
	if got := db.text(t, `SELECT string_agg(coalesce(detail->'before'->>'rev', '-') || '>' || (detail->'after'->>'rev'), ' ' ORDER BY id)
		FROM platform.audit_log WHERE action = 'price_envelope.set' AND target_id = '`+site.String()+`'`); got != "->1 1>2" {
		t.Errorf("the audit: %s", got)
	}
}

// expectEnvelope checks that site's is the only envelope, at revision rev with a model's daily move of change.
func (db opsDB) expectEnvelope(t *testing.T, site uuid.UUID, rev, change int64) {
	t.Helper()
	var listed []operations.PriceEnvelope
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		listed, err = operations.PriceEnvelopes(t.Context(), tx)
		return err
	}); err != nil || len(listed) != 1 || listed[0].Site != site || listed[0].Rev != rev || listed[0].ModelDailyChange != change {
		t.Errorf("the envelopes: %+v %v", listed, err)
	}
}

// TestCountPrice: a publication the owner approved counts in the day it runs, a move no share measures as all of the
// old price and one beyond the largest envelope as one more than it, with or without an envelope; and the use of every
// envelope today reads back.
func TestCountPrice(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site, model, other, far := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	if _, err := db.setEnvelope(t, envelope(site), 0); err != nil {
		t.Fatal(err)
	}
	for _, r := range []operations.Repricing{
		{Site: site, Model: model},
		{Site: site, Model: model, Change: big.NewRat(1, 4)},
		{Site: site, Model: other, Change: big.NewRat(1, 100)},
		{Site: site, Model: far, Change: big.NewRat(20_000, 1)},
		{Site: uuid.NewV7(), Model: model, Change: big.NewRat(1, 100)},
	} {
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return operations.CountPrice(t.Context(), tx, r) }); err != nil {
			t.Fatal(err)
		}
	}
	if got := db.text(t, `SELECT count || '/' || models FROM operations.price_counters WHERE site_id = '`+site.String()+`' AND model_id IS NULL`); got != "4/3" {
		t.Errorf("the site's row is %s", got)
	}
	db.expectMoves(t, site, map[uuid.UUID]operations.ModelMoves{
		model: {Model: model, Change: 12500, Count: 2}, other: {Model: other, Change: 100, Count: 1},
		far: {Model: far, Change: 100_000_001, Count: 1}, // a move beyond the largest envelope counts as one more than it
	})
}

// expectMoves checks that the only envelope with use today is site's, with want's moves and their publications.
func (db opsDB) expectMoves(t *testing.T, site uuid.UUID, want map[uuid.UUID]operations.ModelMoves) {
	t.Helper()
	var usage []operations.PriceUsage
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		usage, err = operations.PriceUsages(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var changes int32
	for _, m := range want {
		changes += m.Count
	}
	if len(usage) != 1 || usage[0].Envelope.Site != site || usage[0].Changes != changes || int(usage[0].Models) != len(want) || len(usage[0].Moves) != len(want) {
		t.Fatalf("the use: %+v", usage)
	}
	for _, m := range usage[0].Moves {
		if m != want[m.Model] {
			t.Errorf("the moves of %s: %+v", m.Model, m)
		}
	}
}

// TestPriceRaces: two publications of one model, each within the envelope and over it together, queue on the model's
// counters: the second waits for the first to commit and then goes to the owner.
func TestPriceRaces(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site, model := uuid.NewV7(), uuid.NewV7()
	if _, err := db.setEnvelope(t, envelope(site), 0); err != nil {
		t.Fatal(err)
	}
	six := big.NewRat(6, 100)
	var second operations.Operation
	db.whileWaited(t, func(tx pg.Tx) error {
		first := publish(site, model, six, big.NewRat(1, 2))
		op, _, err := operations.ProposePrice(t.Context(), tx, first.p, first.r)
		if err == nil && op.State != operations.StateApproved {
			err = fmt.Errorf("the first publication: %s", op.State)
		}
		return err
	}, func() (err error) {
		second, err = db.proposePrice(t, publish(site, model, six, big.NewRat(1, 2)))
		return err
	}, nil)
	if second.State != operations.StatePending {
		t.Errorf("the second publication: %s", second.State)
	}
	if got := db.priceCounters(t, site); got != "1/1 600#1" {
		t.Errorf("the counters are %s", got)
	}
}

// TestPriceFailuresReachTheCaller: every price call hands a failing database back to its caller.
func TestPriceFailuresReachTheCaller(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	site := uuid.NewV7()
	if _, err := db.setEnvelope(t, envelope(site), 0); err != nil {
		t.Fatal(err)
	}
	pr := publish(site, uuid.NewV7(), big.NewRat(1, 100), big.NewRat(1, 2))
	calls := map[string]func(ctx context.Context, tx pg.Tx) error{
		"PriceEnvelopes": func(ctx context.Context, tx pg.Tx) error { _, err := operations.PriceEnvelopes(ctx, tx); return err },
		"PriceUsages":    func(ctx context.Context, tx pg.Tx) error { _, err := operations.PriceUsages(ctx, tx); return err },
		"SetPriceEnvelope": func(ctx context.Context, tx pg.Tx) error {
			_, err := operations.SetPriceEnvelope(ctx, tx, owner, envelope(site), 1, "r")
			return err
		},
		"ProposePrice": func(ctx context.Context, tx pg.Tx) error {
			_, _, err := operations.ProposePrice(ctx, tx, pr.p, pr.r)
			return err
		},
		"CountPrice": func(ctx context.Context, tx pg.Tx) error { return operations.CountPrice(ctx, tx, pr.r) },
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range calls {
		if got := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return call(canceled, tx) }); !errors.Is(got, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, got)
		}
	}
}
