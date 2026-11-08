package operations

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The owner's daily digest (docs/domains/operations.md「审计」). Its invariant: every UTC day from the first one on has
// exactly one digest, made from one snapshot taken after every transaction that could still write into the day has
// ended, and announced as an incident of its own. The days are made in order from the day after the last one, so a
// node that was down catches up; the row's key is the day and inserting it shares a transaction with the signal, so
// whichever node or attempt gets there first makes and announces it, once.

const (
	// digestSignal is the alert that carries a digest's headline to the owner's channels, keyed by its day. It is
	// never resolved here: the alert module treats it as a notice, which closes itself once told and sends no recovery.
	digestSignal = "operations.audit_digest"
	// digestDays bounds the days one run makes, so a node that was down for weeks catches up a week a minute.
	digestDays = 7
)

// AuditDigest is one UTC day as the owner reads it, kept as it was made. The JSON is the contract's AuditDigest.
type AuditDigest struct {
	Day        string            `json:"day"` // YYYY-MM-DD
	MadeAt     time.Time         `json:"made_at"`
	Operations []DigestOperation `json:"operations"`
	Money      []DigestMoney     `json:"money"`
	Approvals  []DigestApproval  `json:"approvals"`
	Refused    []DigestRefusal   `json:"refused"`
	Chain      DigestChain       `json:"chain"`
}

// DigestOperation is how many of the agent's operations proposed that day with one tool are now in one state.
type DigestOperation struct {
	Tool  string `json:"tool"`
	State string `json:"state"`
	Count int32  `json:"count"`
}

// DigestMoney is one cap policy's day: what its counter held when the digest was made, against its daily limits.
type DigestMoney struct {
	Scope         string `json:"scope"` // platform or the site's TypeID
	Category      string `json:"category"`
	Currency      string `json:"currency"`
	Reserved      int64  `json:"reserved,string"`
	Committed     int64  `json:"committed,string"`
	Count         int32  `json:"count"`
	DailyMax      int64  `json:"daily_max,string"`
	HardDailyMax  int64  `json:"hard_daily_max,string"`
	DailyCountMax int32  `json:"daily_count_max"`
}

// DigestApproval is how many approvals asked that day are now in one state.
type DigestApproval struct {
	State string `json:"state"`
	Count int32  `json:"count"`
}

// DigestRefusal is how many calls that day were refused with one error code; the code is empty for a refusal without one.
type DigestRefusal struct {
	Code  string `json:"code"`
	Count int32  `json:"count"`
}

