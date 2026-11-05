package operations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Category is a kind of money the agent moves, each with its own caps (docs/domains/operations.md「转账的累计金额上限」).
type Category string

// CategoryTopUp is a top-up of a supplier: the agent proposes it, the owner pays, the agent confirms it arrived.
const CategoryTopUp Category = "money.top_up"

// RiskTopUp is the risk class of a top-up.
const RiskTopUp Risk = "top_up"

var (
	// ErrNoCapPolicy is a scope and category the owner set no caps for: no money moves there.
	ErrNoCapPolicy = errors.New("operations: no cap policy for this scope and category, so no money moves")
	// ErrPolicyChanged is a cap policy changed since the caller read it, or lowered under a proposal waiting on it.
	ErrPolicyChanged = errors.New("operations: the cap policy changed since it was read")
	// ErrCurrencyFixed is a policy change to another currency, which would mix two in one counter.
	ErrCurrencyFixed = errors.New("operations: a cap policy keeps the currency it was made with")
	// ErrBadPolicy is a policy whose hard limit is below its autonomous one, or a negative limit.
	ErrBadPolicy = errors.New("operations: each limit is at least 0 and each hard limit at least its autonomous limit")
	// ErrWindowEnded is a proposal whose UTC day ended while it waited for a lock: it is not reserved in a day that is
	// over, and is proposed again on the new one.
	ErrWindowEnded = errors.New("operations: the UTC day ended while the proposal waited; propose it again")
	// ErrNotReserved is a money operation with no reservation to confirm or approve: it still waits for approval, or its
	// reservation ended, committed, released or past its windows.
	ErrNotReserved = errors.New("operations: the operation holds no reservation that can be settled")
)

// CapExceeded refuses a proposal over a hard limit: Limit names it, Max is it, Used is what its window holds already
// and Amount the proposal's.
type CapExceeded struct {
	Limit             string
	Max, Used, Amount int64
}

func (e *CapExceeded) Error() string {
	return fmt.Sprintf("operations: the %s cap of %d would be exceeded: %d used, %d asked", e.Limit, e.Max, e.Used, e.Amount)
}

// Transfer is what a money proposal moves: in which scope (Site, nil for the platform) and category, on which object, the
// amount in 10⁻⁹ of the policy's Currency, and Basis, what the server computed it from. Summary is what the owner is
// shown when it waits for approval.
type Transfer struct {
	Site     *uuid.UUID
	Category Category
	Object   Target
	Amount   int64
	Currency platform.Currency
	Basis    map[string]any
	Summary  map[string]any
}

// counters is the three counter rows of a money action, locked in the order every path takes, with its policy.
type counters struct {
	ids                  []int64
	day, week, objectDay OperationsCapCounter
	dayStart, weekStart  time.Time
}

func lockCounters(ctx context.Context, tx pg.Tx, site *uuid.UUID, category Category, object Target, day, week time.Time) (counters, error) {
	q := New()
	if err := q.EnsureCapCounters(ctx, tx, EnsureCapCountersParams{
		Category: string(category), DayStart: day, WeekStart: week, ObjectKind: object.Kind, ObjectID: object.ID, SiteID: site,
	}); err != nil {
		return counters{}, fmt.Errorf("make the cap counters: %w", err)
	}
	rows, err := q.LockCapCounters(ctx, tx, LockCapCountersParams{
		Category: string(category), SiteID: site, DayStart: day, WeekStart: week, ObjectKind: object.Kind, ObjectID: object.ID,
	})
	if err != nil || len(rows) != 3 {
		return counters{}, fmt.Errorf("lock the cap counters (%d): %w", len(rows), err)
	}
	return counters{ids: []int64{rows[0].ID, rows[1].ID, rows[2].ID}, day: rows[0], week: rows[1], objectDay: rows[2], dayStart: day, weekStart: week}, nil
}

// used is what a counter holds, reserved and committed.
func used(c OperationsCapCounter) int64 { return c.Reserved + c.Committed }

