package operations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// RiskPrice is the risk class of a sale price the agent publishes (docs/domains/operations.md「售价的累计变动上限」).
const RiskPrice Risk = "price"

var (
	// ErrNoPriceEnvelope is a site the owner set no price envelope for: the agent changes none of its prices.
	ErrNoPriceEnvelope = errors.New("operations: no price envelope for this site, so the agent changes no price")
	// ErrEnvelopeChanged is an envelope changed since the caller read it.
	ErrEnvelopeChanged = errors.New("operations: the price envelope changed since it was read")
	// ErrBadEnvelope is an envelope with a margin outside 0 to 10000 basis points, a negative limit or a daily move
	// over maxModelDailyChange.
	ErrBadEnvelope = errors.New("operations: an envelope's margin is 0 to 10000 basis points, its daily move at most 100000000 and each limit at least 0")
)

// PriceEnvelope is how far the agent may move a site's sale prices without the owner: every price it publishes keeps
// MinMargin of its cost, a model's prices move at most ModelDailyChange in a UTC day, up and down added together, and
// a day changes at most DailyChanges publications of DailyModels models. Ratios are basis points.
type PriceEnvelope struct {
	Site                      uuid.UUID
	MinMargin                 int32
	ModelDailyChange          int64
	DailyChanges, DailyModels int32
	Rev                       int64
	Updated                   time.Time
}

// Repricing is what publishing a model's price moves, as the server computed it: Change is the largest share any of
// its prices moves, nil when no share measures it (a first price, a price added or lost, zero to non-zero); Margin is
// the least share of its price any cost leaves, nil when the cost is unknown. Summary is what the owner is shown when
// it waits for approval.
type Repricing struct {
	Site, Model    uuid.UUID
	Change, Margin *big.Rat
	Summary        map[string]any
}

// unmeasuredMove is what a move no share measures counts as: all of the old price. It is never within the envelope,
// and once the owner approves it the model's day holds at least this much.
const unmeasuredMove = 10_000

// maxModelDailyChange is the largest daily move an envelope may allow a model, a million percent. A move counts in
// the day as itself, or as one more than this when it is larger, so the day's row never overflows and a model that
// moved further is never again within any envelope that day.
const maxModelDailyChange = 100_000_000

// changeBPS is r's move in basis points, rounded up so the envelope never counts less than the move, and exact however
// large.
func (r Repricing) changeBPS() *big.Int {
	if r.Change == nil {
		return big.NewInt(unmeasuredMove)
	}
	n := new(big.Rat).Mul(r.Change, big.NewRat(10_000, 1))
	bps := new(big.Int).Quo(n.Num(), n.Denom())
	if !n.IsInt() {
		bps.Add(bps, big.NewInt(1))
	}
	return bps
}

// counted is what the day's counter records of r's move: the move, or maxModelDailyChange + 1 for one beyond it.
func (r Repricing) counted() int64 {
	if bps := r.changeBPS(); bps.Cmp(big.NewInt(maxModelDailyChange)) <= 0 {
		return bps.Int64()
	}
	return maxModelDailyChange + 1
}

// Detail is what an operation records of r: its move and margin in basis points, the margin rounded down, each nil
// when it is not measured; a move beyond maxModelDailyChange is shown as one more than it.
func (r Repricing) Detail() map[string]any {
	d := map[string]any{"change_bps": nil, "margin_bps": nil}
	if r.Change != nil {
		d["change_bps"] = r.counted()
	}
	if r.Margin != nil {
		n := new(big.Rat).Mul(r.Margin, big.NewRat(10_000, 1))
		bps := new(big.Int).Div(n.Num(), n.Denom()) // Euclidean: rounds a negative margin down too
		if bps.IsInt64() {
			d["margin_bps"] = bps.Int64()
		}
	}
	return d
}