// DigestChain is the hash chain when the digest was made: its head, the faults that stopped it, and when the check of
// the whole chain last finished.
type DigestChain struct {
	HeadSeq   int64      `json:"head_seq"`
	HeadAt    *time.Time `json:"head_at,omitempty"`
	Faults    int64      `json:"faults"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// digestClock reads, in tx, the time now and the frontier: the moment before which every transaction has ended, so
// nothing written into a day that ended before it can still commit.
type digestClock func(ctx context.Context, tx pg.Tx) (now, frontier time.Time, err error)

// databaseClock is the database's clock and the audit chain's frontier (AuditChainFrontier), with settle for a
// transaction that has begun and not yet shown itself.
func databaseClock(settle time.Duration) digestClock {
	return func(ctx context.Context, tx pg.Tx) (time.Time, time.Time, error) {
		clock, err := New().AuditChainFrontier(ctx, tx, settle.Seconds())
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("read the database's clock: %w", err)
		}
		return clock.Now, clock.Frontier, nil
	}
}

// MakeAuditDigests makes the digests of the days that have ended since the last one, up to digestDays, each announced
// by a signal. It returns how many it made.
func MakeAuditDigests(ctx context.Context, db *pg.DB, scope pg.Scope) (int, error) {
	return makeAuditDigests(ctx, db, scope, databaseClock(chainSettle))
}

// makeAuditDigests is MakeAuditDigests on clock, which the tests fix.
func makeAuditDigests(ctx context.Context, db *pg.DB, scope pg.Scope, clock digestClock) (int, error) {
	made := 0
	for range digestDays {
		day, now, ok, err := nextDigestDay(ctx, db, scope, clock)
		if err != nil || !ok {
			return made, err
		}
		var d AuditDigest
		// One snapshot for the whole day, so a sweep that commits between two reads cannot show an operation still
		// waiting beside its approval expired.
		if err := db.ReadTx(ctx, scope, func(tx pg.Tx) (err error) { d, err = digestOf(ctx, tx, day, now); return err }); err != nil {
			return made, err
		}
		saved, err := saveDigest(ctx, db, scope, day, d)
		if err != nil || !saved { // another node made it, and carries on from there
			return made, err
		}
		made++
	}
	return made, nil
}

// nextDigestDay is the day the next digest is of (the day after the last one, or the day before now before the first),
// the time now, and whether the day is final: it has ended before the frontier, so every transaction that began in it
// has ended and nothing can still commit into it. The clock is read in a transaction of its own, before the day's
// snapshot, so every transaction it saw end is in that snapshot.
func nextDigestDay(ctx context.Context, db *pg.DB, scope pg.Scope, clock digestClock) (day, now time.Time, final bool, err error) {
	err = db.Tx(ctx, scope, func(tx pg.Tx) error {
		var frontier time.Time
		if now, frontier, err = clock(ctx, tx); err != nil {
			return err
		}
		today := now.UTC().Truncate(24 * time.Hour)
		if day, err = New().AuditDigestNext(ctx, tx, today); err != nil {
			return fmt.Errorf("read the next audit digest's day: %w", err)
		}
		final = !day.Add(24 * time.Hour).After(frontier)
		return nil
	})
	return day, now, final, err
}

// saveDigest stores d as the digest of day and signals it, unless that day's is stored already; it reports whether it
// stored it.
func saveDigest(ctx context.Context, db *pg.DB, scope pg.Scope, day time.Time, d AuditDigest) (bool, error) {
	body, err := jsonx.MarshalCanonical(d)
	if err != nil {
		return false, fmt.Errorf("encode the digest of %s: %w", d.Day, err)
	}
	var saved bool
	err = db.Tx(ctx, scope, func(tx pg.Tx) error {
		n, err := New().InsertAuditDigest(ctx, tx, InsertAuditDigestParams{DayStart: day, Body: body})
		if err != nil || n == 0 {
			return err
		}
		saved = true
		return alert.Persist(ctx, tx, uuid.NewV7(), digestAlert(d))
	})
	if err != nil {
		return false, fmt.Errorf("save the audit digest of %s: %w", d.Day, err)
	}
	return saved, nil
}

// digestOf reads the day starting at start, as it is at now.
func digestOf(ctx context.Context, tx pg.Tx, start, now time.Time) (AuditDigest, error) {
	q, end := New(), start.Add(24*time.Hour)
	d := AuditDigest{Day: start.UTC().Format(time.DateOnly)}
	ops, err := q.AuditDayOperations(ctx, tx, AuditDayOperationsParams{DayStart: start, DayEnd: end})
	if err != nil {
		return d, fmt.Errorf("read the day's operations: %w", err)
	}
	for _, r := range ops {
		d.Operations = append(d.Operations, DigestOperation(r))
	}
	approvals, err := q.AuditDayApprovals(ctx, tx, AuditDayApprovalsParams{DayStart: start, DayEnd: end})
	if err != nil {
		return d, fmt.Errorf("read the day's approvals: %w", err)
	}
	for _, r := range approvals {
		d.Approvals = append(d.Approvals, DigestApproval(r))
	}
	refused, err := q.AuditDayRefusals(ctx, tx, AuditDayRefusalsParams{DayStart: start, DayEnd: end})
	if err != nil {
		return d, fmt.Errorf("read the day's refused calls: %w", err)
	}
	for _, r := range refused {
		d.Refused = append(d.Refused, DigestRefusal(r))
	}
	if d.Money, err = digestMoney(ctx, tx, start); err != nil {
		return d, err
	}
	d.Chain, err = digestChain(ctx, tx)
	d.MadeAt = now.UTC()
	return d, err
}

// digestMoney reads every cap policy's counter of the day starting at start.
func digestMoney(ctx context.Context, tx pg.Tx, start time.Time) ([]DigestMoney, error) {
	rows, err := New().AuditDayMoney(ctx, tx, start)
	if err != nil {
		return nil, fmt.Errorf("read the day's money: %w", err)
	}
	money := make([]DigestMoney, len(rows))
	for i, r := range rows {
		scope := "platform"
		if r.SiteID != nil {
			scope = sitePrefix.Encode(*r.SiteID)
		}
		money[i] = DigestMoney{
			Scope: scope, Category: r.Category, Currency: r.Currency, Reserved: r.Reserved, Committed: r.Committed,
			Count: r.Count, DailyMax: r.DailyMax, HardDailyMax: r.HardDailyMax, DailyCountMax: r.DailyCountMax,
		}
	}
	return money, nil
}

// digestChain reads the chain's head, faults and last whole check.
func digestChain(ctx context.Context, tx pg.Tx) (DigestChain, error) {
	head, _, err := headOf(ctx, tx)
	if err != nil {
		return DigestChain{}, err
	}
	c := DigestChain{HeadSeq: head.Seq}
	if head.Seq > 0 {
		c.HeadAt = &head.At
	}
	row, err := New().AuditChainState(ctx, tx)
	if err != nil {
		return c, fmt.Errorf("read the audit chain's faults and check: %w", err)
	}
	c.Faults, c.CheckedAt = row.Faults, row.CheckedAt
	return c, nil
}

// digestAlert is the digest's headline: totals the owner's channel can show, the whole digest being in the API.
func digestAlert(d AuditDigest) alert.Signal {
	var ops, proposals, approvals, refused int64
	for _, o := range d.Operations {
		ops += int64(o.Count)
	}
	for _, m := range d.Money {
		proposals += int64(m.Count)
	}
	for _, a := range d.Approvals {
		approvals += int64(a.Count)
	}
	for _, r := range d.Refused {
		refused += int64(r.Count)
	}
	text := func(n int64) string { return strconv.FormatInt(n, 10) }
	return alert.Signal{Kind: digestSignal, Key: d.Day, Severity: "info", Fields: map[string]string{
		"day": d.Day, "operations": text(ops), "money_proposals": text(proposals), "approvals": text(approvals),
		"refused": text(refused), "chain_head": text(d.Chain.HeadSeq), "chain_faults": text(d.Chain.Faults),
	}}
}

// AuditDigests reads up to n digests newest first, those of the days before the one starting at before when it is
// set, and whether more follow.
func AuditDigests(ctx context.Context, tx pg.Tx, before *time.Time, n int) ([]AuditDigest, bool, error) {
	rows, err := New().AuditDigests(ctx, tx, AuditDigestsParams{Before: before, N: int64(n) + 1})
	if err != nil {
		return nil, false, fmt.Errorf("read the audit digests: %w", err)
	}
	more := len(rows) > n
	if more {
		rows = rows[:n]
	}
	digests := make([]AuditDigest, len(rows))
	for i, body := range rows {
		if err := jsonx.Unmarshal(body, &digests[i]); err != nil {
			return nil, false, fmt.Errorf("decode an audit digest: %w", err)
		}
	}
	return digests, more, nil
}

// digestArgs is one look for a day without its digest.
type digestArgs struct{}

// Kind implements river.JobArgs.
func (digestArgs) Kind() string { return "operations.audit_digest" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time, on the housekeeping queue.
func (digestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// digestWork is one run of the job, logged when it fails.
func digestWork(db *pg.DB, scope pg.Scope, logger *slog.Logger) func(context.Context, *river.Job[digestArgs]) error {
	return func(ctx context.Context, _ *river.Job[digestArgs]) error {
		if _, err := MakeAuditDigests(ctx, db, scope); err != nil {
			logger.Error("operations: the audit digest stopped", "err", err)
		}
		return nil
	}
}

// RegisterAuditDigest adds the daily digest to queue: every minute it looks for a day that has ended without one, so a
// day's is made in the first minutes after it, on whichever node leads the queue. A failed run is logged; the next
// minute tries again.
func RegisterAuditDigest(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(digestWork(db, scope, logger))); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return digestArgs{} })
	return nil
}