// weigh decides amount against p and what c holds: a *CapExceeded over a hard limit, otherwise whether it is within
// every autonomous limit. A sum that does not fit int64 is over any limit.
func weigh(p CapPolicy, c counters, amount int64) (bool, error) {
	over := func(u, limit int64) bool { return u > math.MaxInt64-amount || u+amount > limit }
	switch {
	case amount > p.HardSingle:
		return false, &CapExceeded{Limit: "hard_single", Max: p.HardSingle, Amount: amount}
	case over(used(c.day), p.HardDaily):
		return false, &CapExceeded{Limit: "hard_daily", Max: p.HardDaily, Used: used(c.day), Amount: amount}
	case over(used(c.week), p.HardWeekly):
		return false, &CapExceeded{Limit: "hard_weekly", Max: p.HardWeekly, Used: used(c.week), Amount: amount}
	}
	return amount <= p.Single && !over(used(c.day), p.Daily) && !over(used(c.week), p.Weekly) &&
		!over(used(c.objectDay), p.ObjectDaily) && c.day.Count < p.DailyCount, nil
}

// ProposeMoney is Propose for a proposal that moves m: it locks m's policy and counters, finds a retry of the key as
// Propose does, and then refuses an amount over a hard limit (*CapExceeded, nothing written), asks the owner for one
// over an autonomous limit, and reserves the amount in the category's day and week and the object's day until the end
// of the UTC day. The caller runs one that needs nobody in tx and settles it, as with Propose. A policy-less scope is
// ErrNoCapPolicy. The reservation ends when the operation is refused, expires or is canceled (release), when the
// agent confirms the money (CommitReservation), or at the end of its day (ExpireReservations).
func ProposeMoney(ctx context.Context, tx pg.Tx, p Proposal, m Transfer) (Operation, bool, error) {
	policy, err := lockPolicy(ctx, tx, m.Category, m.Site)
	if err != nil {
		return Operation{}, false, err
	}
	if policy.Currency != m.Currency {
		return Operation{}, false, fmt.Errorf("a %s amount for a %s cap", m.Currency, policy.Currency)
	}
	q := New()
	w, err := q.CapWindows(ctx, tx)
	if err != nil {
		return Operation{}, false, fmt.Errorf("read the cap windows: %w", err)
	}
	c, err := lockCounters(ctx, tx, m.Site, m.Category, m.Object, w.DayStart, w.WeekStart)
	if err != nil {
		return Operation{}, false, err
	}
	// The counters serialize the proposals of a scope and category, a retry of this key among them, so it is found here
	// before it is weighed again against counters that hold its own reservation.
	if op, found, err := Replay(ctx, tx, p); found || err != nil {
		return op, found, err
	}
	within, err := weigh(policy, c, m.Amount)
	if err != nil {
		return Operation{}, false, err
	}
	if !within {
		p.Summary = maps.Clone(m.Summary)
		p.Summary["cap"] = capDetail(policy, c, m.Amount)
	}
	op, replayed, err := Propose(ctx, tx, p)
	if err != nil || replayed {
		return op, replayed, err
	}
	return op, false, reserve(ctx, tx, op, m, policy, w, c)
}

// reserve records op's money action and adds its amount to the counters c, which tx holds, until the end of the day,
// unless that day has ended.
func reserve(ctx context.Context, tx pg.Tx, op Operation, m Transfer, policy CapPolicy, w CapWindowsRow, c counters) error {
	basis, err := jsonx.MarshalCanonical(m.Basis)
	if err != nil {
		return fmt.Errorf("encode the basis of operation %s: %w", op.ID, err)
	}
	q := New()
	if err := q.InsertMoneyAction(ctx, tx, InsertMoneyActionParams{
		OperationID: op.ID, Category: string(m.Category), SiteID: m.Site, ObjectKind: m.Object.Kind, ObjectID: m.Object.ID,
		Amount: m.Amount, Currency: m.Currency.String(), Basis: basis, PolicyRev: policy.Rev, DayStart: w.DayStart,
		WeekStart: w.WeekStart, ExpiresAt: w.DayEnd,
	}); err != nil {
		return fmt.Errorf("reserve operation %s: %w", op.ID, err)
	}
	if err := q.MoveCapCounters(ctx, tx, MoveCapCountersParams{Ids: c.ids, Reserved: m.Amount, Count: 1}); err != nil {
		return fmt.Errorf("reserve operation %s: %w", op.ID, err)
	}
	// Every lock the proposal waits for is taken by now, the signal's included; a day that ended meanwhile refuses it
	// rather than reserving in yesterday's windows a proposal that could no longer be confirmed.
	ended, err := q.WindowEnded(ctx, tx, w.DayStart)
	if err != nil {
		return fmt.Errorf("check the cap windows: %w", err)
	}
	if ended {
		return ErrWindowEnded
	}
	return nil
}