// The reasons a publication waits, each a part of the envelope it does not keep within (envelopeDetail's members):
// a move no share measures or a cost that is unknown, which the envelope cannot vouch for; a margin under
// min_margin_bps; the model's move today past model_daily_change_bps; a day that has published daily_changes prices,
// or moved daily_models models and this is another.
const (
	ReasonUnmeasuredChange Reason = "envelope_unmeasured_change"
	ReasonUnknownCost      Reason = "envelope_unknown_cost"
	ReasonMinMargin        Reason = "envelope_min_margin"
	ReasonModelDailyChange Reason = "envelope_model_daily_change"
	ReasonDailyChanges     Reason = "envelope_daily_changes"
	ReasonDailyModels      Reason = "envelope_daily_models"
)

// outside decides r against e and what the day's site and model rows hold: the parts of e it does not keep within,
// none when the envelope covers it.
func (e PriceEnvelope) outside(r Repricing, site, model OperationsPriceCounter) []Reason {
	var reasons []Reason
	for _, part := range []struct {
		reason Reason
		out    bool
	}{
		{ReasonUnmeasuredChange, r.Change == nil},
		{ReasonUnknownCost, r.Margin == nil},
		{ReasonMinMargin, r.Margin != nil && r.Margin.Cmp(big.NewRat(int64(e.MinMargin), 10_000)) < 0},
		{ReasonModelDailyChange, r.Change != nil &&
			new(big.Int).Add(big.NewInt(model.ChangeBps), r.changeBPS()).Cmp(big.NewInt(e.ModelDailyChange)) > 0},
		{ReasonDailyChanges, site.Count >= e.DailyChanges},
		{ReasonDailyModels, model.Count == 0 && site.Models >= e.DailyModels},
	} {
		if part.out {
			reasons = append(reasons, part.reason)
		}
	}
	return reasons
}

// envelopeDetail is what the owner is shown of the envelope a publication is weighed against.
func (e PriceEnvelope) envelopeDetail(r Repricing, site, model OperationsPriceCounter) map[string]any {
	d := r.Detail()
	maps.Copy(d, map[string]any{
		"rev": e.Rev, "min_margin_bps": e.MinMargin, "model_daily_change_bps": e.ModelDailyChange,
		"model_change_used": model.ChangeBps, "daily_changes": e.DailyChanges, "daily_changes_used": site.Count,
		"daily_models": e.DailyModels, "daily_models_used": site.Models, "model_moved_today": model.Count > 0,
	})
	return d
}

// PriceEnvelopes reads every envelope.
func PriceEnvelopes(ctx context.Context, tx pg.Tx) ([]PriceEnvelope, error) {
	rows, err := New().PriceEnvelopes(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the price envelopes: %w", err)
	}
	envelopes := make([]PriceEnvelope, len(rows))
	for i, r := range rows {
		envelopes[i] = envelopeOf(r)
	}
	return envelopes, nil
}

// SetPriceEnvelope makes or changes e's site's envelope as actor and records it. rev is the revision the caller read,
// 0 for an envelope not made yet; another is ErrEnvelopeChanged.
func SetPriceEnvelope(ctx context.Context, tx pg.Tx, actor audit.Actor, e PriceEnvelope, rev int64, reason string) (PriceEnvelope, error) {
	if e.MinMargin < 0 || e.MinMargin > 10_000 || e.ModelDailyChange > maxModelDailyChange ||
		min(e.ModelDailyChange, int64(e.DailyChanges), int64(e.DailyModels)) < 0 {
		return PriceEnvelope{}, ErrBadEnvelope
	}
	q := New()
	found, err := q.LockPriceEnvelopeForUpdate(ctx, tx, e.Site)
	if err != nil {
		return PriceEnvelope{}, fmt.Errorf("lock the price envelope: %w", err)
	}
	if len(found) == 0 && rev != 0 || len(found) > 0 && found[0].Rev != rev {
		return PriceEnvelope{}, ErrEnvelopeChanged
	}
	var before map[string]any
	var row OperationsPriceEnvelope
	if len(found) == 0 {
		row, err = q.InsertPriceEnvelope(ctx, tx, InsertPriceEnvelopeParams{
			SiteID: e.Site, MinMarginBps: e.MinMargin, ModelDailyChangeBps: e.ModelDailyChange, DailyChanges: e.DailyChanges, DailyModels: e.DailyModels,
		})
	} else {
		before = envelopeOf(found[0]).auditDetail()
		row, err = q.UpdatePriceEnvelope(ctx, tx, UpdatePriceEnvelopeParams{
			SiteID: e.Site, MinMarginBps: e.MinMargin, ModelDailyChangeBps: e.ModelDailyChange, DailyChanges: e.DailyChanges, DailyModels: e.DailyModels,
		})
	}
	if err != nil {
		return PriceEnvelope{}, fmt.Errorf("write the price envelope: %w", err)
	}
	set := envelopeOf(row)
	return set, audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "price_envelope.set", Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "price_envelope", ID: e.Site.String()},
		Detail: map[string]any{"before": before, "after": set.auditDetail()},
	})
}

