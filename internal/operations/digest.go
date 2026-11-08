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

// The owner's daily digest (docs/domains/operations.md「审计」): once a UTC day has ended, one digest of it is made and
// kept, and a signal tells the owner. Inserting the day's row and the signal share a transaction, and the row's key is
// the day, so the digest is made and announced exactly once, whichever node or attempt gets there first.

// digestSignal is the alert that carries the digest's headline to the owner's channels. One incident, keyed platform,
// whose fields each day's digest replaces: a rule that repeats every day delivers the latest.
const digestSignal = "operations.audit_digest"

// AuditDigest is one UTC day as the owner reads it, kept as it was made. The JSON is the contract's AuditDigest.
type AuditDigest struct {
	Day        string            `json:"day"` // YYYY-MM-DD
	MadeAt     time.Time         `json:"made_at"`
	Operations []DigestOperation `json:"operations"`
	Money      []DigestMoney     `json:"money"`
	Approvals  []DigestApproval  `json:"approvals"`
	Refused    []DigestRefusal   `json:"refused"`
	Chain      DigestChain       `json:"chain"`
	Replica    DigestReplica     `json:"replica"`
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

// DigestReplica is the copy of the chain when the digest was made. State is unconfigured (the node has no replica
// bucket), behind (a link has waited longer than replicaLagAfter) or current.
type DigestReplica struct {
	State         string     `json:"state"`
	ReplicatedSeq int64      `json:"replicated_seq"`
	ReplicatedAt  *time.Time `json:"replicated_at,omitempty"`
}

// MakeAuditDigest makes yesterday's digest unless it has been made, and signals it; it reports whether it made one.
// replicaOn says whether this node copies the chain, which the digest reports.
func MakeAuditDigest(ctx context.Context, db *pg.DB, scope pg.Scope, replicaOn bool) (bool, error) {
	var made bool
	err := db.Tx(ctx, scope, func(tx pg.Tx) error {
		q := New()
		due, err := q.AuditDigestDue(ctx, tx)
		if err != nil || due.Made {
			return err
		}
		d, err := digestOf(ctx, tx, due.DayStart, replicaOn)
		if err != nil {
			return err
		}
		body, err := jsonx.MarshalCanonical(d)
		if err != nil {
			return fmt.Errorf("encode the digest of %s: %w", d.Day, err)
		}
		n, err := q.InsertAuditDigest(ctx, tx, InsertAuditDigestParams{DayStart: due.DayStart, Body: body})
		if err != nil || n == 0 {
			return err
		}
		made = true
		return alert.Persist(ctx, tx, uuid.NewV7(), digestAlert(d))
	})
	if err != nil {
		return false, fmt.Errorf("make the audit digest: %w", err)
	}
	return made, nil
}

// digestOf reads the day starting at start.
func digestOf(ctx context.Context, tx pg.Tx, start time.Time, replicaOn bool) (AuditDigest, error) {
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
	refused, err := q.AuditDayRefusals(ctx, tx, AuditDayRefusalsParams{FirstID: v7Floor(start), EndID: v7Floor(end)})
	if err != nil {
		return d, fmt.Errorf("read the day's refused calls: %w", err)
	}
	for _, r := range refused {
		d.Refused = append(d.Refused, DigestRefusal(r))
	}
	if d.Money, err = digestMoney(ctx, tx, start); err != nil {
		return d, err
	}
	if d.Chain, err = digestChain(ctx, tx); err != nil {
		return d, err
	}
	d.Replica, err = digestReplica(ctx, tx, replicaOn)
	d.MadeAt = time.Now().UTC()
	return d, err
}

// v7Floor is the least UUIDv7 made at t: t's milliseconds and zeros, at or below every ID made from t on.
func v7Floor(t time.Time) uuid.UUID {
	var id uuid.UUID
	ms := uint64(t.UnixMilli()) //nolint:gosec // a time after 1970, as every UUIDv7's is
	for i := range 6 {
		id[i] = byte(ms >> (40 - 8*i))
	}
	return id
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

// digestReplica reads how far the copy has got.
func digestReplica(ctx context.Context, tx pg.Tx, replicaOn bool) (DigestReplica, error) {
	lag, err := readReplicaLag(ctx, tx)
	if err != nil {
		return DigestReplica{}, err
	}
	r := DigestReplica{State: "current", ReplicatedSeq: lag.ReplicatedSeq, ReplicatedAt: lag.ReplicatedAt}
	switch {
	case !replicaOn:
		r.State = "unconfigured"
	case lag.behind:
		r.State = "behind"
	}
	return r, nil
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
	return alert.Signal{Kind: digestSignal, Key: "platform", Severity: "info", Fields: map[string]string{
		"day": d.Day, "operations": text(ops), "money_proposals": text(proposals), "approvals": text(approvals),
		"refused": text(refused), "chain_head": text(d.Chain.HeadSeq), "chain_faults": text(d.Chain.Faults),
		"replica": d.Replica.State, "replicated_seq": text(d.Replica.ReplicatedSeq),
	}}
}

// AuditDigests reads up to n digests newest first, those of the days before the one starting at before when it is
// set, and whether more follow.
func AuditDigests(ctx context.Context, tx pg.Tx, before *time.Time, n int) ([]AuditDigest, bool, error) {
	rows, err := New().AuditDigests(ctx, tx, AuditDigestsParams{Before: before, N: int32(n + 1)}) //nolint:gosec // a page size, at most 200
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
func digestWork(db *pg.DB, scope pg.Scope, replicaOn bool, logger *slog.Logger) func(context.Context, *river.Job[digestArgs]) error {
	return func(ctx context.Context, _ *river.Job[digestArgs]) error {
		if _, err := MakeAuditDigest(ctx, db, scope, replicaOn); err != nil {
			logger.Error("operations: the audit digest stopped", "err", err)
		}
		return nil
	}
}

// RegisterAuditDigest adds the daily digest to queue: every minute it looks for yesterday's, so it is made in the first
// minutes of each UTC day, on whichever node leads the queue. A failed run is logged; the next minute tries again.
func RegisterAuditDigest(db *pg.DB, scope pg.Scope, queue *pg.Queue, replicaOn bool, logger *slog.Logger) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(digestWork(db, scope, replicaOn, logger))); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return digestArgs{} })
	return nil
}
