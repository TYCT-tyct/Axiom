package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"

	"github.com/jackc/pgx/v5"
)

// The supplier's side of a settlement, run with the suppliers module's own service rather than a double: what an attempt
// cost, the key its charge is reconciled by, and the endpoint it was made on, written in the settlement's own
// transaction; and, when the settlement ends the request, the row that says the request needs nothing more. The upstream
// is the only fake, as in the ledger and cost tests: what is checked is the writing back.

const (
	// The counts the upstream reports over the call's three samplings: the reply's, and the two compactions the request's
	// context management asked for. They are all different and none is zero, so a count recorded in another item's place,
	// or a sampling left out of the row, changes the sums.
	segmentInput, segmentCacheRead, segmentOutput = 130, 70, 60
	compactionInput, compactionCacheRead, compactionOutput = 10, 5, 20
	secondInput, secondCacheRead, secondOutput             = 3, 2, 4
	// everyInput and the two sums below are what the row must hold: the items added over the three samplings.
	everyInput     = segmentInput + compactionInput + secondInput
	everyCacheRead = segmentCacheRead + compactionCacheRead + secondCacheRead
	everyOutput    = segmentOutput + compactionOutput + secondOutput
	// supplierScheduleEvery prices every item these attempts use, in new-api quota, the supplier's native unit: an input
	// token at 0.00001, a cached one at 0.00002, an output token at 0.00004. No rate is zero, so a row that lost an item's
	// count cannot still add up to the right amount.
	supplierScheduleEvery = `{"v":1,"currency":"XXX","rates":{"input":"0.00001","cache_read":"0.00002",` +
		`"output":"0.00004","reasoning":"0.00002"}}`
	// supplierSchedulePartial prices the input and the output only: what the row must then say of the cache reads.
	supplierSchedulePartial = `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00004"}}`
	// saleScheduleEvery prices the same items for the user, so that the call is chargeable without a missing sale price
	// standing in for a missing supplier price.
	saleScheduleEvery = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_read":"0.0000005"}}`
	// everyNative is the supplier's own amount for the three samplings: 143 input, 77 cached and 84 output tokens at the
	// rates above, in quota. It is the schedule's, not the sale price's, which is a different currency and ratio.
	everyNative = "0.00633"
	// everyCredits is that amount in billionths of a credit at the unit value the supplier's balance was confirmed at,
	// 0.001: ten billionths an input token, twenty a cached one, forty an output.
	everyCredits = everyInput*10 + everyCacheRead*20 + everyOutput*40
)

// supplierEveryItemRig is a rig whose alpha-1 entry is a real supplier endpoint priced by the cost schedule given, with
// the receipt an attempt on it leaves and the endpoint's id.
func supplierEveryItemRig(t *testing.T, prices string) (*rig, suppliers.AttemptReceipt, uuid.UUID) {
	t.Helper()
	r := newRig(t, func(o *rigOptions) {
		o.suppliers = true
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name != "alpha-1" {
				return
			}
			schedule, err := catalog.ParseSchedule([]byte(saleScheduleEvery))
			if err != nil {
				t.Fatal(err)
			}
			e.Price.Schedule = schedule
		}
	})
	receipt, endpoint := r.seedSupplierWith(prices)
	e := r.endpoints["alpha-1"]
	e.ID = routing.EndpointID(endpoint)
	r.endpoints["alpha-1"] = e
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, e)
	return r, receipt, endpoint
}

// TestTheRowHoldsEverySegmentAndEveryItemTheUpstreamReported: the call's attempt used three samplings — the reply and the
// two compactions the request asked for — and the supplier's row of it holds the input, the cache read and the output
// added over all three, priced by the supplier's own schedule rather than the sale price, on the endpoint the attempt was
// made on. A row that kept only the reply's counts, that lost an item's count, or that was priced from what the user pays
// fails here.
func TestTheRowHoldsEverySegmentAndEveryItemTheUpstreamReported(t *testing.T) {
	t.Parallel()
	r, receipt, endpoint := supplierEveryItemRig(t, supplierScheduleEvery)
	sample, _ := sample(t, protocol.Messages)
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(receipt)
	up.setScript(serve(beat{data: threeSegments(sample)}))
	a := r.funded(1e12)
	r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))

	call := r.lastCall()
	if charged := r.charged(call.ID); charged == 0 {
		t.Fatal("the call was free, want it charged: the upstream reported output")
	}
	row, ok := r.supplierRow(call.ID)
	if !ok {
		t.Fatal("the attempt left no row on the supplier's side")
	}
	want := map[string]int64{"input": everyInput, "cache_read": everyCacheRead, "output": everyOutput}
	if len(row.items) != len(want) {
		t.Errorf("the row counts %v, want exactly %v: an item the upstream reported is not left out, and none is made up",
			row.items, want)
	}
	for item, count := range want {
		if got := row.items[item]; got != count {
			t.Errorf("the row counts %d %s, want %d over the three samplings: %v", got, item, count, row.items)
		}
	}
	if row.flag != suppliers.CostComplete.String() {
		t.Errorf("the row is flagged %q, want complete: the schedule prices every item the upstream reported", row.flag)
	}
	if row.native != everyNative {
		t.Errorf("the row is %s native, want %s: the supplier's schedule for what was used, not the sale price",
			row.native, everyNative)
	}
	if row.endpoint != endpoint {
		t.Errorf("the row names endpoint %s, want the one the attempt was made on, %s", row.endpoint, endpoint)
	}
	if row.ref != nil {
		t.Errorf("the row has charge ref %q, want none: the fake upstream reported no request id to match a charge by",
			*row.ref)
	}
	// The same amount on the books: the supplier's native cost times the unit value it was confirmed at. That the two
	// agree is the point of writing the row in the settlement's own transaction.
	if booked := r.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'supplier_cost'"); booked != everyCredits {
		t.Errorf("the books hold the attempt at %d, want %d: the supplier's own amount in credits", booked, everyCredits)
	}
	r.ledgerBalanced()
}