func (e PriceEnvelope) auditDetail() map[string]any {
	return map[string]any{
		"min_margin_bps": e.MinMargin, "model_daily_change_bps": e.ModelDailyChange, "daily_changes": e.DailyChanges,
		"daily_models": e.DailyModels, "rev": e.Rev,
	}
}

func envelopeOf(r OperationsPriceEnvelope) PriceEnvelope {
	return PriceEnvelope{
		Site: r.SiteID, MinMargin: r.MinMarginBps, ModelDailyChange: r.ModelDailyChangeBps, DailyChanges: r.DailyChanges,
		DailyModels: r.DailyModels, Rev: r.Rev, Updated: r.UpdatedAt,
	}
}

// lockEnvelope holds site's envelope for share until tx ends: a change of it waits for tx.
func lockEnvelope(ctx context.Context, tx pg.Tx, site uuid.UUID) (PriceEnvelope, error) {
	rows, err := New().LockPriceEnvelope(ctx, tx, site)
	if err != nil {
		return PriceEnvelope{}, fmt.Errorf("lock the price envelope: %w", err)
	}
	if len(rows) == 0 {
		return PriceEnvelope{}, ErrNoPriceEnvelope
	}
	return envelopeOf(rows[0]), nil
}

// holdPriceCounters makes and locks the site's and the model's rows of the day, in that order.
func holdPriceCounters(ctx context.Context, tx pg.Tx, site, model uuid.UUID, day time.Time) (siteRow, modelRow OperationsPriceCounter, err error) {
	q := New()
	if err := q.EnsurePriceCounters(ctx, tx, EnsurePriceCountersParams{SiteID: site, DayStart: day, ModelID: model}); err != nil {
		return siteRow, modelRow, fmt.Errorf("make the price counters: %w", err)
	}
	rows, err := q.LockPriceCounters(ctx, tx, LockPriceCountersParams{SiteID: site, DayStart: day, ModelID: model})
	if err != nil || len(rows) != 2 {
		return siteRow, modelRow, fmt.Errorf("lock the price counters (%d): %w", len(rows), err)
	}
	return rows[0], rows[1], nil
}

