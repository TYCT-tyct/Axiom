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

// The categories: a top-up of a supplier, which the agent proposes, the owner pays and the agent confirms arrived; a
// credit grant to an account, which the owner approves and which gives the credit when it runs; and a refund of an
// order, which the owner approves and the payment channel then sends.
const (
	CategoryTopUp  Category = "money.top_up"
	CategoryCredit Category = "money.credit"
	CategoryRefund Category = "money.refund"
)

// The risk classes of a top-up, a credit grant and a refund.
const (
	RiskTopUp  Risk = "top_up"
	RiskGift   Risk = "gift"
	RiskRefund Risk = "refund"
)

// autonomous is whether a proposal of c within every autonomous limit needs nobody: a top-up does; a credit grant
// always waits for the owner (「退款与赠额默认人工」), so its autonomous limits bind nothing and only its hard limits
// refuse.
func (c Category) autonomous() bool { return c == CategoryTopUp }

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

// The reasons a money proposal waits: its scope is paused, or it goes over an autonomous limit of a category that has
// them (capDetail's single_max, daily_max, weekly_max, object_daily_max and daily_count_max).
const (
	ReasonPaused      Reason = "cap_paused"
	ReasonSingle      Reason = "cap_single"
	ReasonDaily       Reason = "cap_daily"
	ReasonWeekly      Reason = "cap_weekly"
	ReasonObjectDaily Reason = "cap_object_daily"
	ReasonDailyCount  Reason = "cap_daily_count"
)

// used is what a counter holds, reserved and committed.
func used(c OperationsCapCounter) int64 { return c.Reserved + c.Committed }

// weigh decides amount against p and what c holds: a *CapExceeded over a hard limit, otherwise why it waits for a
// person, none when it needs nobody: a paused scope, a category that always asks, or each autonomous limit it goes
// over. A sum that does not fit int64 is over any limit.
func weigh(p CapPolicy, c counters, amount int64) ([]Reason, error) {
	over := func(u, limit int64) bool { return u > math.MaxInt64-amount || u+amount > limit }
	switch {
	case amount > p.HardSingle:
		return nil, &CapExceeded{Limit: "hard_single", Max: p.HardSingle, Amount: amount}
	case over(used(c.day), p.HardDaily):
		return nil, &CapExceeded{Limit: "hard_daily", Max: p.HardDaily, Used: used(c.day), Amount: amount}
	case over(used(c.week), p.HardWeekly):
		return nil, &CapExceeded{Limit: "hard_weekly", Max: p.HardWeekly, Used: used(c.week), Amount: amount}
	}
	var reasons []Reason
	if p.Paused != nil {
		reasons = append(reasons, ReasonPaused)
	}
	if !p.Category.autonomous() { // its autonomous limits bind nothing, so none of them is why it waits
		return append(reasons, ReasonAlways), nil
	}
	for _, limit := range []struct {
		reason Reason
		over   bool
	}{
		{ReasonSingle, amount > p.Single},
		{ReasonDaily, over(used(c.day), p.Daily)},
		{ReasonWeekly, over(used(c.week), p.Weekly)},
		{ReasonObjectDaily, over(used(c.objectDay), p.ObjectDaily)},
		{ReasonDailyCount, c.day.Count >= p.DailyCount},
	} {
		if limit.over {
			reasons = append(reasons, limit.reason)
		}
	}
	return reasons, nil
}

// ProposeMoney is Propose for a proposal that moves m: it locks m's policy and counters, finds a retry of the key as
// Propose does, and then refuses an amount over a hard limit (*CapExceeded, nothing written), asks the owner for one
// over an autonomous limit, in a paused scope or of a category that always asks, and reserves the amount in the category's day and week and the
// object's day until the end of the UTC day, a refund's until its outcome (untilOutcome). The caller runs one that needs nobody in tx and settles it, as with
// Propose. A policy-less scope is ErrNoCapPolicy. The reservation ends when the operation is refused, expires or is canceled (release), when the
// agent confirms the money (CommitReservation), when an executing operation's outcome is known (Conclude), or at the end
// of its day (ExpireReservations).
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
	reasons, err := weigh(policy, c, m.Amount)
	if err != nil {
		return Operation{}, false, err
	}
	if len(reasons) > 0 {
		p.Summary, p.Reasons = maps.Clone(m.Summary), reasons
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
		WeekStart: w.WeekStart, ExpiresAt: m.Category.expiry(w),
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
		"daily_count": c.day.Count, "daily_count_max": p.DailyCount, "paused": p.Paused != nil,
	}
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
	_, err = lockScope(ctx, tx, rows[0].OperationsMoneyAction)
	return err
}

// CommitReservation is the agent's confirmation, in operation confirm, that the money of its own proposal arrived,
// with proof: the reservation moves to committed in the windows it was reserved in, and stays there whatever happens
// to the money later. A proposal that is not the credential's, not a succeeded money proposal, or whose reservation
// ended (as commit checks it) is refused (ErrNoOperation, ErrNotReserved), and nothing is written.
func CommitReservation(ctx context.Context, tx pg.Tx, confirm Operation, proposal uuid.UUID, object Target, proof string) (Reservation, error) {
	m, err := lockProposal(ctx, tx, confirm.Credential, proposal, object)
	if err != nil {
		return Reservation{}, err
	}
	return commit(ctx, tx, m, confirm, proof, audit.OperationsAgent(confirm.Credential))
}

