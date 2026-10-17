package main

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/routing"
)

// silentSuppliers is billing's Suppliers for this test: the settlement's writes to suppliers go nowhere.
type silentSuppliers struct{}

func (silentSuppliers) RecordAttempts(context.Context, pg.Tx, uuid.UUID, []billing.AttemptRecord) error {
	return nil
}

func (silentSuppliers) CommittedAttempt(context.Context, pg.Tx, uuid.UUID) (billing.JournalAttempt, bool, error) {
	return billing.JournalAttempt{}, false, nil
}

func (silentSuppliers) RecordLostRequest(context.Context, pg.Tx, uuid.UUID) error { return nil }

func (silentSuppliers) ResolveLostRequest(context.Context, pg.Tx, uuid.UUID) error { return nil }

func (silentSuppliers) EndRequest(context.Context, pg.Tx, uuid.UUID, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}

// chainFixture is a database with all modules, one funded account, and billing started under a live holder.
type chainFixture struct {
	app           *pg.DB
	platformScope pg.Scope
	scope         pg.Scope
	site          uuid.UUID
	account       uuid.UUID
	workspace     uuid.UUID
	key           uuid.UUID
	currency      platform.Currency
	schedule      catalog.Schedule
	billing       *billing.Billing
}

func newChainFixture(t *testing.T) chainFixture {
	t.Helper()
	database := pgtest.New(t, modules()...)
	app, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	ctx := t.Context()
	f := chainFixture{
		app: app, platformScope: pg.PlatformScope(),
		site: uuid.NewV7(), account: uuid.NewV7(), workspace: uuid.NewV7(), key: uuid.NewV7(),
	}
	f.scope = pg.NewScope(false, nil, []uuid.UUID{f.account})
	if f.currency, err = platform.ParseCurrency("USD"); err != nil {
		t.Fatal(err)
	}
	if f.schedule, err = catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"}}`)); err != nil {
		t.Fatal(err)
	}
	if f.billing, err = billing.Start(ctx, app, f.platformScope, billing.Config{
		HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, CorrectionWindow: time.Hour,
		Logger: slog.New(slog.DiscardHandler),
	}, silentSuppliers{}); err != nil {
		t.Fatal(err)
	}
	if err := app.Tx(ctx, f.scope, func(tx pg.Tx) error {
		return billing.OpenBalance(ctx, tx, f.site, f.account, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Tx(ctx, f.platformScope, func(tx pg.Tx) error {
		return billing.AddCredit(ctx, tx, billing.Credit{
			ID: uuid.NewV7(), Site: f.site, Account: f.account, Amount: 10_000_000,
			Actor: audit.System("chain.test"), Reason: "a funded account for the chain test",
		})
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

// chain is one call after a real settlement, read back through both modules' public reads.
type chain struct {
	request       uuid.UUID
	first, second uuid.UUID
	call          observability.CallRecord
	ledger        []billing.LedgerTransaction
	charged       int64
}

// makeChain reserves and settles two attempts, writes the call with routing's record of the same two tries, and reads
// the call, its attempts and the ledger back under the account's scope.
func makeChain(t *testing.T, f chainFixture) chain {
	t.Helper()
	ctx := t.Context()
	c := chain{request: uuid.NewV7(), first: uuid.NewV7(), second: uuid.NewV7()}
	admitted := billing.Admitted{Scope: f.scope, Site: f.site, Account: f.account, Workspace: f.workspace, Key: f.key}
	var hold *billing.Hold
	if err := f.app.Tx(ctx, f.scope, func(tx pg.Tx) (err error) {
		var kept []int
		hold, kept, err = f.billing.Reserve(ctx, tx, admitted, c.request, billing.Demand{TextBytes: 1000},
			[]billing.Quotable{{Price: f.schedule, Model: billing.Model{ContextLength: 1e12}, MaxOutput: 100}})
		if err == nil && len(kept) != 1 {
			t.Errorf("the reservation kept %v, want the one entry", kept)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	final, err := f.billing.Settle(ctx, hold, billing.Outcome{Attempts: []billing.AttemptResult{
		{Seq: 1, Entry: 0, Cost: 5, Costed: true, Segments: []billing.Segment{segment(t, 50, 0)}, Result: billing.FailedBeforeCommit, Finish: billing.FinishError},
		{Seq: 2, Entry: 0, Cost: 7, Costed: true, Segments: []billing.Segment{segment(t, 50, 10)}, Result: billing.Committed, Finish: billing.FinishStop},
	}})
	if err != nil {
		t.Fatal(err)
	}
	c.charged = final.Charged.CeilAmount()
	c.call = callRecord(t, f, c)
	recorder := observability.NewRecorder(ctx, f.app, f.platformScope, observability.Config{
		QueueSize: 8, BatchSize: 8, FlushInterval: 10 * time.Millisecond,
		RetryBase: time.Millisecond, RetryMax: 10 * time.Millisecond, WriteTimeout: 5 * time.Second,
	}, slog.New(slog.DiscardHandler))
	if err := recorder.Enqueue(c.call); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.app.ReadTx(ctx, f.scope, func(tx pg.Tx) (err error) {
		if c.call, err = observability.GetTrace(ctx, tx, c.request); err != nil {
			return err
		}
		c.ledger, err = billing.LedgerOf(ctx, tx, c.request)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// callRecord is what the gateway would hand the recorder: the request, its two attempts and routing's record.
func callRecord(t *testing.T, f chainFixture, c chain) observability.CallRecord {
	t.Helper()
	route, err := routing.Record{
		Excluded: []routing.Excluded{
			{Endpoint: routing.EndpointID(c.second), Reason: routing.ContextTooSmall},
			{Endpoint: routing.EndpointID(uuid.NewV7()), Reason: routing.NoEgress},
		},
		Route: []routing.EndpointID{routing.EndpointID(c.first), routing.EndpointID(c.second)},
		Attempts: []routing.Attempt{
			{Endpoint: routing.EndpointID(c.first), Egress: "direct", Failed: true, Scope: routing.ScopeEndpoint, Action: routing.NextEntry},
		},
	}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	return observability.CallRecord{
		ID: c.request, At: started, Site: f.site, Account: f.account, Workspace: f.workspace, Key: f.key,
		IP: netip.MustParseAddr("203.0.113.9"), ClientApp: "claude-code", Class: "chat", Language: "en",
		UsedTools: true, Model: "claude-opus", Route: route,
		Attempts: []observability.AttemptRecord{
			{
				Seq: 1, Endpoint: c.first, Supplier: f.key, SourceClass: "relay", Egress: "direct", Outcome: observability.AttemptFailed,
				ErrorKind: "rate_limited", StartedAt: started, FinishedAt: started.Add(100 * time.Millisecond),
			},
			{
				Seq: 2, Endpoint: c.second, Supplier: f.site, SourceClass: "official", Egress: "direct", Outcome: observability.AttemptCommitted,
				StartedAt: started.Add(200 * time.Millisecond), FinishedAt: started.Add(900 * time.Millisecond),
				FirstToken: 300 * time.Millisecond,
			},
		},
		Usage:       map[catalog.Item]int64{catalog.ItemInput: 50, catalog.ItemOutput: 10},
		ServiceTier: catalog.ServiceTierStandard, PriceNano: c.charged, CostNano: 12, Currency: f.currency,
		Latency: 1200 * time.Millisecond, FirstToken: 300 * time.Millisecond, FinishReason: "stop",
		ConsentVersion: "2026-10", TrainingAllowed: true,
	}
}

// TestTraceReadsTheWholeChain: one request ID links a call with two attempts, routing's record of the candidates it
// left out and why, and the ledger transactions a real settlement posts. Observability reads the call and its attempts,
// billing reads the ledger, and the ledger's attempts and charge are the call's (Issue #29「从任意一条调用记录都能串起
// 它的每次尝试、路由过程和账本交易」).
func TestTraceReadsTheWholeChain(t *testing.T) {
	f := newChainFixture(t)
	c := makeChain(t, f)
	checkAttempts(t, c.call.Attempts, c.first, c.second)
	checkRoute(t, c.call.Route)
	if c.call.PriceNano != c.charged || c.call.CostNano != 12 {
		t.Errorf("the call's amounts: price %d (settled %d), cost %d", c.call.PriceNano, c.charged, c.call.CostNano)
	}
	checkLedger(t, c.ledger, c.charged)
	checkNoTraceForAnotherAccount(t, f.app, c.request)
}

// checkAttempts: the call carries both attempts in order, each on the endpoint it went to.
func checkAttempts(t *testing.T, attempts []observability.AttemptRecord, first, second uuid.UUID) {
	t.Helper()
	if len(attempts) != 2 || attempts[0].Seq != 1 || attempts[1].Seq != 2 ||
		attempts[0].Endpoint != first || attempts[1].Endpoint != second ||
		attempts[0].Outcome != observability.AttemptFailed || attempts[1].Outcome != observability.AttemptCommitted {
		t.Errorf("the call's attempts: %+v", attempts)
	}
}

// checkRoute: the call carries routing's record of what it left out and why, and the route it chose.
func checkRoute(t *testing.T, route []byte) {
	t.Helper()
	var record struct {
		Excluded []struct {
			Endpoint string `json:"endpoint"`
			Reason   string `json:"reason"`
		} `json:"excluded"`
		Route []string `json:"route"`
	}
	if err := jsonx.Unmarshal(route, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.Excluded) != 2 || record.Excluded[0].Reason != "context_too_small" ||
		record.Excluded[1].Reason != "no_egress" || len(record.Route) != 2 {
		t.Errorf("the route's record: %+v", record)
	}
}

// checkLedger: the settlement posted the two attempts' costs and the request's charge under the same request ID, in
// that order, and each transaction's entries sum to zero.
func checkLedger(t *testing.T, ledger []billing.LedgerTransaction, charged int64) {
	t.Helper()
	want := []struct {
		kind string
		seq  int32
		cost int64 // the supplier_cost entry, for an attempt's cost
	}{{"attempt_cost", 1, 5}, {"attempt_cost", 2, 7}, {"settle", 0, 0}}
	if len(ledger) != len(want) {
		t.Fatalf("the ledger has %d transactions, want %d: %+v", len(ledger), len(want), ledger)
	}
	for i, w := range want {
		transaction := ledger[i]
		var sum, cost, paid int64
		for _, e := range transaction.Entries {
			sum += e.Amount
			switch e.Book {
			case "supplier_cost":
				cost = e.Amount
			case "user_balance":
				paid = e.Amount
			}
		}
		wantPaid := int64(0)
		if w.kind == "settle" {
			wantPaid = charged
		}
		if transaction.Kind != w.kind || transaction.AttemptSeq != w.seq || sum != 0 || cost != w.cost || paid != wantPaid {
			t.Errorf("ledger %d: kind %q seq %d, entries %+v (sum %d, supplier cost %d, paid %d); want %+v",
				i, transaction.Kind, transaction.AttemptSeq, transaction.Entries, sum, cost, paid, w)
		}
	}
}

// checkNoTraceForAnotherAccount: the trace read follows the transaction's scope, so another account finds no call.
func checkNoTraceForAnotherAccount(t *testing.T, app *pg.DB, request uuid.UUID) {
	t.Helper()
	other := pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})
	err := app.ReadTx(t.Context(), other, func(tx pg.Tx) error {
		_, err := observability.GetTrace(t.Context(), tx, request)
		return err
	})
	if !errors.Is(err, pg.ErrNoRows) {
		t.Errorf("another account's trace read: %v, want no rows", err)
	}
}

// segment is one sampling: its input and output tokens, all reported.
func segment(t *testing.T, in, out int64) billing.Segment {
	t.Helper()
	usage, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: in, catalog.ItemOutput: out},
		catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	return billing.Segment{Usage: usage, Reported: catalog.ItemSet(1<<catalog.ItemInput | 1<<catalog.ItemOutput)}
}