// TestAnItemTheSupplierDoesNotPriceIsUnpriced: a supplier that prices the input and the output but not the cache reads
// leaves a row that still says how many it read — the usage is what the upstream reported, whichever part of it could be
// priced — and is flagged unpriced rather than complete, so that neither the amount nor its silence is taken for the
// whole cost.
func TestAnItemTheSupplierDoesNotPriceIsUnpriced(t *testing.T) {
	t.Parallel()
	r, receipt, _ := supplierEveryItemRig(t, supplierSchedulePartial)
	sample, _ := sample(t, protocol.Messages)
	up := withCosts(r, r.billing, r.suppliers)
	up.setReceipt(receipt)
	up.setScript(serve(beat{data: threeSegments(sample)}))
	a := r.funded(1e12)
	r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))

	row, ok := r.supplierRow(r.lastCall().ID)
	if !ok {
		t.Fatal("the attempt left no row on the supplier's side")
	}
	if got := row.items["cache_read"]; got != everyCacheRead {
		t.Errorf("the row counts %d cache reads, want %d: what could not be priced is still what was used", got, everyCacheRead)
	}
	if row.flag != suppliers.CostUnpriced.String() {
		t.Errorf("the row is flagged %q, want %q: the schedule prices no cache read", row.flag, suppliers.CostUnpriced.String())
	}
	// The priced part only: the input and the output, without the cache reads it has no rate for.
	priced := "0.00479"
	if row.native != priced {
		t.Errorf("the row is %s native, want %s: the items the schedule prices, and nothing for the others", row.native, priced)
	}
	r.ledgerBalanced()
}