// ProposePrice is Propose for publishing a sale price: it locks r's site's envelope and the day's site and model
// counters, and asks the owner for a publication outside the envelope; one within it the caller publishes in tx and
// settles, and it is counted here. A site without an envelope is ErrNoPriceEnvelope; a UTC day that ended while the
// proposal waited for a lock is ErrWindowEnded.
func ProposePrice(ctx context.Context, tx pg.Tx, p Proposal, r Repricing) (Operation, bool, error) {
	e, err := lockEnvelope(ctx, tx, r.Site)
	if err != nil {
		return Operation{}, false, err
	}
	q := New()
	w, err := q.CapWindows(ctx, tx)
	if err != nil {
		return Operation{}, false, fmt.Errorf("read the day: %w", err)
	}
	site, model, err := holdPriceCounters(ctx, tx, r.Site, r.Model, w.DayStart)
	if err != nil {
		return Operation{}, false, err
	}
	if reasons := e.outside(r, site, model); len(reasons) > 0 {
		p.Summary, p.Reasons = maps.Clone(r.Summary), reasons
		p.Summary["envelope"] = e.envelopeDetail(r, site, model)
	}
	op, replayed, err := Propose(ctx, tx, p)
	if err != nil || replayed || op.State != StateApproved {
		return op, replayed, err
	}
	if err := countPrice(ctx, tx, r, w.DayStart, model); err != nil {
		return Operation{}, false, err
	}
	// Every lock is taken by now; a day that ended meanwhile is not where today's move is counted.
	ended, err := q.WindowEnded(ctx, tx, w.DayStart)
	if err != nil {
		return Operation{}, false, fmt.Errorf("check the day: %w", err)
	}
	if ended {
		return Operation{}, false, ErrWindowEnded
	}
	return op, false, nil
}

// CountPrice counts r, a publication the owner approved, in the day it runs on: approved moves fill the envelope like
// the agent's own. It locks the envelope, when the site has one, before the counters, as ProposePrice does.
func CountPrice(ctx context.Context, tx pg.Tx, r Repricing) error {
	if _, err := lockEnvelope(ctx, tx, r.Site); err != nil && !errors.Is(err, ErrNoPriceEnvelope) {
		return err
	}
	w, err := New().CapWindows(ctx, tx)
	if err != nil {
		return fmt.Errorf("read the day: %w", err)
	}
	_, model, err := holdPriceCounters(ctx, tx, r.Site, r.Model, w.DayStart)
	if err != nil {
		return err
	}
	return countPrice(ctx, tx, r, w.DayStart, model)
}

func countPrice(ctx context.Context, tx pg.Tx, r Repricing, day time.Time, model OperationsPriceCounter) error {
	if err := New().CountPriceChange(ctx, tx, CountPriceChangeParams{
		SiteID: r.Site, DayStart: day, ModelID: r.Model, ChangeBps: r.counted(), FirstMove: model.Count == 0,
	}); err != nil {
		return fmt.Errorf("count the price change of model %s: %w", r.Model, err)
	}
	return nil
}

// PriceUsage is an envelope with what its site's counters of the current UTC day hold: the publications and models
// changed, and each model's moves.
type PriceUsage struct {
	Envelope        PriceEnvelope
	DayStart        time.Time
	Changes, Models int32
	Moves           []ModelMoves
}

// ModelMoves is what one model's prices moved in a day, in basis points added together, and how many times.
type ModelMoves struct {
	Model  uuid.UUID
	Change int64
	Count  int32
}

// PriceUsages reads every envelope with its use today.
func PriceUsages(ctx context.Context, tx pg.Tx) ([]PriceUsage, error) {
	rows, err := New().PriceUsage(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the price envelopes' use: %w", err)
	}
	var usage []PriceUsage
	for _, r := range rows {
		if len(usage) == 0 || usage[len(usage)-1].Envelope.Site != r.SiteID {
			usage = append(usage, PriceUsage{Envelope: envelopeOf(OperationsPriceEnvelope{
				SiteID: r.SiteID, MinMarginBps: r.MinMarginBps, ModelDailyChangeBps: r.ModelDailyChangeBps,
				DailyChanges: r.DailyChanges, DailyModels: r.DailyModels, Rev: r.Rev, UpdatedAt: r.UpdatedAt,
			}), DayStart: r.DayStart})
		}
		u := &usage[len(usage)-1]
		if r.ModelID == nil {
			u.Changes, u.Models = r.Count, r.Models
			continue
		}
		u.Moves = append(u.Moves, ModelMoves{Model: *r.ModelID, Change: r.ChangeBps, Count: r.Count})
	}
	return usage, nil
}