// Replay finds the operation p's credential made with p's key, as Propose would: found when it is p's request, and
// ErrKeyReused when the key was used for another. A handler calls it before it reads what a first request needs, so a
// retry finds its operation even when the target has changed since; one still uncommitted is not found here, and
// Propose finds it once it commits.
func Replay(ctx context.Context, tx pg.Tx, p Proposal) (Operation, bool, error) {
	rows, err := New().OperationsByKey(ctx, tx, OperationsByKeyParams{CredentialID: p.Credential, IdempotencyKey: p.Key})
	if err != nil || len(rows) == 0 {
		return Operation{}, false, err
	}
	params, err := jsonx.MarshalCanonical(p.Params)
	if err != nil {
		return Operation{}, false, fmt.Errorf("propose %s: encode the parameters: %w", p.Tool, err)
	}
	if hash := requestHash(p.Tool, p.Target, params); !bytes.Equal(rows[0].RequestHash, hash[:]) {
		return Operation{}, false, ErrKeyReused
	}
	op, err := withApproval(ctx, tx, operationOf(rows[0]))
	return op, true, err
}

// capDetail is what the owner is shown of the caps a proposal is weighed against: each limit and what its window
// holds before it. Amounts are decimal text, which a browser reads without rounding.
func capDetail(p CapPolicy, c counters, amount int64) map[string]any {
	text := func(n int64) string { return strconv.FormatInt(n, 10) }
	return map[string]any{
		"currency": p.Currency.String(), "amount": text(amount), "policy_rev": p.Rev,
		"single_max": text(p.Single), "hard_single_max": text(p.HardSingle),
		"daily_used": text(used(c.day)), "daily_max": text(p.Daily), "hard_daily_max": text(p.HardDaily),
		"weekly_used": text(used(c.week)), "weekly_max": text(p.Weekly), "hard_weekly_max": text(p.HardWeekly),
		"object_daily_used": text(used(c.objectDay)), "object_daily_max": text(p.ObjectDaily),
		"daily_count": c.day.Count, "daily_count_max": p.DailyCount,
	}
}

// Reservation is a money operation's reservation as it is now.
type Reservation struct {
	Operation uuid.UUID
	Category  Category
	Object    Target
	Amount    int64
	Currency  string
	Basis     []byte
	State     string
	Expires   time.Time
	Proof     string
	Settled   *time.Time
}

// CheckReservation is what an approval of op, a money operation tx has locked, rechecks before it runs: the reservation
// is still held (otherwise ErrNotReserved) and still within the hard limits of the policy as it is now, the windows
// holding it already (otherwise ErrPolicyChanged): a policy loosened since the proposal leaves it alone, one tightened
// under it refuses it. The caller then ends op rejected, which releases it.
func CheckReservation(ctx context.Context, tx pg.Tx, op Operation) (Reservation, error) {
	q := New()
	rows, err := q.LockMoneyAction(ctx, tx, op.ID)
	if err != nil || len(rows) == 0 {
		return Reservation{}, fmt.Errorf("lock the reservation of operation %s: %w", op.ID, err)
	}
	m := rows[0].OperationsMoneyAction
	if m.State != "reserved" || rows[0].Due {
		return Reservation{}, ErrNotReserved
	}
	p, err := lockPolicy(ctx, tx, Category(m.Category), m.SiteID)
	if err != nil {
		return Reservation{}, err
	}
	c, err := lockCounters(ctx, tx, m.SiteID, Category(m.Category), Target{Kind: m.ObjectKind, ID: m.ObjectID}, m.DayStart, m.WeekStart)
	if err != nil {
		return Reservation{}, err
	}
	if m.Amount > p.HardSingle || used(c.day) > p.HardDaily || used(c.week) > p.HardWeekly {
		return Reservation{}, ErrPolicyChanged
	}
	return reservationOf(m), nil
}