// TestASettlementTellsSuppliersTheRequestIsOver: the settlement ends the request and tells the supplier so in the same
// transaction, for the call's own tenant, at a time — the row a charge scanner relies on to know nothing more is coming
// for the request. Settling the same outcome again, as a retry after a crash does, changes nothing the supplier holds:
// no second row, no second ending.
func TestASettlementTellsSuppliersTheRequestIsOver(t *testing.T) {
	t.Parallel()
	r, receipt, _ := supplierEveryItemRig(t, supplierScheduleEvery)
	sample, _ := sample(t, protocol.Messages)
	ledger := &recordingLedger{Billing: r.billing}
	up := withCosts(r, ledger, r.suppliers)
	up.setReceipt(receipt)
	up.setScript(serve(beat{data: threeSegments(sample)}))
	a := r.funded(1e12)
	r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))

	call := r.lastCall()
	if state := r.reservationState(call.ID); state != "settled" {
		t.Fatalf("the reservation is %q, want settled", state)
	}
	var site, account, ended string
	if err := r.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT site_id::text, account_id::text, ended_at::text
			FROM suppliers.ended_requests WHERE request_id = $1`, call.ID).Scan(&site, &account, &ended)
	}); err != nil {
		t.Fatalf("the supplier was not told the request is over: %v", err)
	}
	if site != siteID.String() || account != a.id.String() || ended == "" {
		t.Errorf("the request ended for %s/%s at %q, want the call's tenant %s/%s at a time",
			site, account, ended, siteID, a.id)
	}

	before := r.supplierHolds(call.ID)
	outcome, ok := ledger.outcome()
	if !ok {
		t.Fatal("the settlement's outcome was not recorded")
	}
	if _, err := r.billing.Settle(t.Context(), call.Hold, outcome); err != nil {
		t.Fatalf("settle the same outcome again: %v", err)
	}
	if after := r.supplierHolds(call.ID); after != before {
		t.Errorf("the supplier's rows changed when the same outcome was settled twice:\nbefore %s\nafter  %s", before, after)
	}
	r.ledgerBalanced()
}

// threeSegments is the upstream's answer: a reply that reports its input and its cache reads and delivers content, which
// commits the call, then the final usage event, which reports the reply's output and the two compactions the request
// asked for, each a sampling with usage of its own; message_stop ends the stream, so every count is final.
func threeSegments(sample []string) string {
	start := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message",` +
		`"role":"assistant","model":"up-alpha","content":[],"stop_reason":null,"stop_sequence":null,"usage":{` +
		`"input_tokens":` + itoa(segmentInput) + `,"cache_read_input_tokens":` + itoa(segmentCacheRead) + `}}}` + "\n\n"
	final := "event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn",` +
		`"stop_sequence":null},"usage":{"output_tokens":` + itoa(segmentOutput) + `,"iterations":[` +
		`{"type":"compaction","input_tokens":` + itoa(compactionInput) + `,"cache_read_input_tokens":` +
		itoa(compactionCacheRead) + `,"output_tokens":` + itoa(compactionOutput) + `},` +
		`{"type":"compaction","input_tokens":` + itoa(secondInput) + `,"cache_read_input_tokens":` +
		itoa(secondCacheRead) + `,"output_tokens":` + itoa(secondOutput) + `}]}}` + "\n\n"
	return start + events(sample, evBlock, evHello) + final + events(sample, evMsgStop)
}

// supplierRow is one of a request's rows on the supplier's side.
type supplierRow struct {
	seq      int16
	items    map[string]int64 // the counts of each item, added over the attempt's samplings
	flag     string
	native   string  // the amount in the supplier's own unit, as the database stores it
	ref      *string // the supplier's key of the charge, when it reported one
	endpoint uuid.UUID
}

// supplierRow reads a request's only row on the supplier's side, and whether there is one.
func (r *rig) supplierRow(id uuid.UUID) (supplierRow, bool) {
	r.t.Helper()
	var row supplierRow
	var usage []byte
	found := false
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		err := tx.QueryRow(r.t.Context(), `SELECT attempt_seq, usage, cost_flag, estimated_native::text,
			charge_ref, endpoint_id FROM suppliers.attempt_costs WHERE request_id = $1 ORDER BY attempt_seq`, id).
			Scan(&row.seq, &usage, &row.flag, &row.native, &row.ref, &row.endpoint)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		var stored struct {
			Items map[string]int64 `json:"items"`
		}
		if err := jsonx.Unmarshal(usage, &stored); err != nil {
			return err
		}
		row.items = stored.Items
		return nil
	}); err != nil {
		r.t.Fatalf("read the supplier's row of %s: %v", id, err)
	}
	return row, found
}

// supplierHolds is everything the supplier holds of a request, as one string: settling one outcome twice must leave all
// of it as it was.
func (r *rig) supplierHolds(id uuid.UUID) string {
	r.t.Helper()
	var out strings.Builder
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		for _, query := range []string{
			`SELECT to_jsonb(a)::text FROM suppliers.attempt_costs a WHERE a.request_id = $1 ORDER BY a.attempt_seq`,
			`SELECT to_jsonb(e)::text FROM suppliers.ended_requests e WHERE e.request_id = $1`,
			`SELECT to_jsonb(l)::text FROM suppliers.lost_requests l WHERE l.request_id = $1`,
			`SELECT to_jsonb(s)::text FROM suppliers.lost_request_resolutions s WHERE s.request_id = $1`,
		} {
			rows, err := tx.Query(r.t.Context(), query, id)
			if err != nil {
				return err
			}
			for rows.Next() {
				var row string
				if err := rows.Scan(&row); err != nil {
					rows.Close()
					return err
				}
				out.WriteString(row)
				out.WriteByte('\n')
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		r.t.Fatalf("read what the supplier holds of %s: %v", id, err)
	}
	return out.String()
}

// reservationState is a reservation's state.
func (r *rig) reservationState(id uuid.UUID) string {
	r.t.Helper()
	var state string
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(r.t.Context(), `SELECT state FROM billing.reservations WHERE request_id = $1`, id).Scan(&state)
	}); err != nil {
		r.t.Fatal(err)
	}
	return state
}

// recordingLedger is billing, keeping the outcome the settlement was given: what a retry after a crash hands back.
type recordingLedger struct {
	*billing.Billing
	mu       sync.Mutex
	given    billing.Outcome
	recorded bool
}

func (l *recordingLedger) Settle(ctx context.Context, h *billing.Hold, o billing.Outcome) (billing.Final, error) {
	l.mu.Lock()
	l.given, l.recorded = o, true
	l.mu.Unlock()
	return l.Billing.Settle(ctx, h, o)
}

// outcome is the outcome the settlement was given, and whether there was one.
func (l *recordingLedger) outcome() (billing.Outcome, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.given, l.recorded
}