// CommitApproved is the run of op, a money operation the owner approved, that moved its money in tx: its reservation,
// which CheckReservation rechecked in tx, becomes committed in the windows it was reserved in, with proof of where the
// money went and op as what confirmed it. A credit grant commits so, as the credit is given in the same transaction.
func CommitApproved(ctx context.Context, tx pg.Tx, op Operation, proof string) (Reservation, error) {
	rows, err := New().MoneyActionOf(ctx, tx, op.ID)
	switch {
	case err != nil:
		return Reservation{}, fmt.Errorf("read the reservation of operation %s: %w", op.ID, err)
	case len(rows) == 0:
		return Reservation{}, ErrNotReserved
	}
	return commit(ctx, tx, rows[0], op, proof, audit.OperationsAgent(op.Credential))
}

// commit moves m, the reservation of an operation tx holds, to committed, confirmed by operation by with proof, as
// actor's. Only the holder of its operation locks a reservation, so it is locked last, after its scope, and checked
// then by the clock after every wait: a confirmation that waited past midnight does not commit yesterday's.
func commit(ctx context.Context, tx pg.Tx, m OperationsMoneyAction, by Operation, proof string, actor audit.Actor) (Reservation, error) {
	c, err := lockScope(ctx, tx, m)
	if err != nil {
		return Reservation{}, err
	}
	q := New()
	rows, err := q.LockMoneyAction(ctx, tx, m.OperationID)
	switch {
	case err != nil:
		return Reservation{}, fmt.Errorf("lock the reservation of operation %s: %w", m.OperationID, err)
	case len(rows) == 0 || rows[0].OperationsMoneyAction.State != "reserved" || rows[0].Due:
		return Reservation{}, ErrNotReserved
	}
	if err := q.MoveCapCounters(ctx, tx, MoveCapCountersParams{Ids: c.ids, Reserved: -m.Amount, Committed: m.Amount}); err != nil {
		return Reservation{}, fmt.Errorf("commit the reservation of operation %s: %w", m.OperationID, err)
	}
	if err := q.SettleMoneyAction(ctx, tx, SettleMoneyActionParams{OperationID: m.OperationID, State: "committed", Proof: &proof, ConfirmedBy: &by.ID}); err != nil {
		return Reservation{}, fmt.Errorf("commit the reservation of operation %s: %w", m.OperationID, err)
	}
	m.State, m.Proof = "committed", &proof
	return reservationOf(m), recordReservation(ctx, tx, actor, "reservation.commit", m, by.Reason)
}

// lockProposal locks credential's succeeded money proposal on object and reads its reservation, which commit locks.
func lockProposal(ctx context.Context, tx pg.Tx, credential, proposal uuid.UUID, object Target) (OperationsMoneyAction, error) {
	q := New()
	locked, err := q.LockOperation(ctx, tx, proposal)
	if err != nil {
		return OperationsMoneyAction{}, fmt.Errorf("lock operation %s: %w", proposal, err)
	}
	if len(locked) == 0 || locked[0].CredentialID != credential || locked[0].TargetKind != object.Kind || locked[0].TargetID != object.ID {
		return OperationsMoneyAction{}, ErrNoOperation
	}
	rows, err := q.MoneyActionOf(ctx, tx, proposal)
	switch {
	case err != nil:
		return OperationsMoneyAction{}, fmt.Errorf("read the reservation of operation %s: %w", proposal, err)
	case len(rows) == 0:
		return OperationsMoneyAction{}, ErrNoOperation
	case State(locked[0].State) != StateSucceeded:
		return OperationsMoneyAction{}, ErrNotReserved
	}
	return rows[0], nil
}

// lockScope locks m's policy, if it still has one, then its counters in the windows m was made in. It holds the policy
// first, as every path does before it makes or locks a counter: a repair holds the policy and the counters that exist
// and then writes back the ones that are missing, so a confirmation or release that made a missing row and then waited
// for the repair's counters would wait in a cycle.
func lockScope(ctx context.Context, tx pg.Tx, m OperationsMoneyAction) (counters, error) {
	if _, err := lockPolicy(ctx, tx, Category(m.Category), m.SiteID); err != nil && !errors.Is(err, ErrNoCapPolicy) {
		return counters{}, err
	}
	return lockCounters(ctx, tx, m.SiteID, Category(m.Category), Target{Kind: m.ObjectKind, ID: m.ObjectID}, m.DayStart, m.WeekStart)
}

// holdScopes locks the policies and then the counters of every reservation still held by the operations ids, which tx
// holds, each in one order across scopes, windows and objects, before a batch releases the first: released one by one,
// two batches over two scopes in opposite order would wait on each other. A reservation's counters are made with it and
// never deleted (the application cannot), so the rows that exist are every one the batch moves.
func holdScopes(ctx context.Context, tx pg.Tx, ids []uuid.UUID) error {
	q := New()
	if err := q.LockReservedPolicies(ctx, tx, ids); err != nil {
		return fmt.Errorf("lock the cap policies of %d operations: %w", len(ids), err)
	}
	if err := q.LockReservedCounters(ctx, tx, ids); err != nil {
		return fmt.Errorf("lock the cap counters of %d operations: %w", len(ids), err)
	}
	return nil
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
	c, err := lockScope(ctx, tx, m)
	if err != nil {
		return err
	}
	if err := New().MoveCapCounters(ctx, tx, MoveCapCountersParams{Ids: c.ids, Reserved: -m.Amount}); err != nil {
		return fmt.Errorf("release the reservation of operation %s: %w", m.OperationID, err)
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
	if err := holdScopes(ctx, tx, ids); err != nil {
		return 0, err
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