// holdReservation locks operation id's reservation, if it holds one, then its policy and counters, so the run of an
// approval waits for nothing after the approval's signal is resolved.
func holdReservation(ctx context.Context, tx pg.Tx, id uuid.UUID) error {
	rows, err := New().LockMoneyAction(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("lock the reservation of operation %s: %w", id, err)
	}
	if len(rows) == 0 || rows[0].OperationsMoneyAction.State != "reserved" {
		return nil
	}
	m := rows[0].OperationsMoneyAction
	if _, err := lockPolicy(ctx, tx, Category(m.Category), m.SiteID); err != nil && !errors.Is(err, ErrNoCapPolicy) {
		return err
	}
	_, err = lockCounters(ctx, tx, m.SiteID, Category(m.Category), Target{Kind: m.ObjectKind, ID: m.ObjectID}, m.DayStart, m.WeekStart)
	return err
}

// CommitReservation is the agent's confirmation, in operation confirm, that the money of its own proposal arrived,
// with proof: the reservation moves to committed in the windows it was reserved in, and stays there whatever happens
// to the money later. A proposal that is not the credential's, not a succeeded money proposal, or whose reservation
// ended is refused (ErrNoOperation, ErrNotReserved), and nothing is written.
func CommitReservation(ctx context.Context, tx pg.Tx, confirm Operation, proposal uuid.UUID, object Target, proof string) (Reservation, error) {
	m, err := lockProposal(ctx, tx, confirm.Credential, proposal, object)
	if err != nil {
		return Reservation{}, err
	}
	if err := moveReserved(ctx, tx, m, m.Amount); err != nil {
		return Reservation{}, err
	}
	if err := New().SettleMoneyAction(ctx, tx, SettleMoneyActionParams{OperationID: proposal, State: "committed", Proof: &proof, ConfirmedBy: &confirm.ID}); err != nil {
		return Reservation{}, fmt.Errorf("commit the reservation of operation %s: %w", proposal, err)
	}
	m.State, m.Proof = "committed", &proof
	return reservationOf(m), recordReservation(ctx, tx, audit.OperationsAgent(confirm.Credential), "reservation.commit", m, confirm.Reason)
}

// lockProposal locks credential's succeeded money proposal on object, then its reservation, which must still be held
// and within its windows.
func lockProposal(ctx context.Context, tx pg.Tx, credential, proposal uuid.UUID, object Target) (OperationsMoneyAction, error) {
	q := New()
	locked, err := q.LockOperation(ctx, tx, proposal)
	if err != nil {
		return OperationsMoneyAction{}, fmt.Errorf("lock operation %s: %w", proposal, err)
	}
	if len(locked) == 0 || locked[0].CredentialID != credential || locked[0].TargetKind != object.Kind || locked[0].TargetID != object.ID {
		return OperationsMoneyAction{}, ErrNoOperation
	}
	rows, err := q.LockMoneyAction(ctx, tx, proposal)
	switch {
	case err != nil:
		return OperationsMoneyAction{}, fmt.Errorf("lock the reservation of operation %s: %w", proposal, err)
	case len(rows) == 0:
		return OperationsMoneyAction{}, ErrNoOperation
	case State(locked[0].State) != StateSucceeded || rows[0].OperationsMoneyAction.State != "reserved" || rows[0].Due:
		return OperationsMoneyAction{}, ErrNotReserved
	}
	return rows[0].OperationsMoneyAction, nil
}

// moveReserved takes amount out of m's reservation in the windows it was made in, adding committed to them.
func moveReserved(ctx context.Context, tx pg.Tx, m OperationsMoneyAction, committed int64) error {
	c, err := lockCounters(ctx, tx, m.SiteID, Category(m.Category), Target{Kind: m.ObjectKind, ID: m.ObjectID}, m.DayStart, m.WeekStart)
	if err != nil {
		return err
	}
	return New().MoveCapCounters(ctx, tx, MoveCapCountersParams{Ids: c.ids, Reserved: -m.Amount, Committed: committed})
}

// release ends op's reservation, if it has one still held, when op ends without its money: refused, expired or
// canceled. tx holds op.
func release(ctx context.Context, tx pg.Tx, actor audit.Actor, op Operation) error {
	rows, err := New().LockMoneyAction(ctx, tx, op.ID)
	if err != nil {
		return fmt.Errorf("lock the reservation of operation %s: %w", op.ID, err)
	}
	if len(rows) == 0 || rows[0].OperationsMoneyAction.State != "reserved" {
		return nil
	}
	return releaseLocked(ctx, tx, actor, rows[0].OperationsMoneyAction)
}

func releaseLocked(ctx context.Context, tx pg.Tx, actor audit.Actor, m OperationsMoneyAction) error {
	if err := moveReserved(ctx, tx, m, 0); err != nil {
		return err
	}
	if err := New().SettleMoneyAction(ctx, tx, SettleMoneyActionParams{OperationID: m.OperationID, State: "released"}); err != nil {
		return fmt.Errorf("release the reservation of operation %s: %w", m.OperationID, err)
	}
	m.State = "released"
	return recordReservation(ctx, tx, actor, "reservation.release", m, "")
}

// reservationExpiryActor is who releases a reservation whose windows ended unconfirmed.
var reservationExpiryActor = audit.System("operations.reservation_expiry")

// ExpireReservations releases up to batch reservations past the end of their windows and returns how many: a top-up
// the agent did not confirm on the day it proposed it no longer holds that day's caps. Rows a confirmation holds are
// skipped; one that commits first wins.
func ExpireReservations(ctx context.Context, tx pg.Tx, batch int) (int, error) {
	q := New()
	ids, err := q.LockDueMoneyActions(ctx, tx, int32(batch)) // #nosec G115 -- a small constant
	if err != nil {
		return 0, fmt.Errorf("expire the reservations past their windows: %w", err)
	}
	for _, id := range ids {
		rows, err := q.MoneyActionOf(ctx, tx, id)
		if err != nil || len(rows) == 0 {
			return 0, fmt.Errorf("read the reservation of operation %s: %w", id, err)
		}
		if err := releaseLocked(ctx, tx, reservationExpiryActor, rows[0]); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

func recordReservation(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, m OperationsMoneyAction, reason string) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "operations", Kind: "operation", ID: m.OperationID.String()},
		Detail: map[string]any{
			"operation_id": m.OperationID.String(), "category": m.Category, "object_kind": m.ObjectKind, "object_id": m.ObjectID,
			"amount": m.Amount, "currency": m.Currency, "state": m.State, "day_start": m.DayStart, "week_start": m.WeekStart,
		},
	})
}

// ReservationOf reads operation id's reservation; found is false when it moves no money.
func ReservationOf(ctx context.Context, tx pg.Tx, id uuid.UUID) (Reservation, bool, error) {
	rows, err := New().MoneyActionOf(ctx, tx, id)
	if err != nil || len(rows) == 0 {
		return Reservation{}, false, err
	}
	return reservationOf(rows[0]), true, nil
}

func reservationOf(m OperationsMoneyAction) Reservation {
	return Reservation{
		Operation: m.OperationID, Category: Category(m.Category), Object: Target{Kind: m.ObjectKind, ID: m.ObjectID},
		Amount: m.Amount, Currency: m.Currency, Basis: m.Basis, State: m.State, Expires: m.ExpiresAt, Proof: deref(m.Proof),
		Settled: m.SettledAt,
	}
}
