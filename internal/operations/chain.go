package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// The hash chain over the audit log (docs/domains/operations.md「审计」). Its invariant: the links, by seq, are the audit
// entries up to the head's, in (at, id) order, each hashing the previous link's hash and its entry. Every minute a run
// checks the last chainWindow links against the log and then links the entries that have committed since; once a day
// a round checks every link, a few batches a run, saving its place. Whatever breaks the invariant is recorded as a fault, alerted and stops the chaining: an entry
// is never put in the middle, and a link is never written over.

const (
	// chainBatch is how many links one transaction appends or checks, so a backlog moves in short transactions.
	chainBatch = 500
	// chainWindow is how many links behind the head each run checks before it extends the chain: what a person or
	// a bug changed in the newest entries is found within the minute, and the rest by the daily check, at a cost that
	// does not grow with the log.
	chainWindow = 1000
	// chainCheckBatches bounds one run of the check of the whole chain to that many batches, far within the queue's
	// ten-minute job deadline; the round carries on in the next run, a minute later.
	chainCheckBatches = 20
	// chainSettle is how long after its transaction began an entry is linked at the soonest (AuditChainFrontier). The
	// lag is alerted when the oldest entry not yet linked is a minute older than that.
	chainSettle = time.Minute
)

// The alerts of the chain: a fault (critical, one incident until the owner closes it), the lag (warning, resolved
// when the chain catches up) and a check of the whole chain that has not finished in a day (warning, resolved when it
// does).
const (
	chainFaultSignal = "operations.audit_chain_fault"
	chainLagSignal   = "operations.audit_chain_lag"
	chainCheckSignal = "operations.audit_chain_check_overdue"
)

// chainActor is who records a fault.
var chainActor = audit.System("operations.audit_chain")

// genesis is what the first link follows.
var genesis = make([]byte, sha256.Size)

// linkHash is the hash of the link of e after the link whose hash is prev: SHA-256 of prev followed by the entry's
// fields (entryFields) as an RFC 8785 JSON array. A verifier outside Axiom recomputes it from an export of the log and
// the chain, or from the replica's objects alone.
func linkHash(prev []byte, e audit.Entry) ([]byte, error) {
	encoded, err := jsonx.MarshalCanonical(entryFields(e))
	if err != nil {
		return nil, fmt.Errorf("encode audit entry %s: %w", e.ID, err)
	}
	h := sha256.New()
	h.Write(prev)
	h.Write(encoded)
	return h.Sum(nil), nil
}

// entryFields is what a link's hash covers of its entry: strings and nulls, the first naming this encoding. The detail
// is the text PostgreSQL prints for the jsonb, which is the same for the same value.
func entryFields(e audit.Entry) []any {
	return []any{
		"axiom.audit.v1", e.ID.String(), e.At.UTC().Format("2006-01-02T15:04:05.000000Z"), e.ActorKind, e.ActorID,
		nullable(e.RequestID), e.Action, e.Module, e.TargetKind, e.TargetID, nullable(e.Site), nullable(e.Account),
		e.Reason, string(e.Detail),
	}
}

// nullable is id as text, or null for none.
func nullable(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// fault is what breaks the chain's invariant at one link.
type fault struct {
	kind  string // late, missing, mismatch or broken
	seq   int64
	entry uuid.UUID
}

// ChainAudit is one run: unless the chain is halted, it checks the last chainWindow links, then links the entries
// that have committed, a batch per transaction, and raises or resolves the lag. It returns how many it linked. A fault
// it finds is recorded and stops it.
func ChainAudit(ctx context.Context, db *pg.DB, scope pg.Scope) (int, error) {
	return chainAudit(ctx, db, scope, chainSettle)
}

// chainAudit is ChainAudit with the settling time, which the tests shorten.
func chainAudit(ctx context.Context, db *pg.DB, scope pg.Scope, settle time.Duration) (int, error) {
	chained := 0
	for check := true; ; check = false {
		var n int
		var halted bool
		if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
			n, halted, err = chainStep(ctx, tx, settle, check)
			return err
		}); err != nil || halted {
			return chained, err
		}
		chained += n
		if n < chainBatch {
			return chained, db.Tx(ctx, scope, func(tx pg.Tx) error { return signalLag(ctx, tx, settle) })
		}
	}
}

// chainStep is one transaction of a run: unless the chain is halted, it checks the links behind the head first when
// check is set, then links a batch. It returns how many it linked and whether the chain is halted.
func chainStep(ctx context.Context, tx pg.Tx, settle time.Duration, check bool) (int, bool, error) {
	halted, err := lockChain(ctx, tx)
	if err == nil && !halted && check {
		halted, err = checkHead(ctx, tx)
	}
	if err != nil || halted {
		return 0, halted, err
	}
	n, err := extend(ctx, tx, settle)
	return n, false, err
}

// lockChain takes the chain for the transaction and reports whether a fault has stopped it.
func lockChain(ctx context.Context, tx pg.Tx) (bool, error) {
	q := New()
	if err := q.LockAuditChain(ctx, tx); err != nil {
		return false, fmt.Errorf("lock the audit chain: %w", err)
	}
	halted, err := q.AuditChainHalted(ctx, tx)
	if err != nil {
		return false, fmt.Errorf("read the audit chain's faults: %w", err)
	}
	return halted, nil
}

// checkHead checks the last chainWindow links and halts the chain at a fault, reporting whether it did.
func checkHead(ctx context.Context, tx pg.Tx) (bool, error) {
	head, _, err := headOf(ctx, tx)
	if err != nil || head.Seq == 0 {
		return false, err
	}
	from := max(1, head.Seq-chainWindow+1)
	for {
		f, next, err := verifyBatch(ctx, tx, from)
		if err != nil || f != nil {
			return f != nil, haltAt(ctx, tx, f, err)
		}
		if next == 0 {
			return false, nil
		}
		from = next
	}
}

// headOf is the chain's last link and its entry's place in the log; an empty chain's is seq 0 with the genesis hash,
// before every entry.
func headOf(ctx context.Context, tx pg.Tx) (AuditChainHeadRow, page.Cursor, error) {
	head, err := New().AuditChainHead(ctx, tx)
	if err != nil {
		return AuditChainHeadRow{}, page.Cursor{}, fmt.Errorf("read the audit chain's head: %w", err)
	}
	if len(head) == 0 {
		return AuditChainHeadRow{Hash: genesis}, page.Cursor{}, nil
	}
	return head[0], page.Cursor{At: head[0].At, ID: head[0].EntryID}, nil
}

// extend links the next batch of entries the frontier has passed and returns how many.
func extend(ctx context.Context, tx pg.Tx, settle time.Duration) (int, error) {
	q := New()
	head, after, err := headOf(ctx, tx)
	if err != nil {
		return 0, err
	}
	seq, prev := head.Seq, head.Hash
	clock, err := q.AuditChainFrontier(ctx, tx, settle.Seconds())
	if err != nil {
		return 0, fmt.Errorf("read the audit chain's frontier: %w", err)
	}
	entries, err := audit.Since(ctx, tx, after, clock.Frontier, chainBatch)
	if err != nil {
		return 0, fmt.Errorf("read the audit entries to chain: %w", err)
	}
	for _, e := range entries {
		hash, err := linkHash(prev, e)
		if err != nil {
			return 0, err
		}
		seq++
		if err := q.AppendAuditChain(ctx, tx, AppendAuditChainParams{Seq: seq, EntryID: e.ID, At: e.At, PrevHash: prev, Hash: hash}); err != nil {
			return 0, fmt.Errorf("append link %d: %w", seq, err)
		}
		prev = hash
	}
	return len(entries), nil
}

// signalLag raises the lag when the oldest entry not yet linked is more than a minute past settle, and resolves it
// otherwise.
func signalLag(ctx context.Context, tx pg.Tx, settle time.Duration) error {
	_, after, err := headOf(ctx, tx)
	if err != nil {
		return err
	}
	clock, err := New().AuditChainFrontier(ctx, tx, settle.Seconds())
	if err != nil {
		return fmt.Errorf("read the database's clock: %w", err)
	}
	next, err := audit.Since(ctx, tx, after, clock.Now, 1)
	if err != nil {
		return fmt.Errorf("read the oldest entry not chained: %w", err)
	}
	if len(next) == 0 || clock.Now.Sub(next[0].At) <= settle+time.Minute {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: chainLagSignal, Key: "platform", Severity: "info", Resolved: true})
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: chainLagSignal, Key: "platform", Severity: "warning",
		Fields: map[string]string{"behind_seconds": strconv.FormatInt(int64(clock.Now.Sub(next[0].At)/time.Second), 10)},
	})
}

// VerifyAuditChain advances the check of the whole chain by at most chainCheckBatches batches, a batch per
// transaction: it starts a round up to the head of then once a day, and carries on the round in progress where the
// last run left it, so a run cut short by its deadline loses nothing. It halts the chain at the first fault and
// reports whether the chain is halted, by this check or before it.
func VerifyAuditChain(ctx context.Context, db *pg.DB, scope pg.Scope) (bool, error) {
	return verifyAuditChain(ctx, db, scope, chainCheckBatches)
}

// verifyAuditChain is VerifyAuditChain with at most batches transactions.
func verifyAuditChain(ctx context.Context, db *pg.DB, scope pg.Scope, batches int) (bool, error) {
	for i := range batches {
		var more, halted bool
		if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
			more, halted, err = checkStep(ctx, tx, i == 0)
			return err
		}); err != nil || halted || !more {
			return halted, err
		}
	}
	return false, nil
}

// checkStep is one transaction of the check of the whole chain: it starts a round when one is due, or checks the next
// batch of the round in progress and saves how far it got, in the same transaction. On the first step of a run it
// raises the overdue alert when the round began a day ago; finishing a round resolves it. It reports whether the
// round has more to check now and whether the chain is halted.
func checkStep(ctx context.Context, tx pg.Tx, first bool) (bool, bool, error) {
	halted, err := lockChain(ctx, tx)
	if err != nil || halted {
		return false, halted, err
	}
	q := New()
	rounds, err := q.AuditChainCheck(ctx, tx)
	if err != nil {
		return false, false, fmt.Errorf("read the check of the audit chain: %w", err)
	}
	if len(rounds) == 0 || rounds[0].Due {
		return true, false, startCheck(ctx, tx)
	}
	round := rounds[0]
	if round.Finished {
		return false, false, nil
	}
	if first && round.Overdue {
		if err := signalCheck(ctx, tx, round); err != nil {
			return false, false, err
		}
	}
	return advanceCheck(ctx, tx, round)
}

// advanceCheck checks the next batch of round and saves how far it got, finishing the round once it passes its
// target; it returns as checkStep does.
func advanceCheck(ctx context.Context, tx pg.Tx, round AuditChainCheckRow) (bool, bool, error) {
	f, next, err := verifyBatch(ctx, tx, round.NextSeq)
	if err != nil || f != nil {
		return false, f != nil, haltAt(ctx, tx, f, err)
	}
	done := next == 0 || next > round.TargetSeq
	if done {
		next = round.TargetSeq + 1
	}
	if err := New().AdvanceAuditChainCheck(ctx, tx, AdvanceAuditChainCheckParams{NextSeq: next, Finished: done}); err != nil {
		return false, false, fmt.Errorf("save the check of the audit chain: %w", err)
	}
	if !done {
		return true, false, nil
	}
	return false, false, signalCheck(ctx, tx, AuditChainCheckRow{})
}

// startCheck begins a round of the check up to the chain's head as it is now.
func startCheck(ctx context.Context, tx pg.Tx) error {
	head, _, err := headOf(ctx, tx)
	if err != nil {
		return err
	}
	if err := New().StartAuditChainCheck(ctx, tx, head.Seq); err != nil {
		return fmt.Errorf("start the check of the audit chain: %w", err)
	}
	return nil
}

// signalCheck raises the overdue alert for a round still running a day after it began, and resolves it otherwise.
func signalCheck(ctx context.Context, tx pg.Tx, round AuditChainCheckRow) error {
	if !round.Overdue {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: chainCheckSignal, Key: "platform", Severity: "info", Resolved: true})
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: chainCheckSignal, Key: "platform", Severity: "warning",
		Fields: map[string]string{"next_seq": strconv.FormatInt(round.NextSeq, 10), "target_seq": strconv.FormatInt(round.TargetSeq, 10)},
	})
}

// verifyBatch checks up to chainBatch links from seq from (checkedLinks). It returns the first fault, or the seq to go
// on from, 0 when the batch reached the head.
func verifyBatch(ctx context.Context, tx pg.Tx, from int64) (*fault, int64, error) {
	links, _, f, err := checkedLinks(ctx, tx, from)
	switch {
	case err != nil || f != nil:
		return f, 0, err
	case len(links) < chainBatch:
		return nil, 0, nil
	}
	return nil, from + chainBatch, nil
}

// checkedLinks reads up to chainBatch links from seq from with their entries and checks them: each follows the link
// before it, the entry at its place in the log's (at, id) order is its entry, and that entry still hashes to it. An
// entry behind the head that was never linked takes some link's place, so it is found too. It returns the links that
// hold, each with its entry, up to the first fault, and that fault.
func checkedLinks(ctx context.Context, tx pg.Tx, from int64) ([]AuditChainLinksRow, []audit.Entry, *fault, error) {
	q := New()
	prev, after := genesis, page.Cursor{}
	if from > 1 {
		before, err := q.AuditChainLinks(ctx, tx, AuditChainLinksParams{Seq: from - 1, N: 1})
		if err != nil {
			return nil, nil, nil, fmt.Errorf("read link %d of the audit chain: %w", from-1, err)
		}
		if len(before) == 0 || before[0].Seq != from-1 { // a link removed from behind the batch
			return nil, nil, &fault{"broken", from - 1, uuid.UUID{}}, nil
		}
		prev, after = before[0].Hash, page.Cursor{At: before[0].At, ID: before[0].EntryID}
	}
	links, err := q.AuditChainLinks(ctx, tx, AuditChainLinksParams{Seq: from, N: chainBatch})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read the audit chain from %d: %w", from, err)
	}
	entries, err := audit.Since(ctx, tx, after, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), len(links))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read the audit entries from link %d: %w", from, err)
	}
	for i, l := range links {
		f, err := checkLink(l, prev, entries[i:])
		if err != nil {
			return nil, nil, nil, err
		}
		if f.kind != "" {
			return links[:i], entries[:i], &f, nil
		}
		prev = l.Hash
	}
	return links, entries, nil, nil
}

// checkLink checks link l, which follows the link whose hash is prev, against the entries from its place in the log
// on; the zero fault is none.
func checkLink(l AuditChainLinksRow, prev []byte, entries []audit.Entry) (fault, error) {
	if !bytes.Equal(l.PrevHash, prev) {
		return fault{"broken", l.Seq, l.EntryID}, nil
	}
	if len(entries) == 0 {
		return fault{"missing", l.Seq, l.EntryID}, nil
	}
	e := entries[0]
	if e.ID != l.EntryID {
		if behind(l, e) { // the link's entry is gone from its place: removed, or moved in time
			return fault{"missing", l.Seq, l.EntryID}, nil
		}
		return fault{"late", l.Seq, e.ID}, nil // an entry the chain passed over
	}
	hash, err := linkHash(prev, e)
	switch {
	case err != nil:
		return fault{}, err
	case !bytes.Equal(hash, l.Hash):
		return fault{"mismatch", l.Seq, l.EntryID}, nil
	case !e.At.Equal(l.At): // the hash covers the entry's time, not the link's copy that places the next entries
		return fault{"broken", l.Seq, l.EntryID}, nil
	}
	return fault{}, nil
}

// behind reports whether entry e comes after link l in (at, id) order.
func behind(l AuditChainLinksRow, e audit.Entry) bool {
	if !e.At.Equal(l.At) {
		return e.At.After(l.At)
	}
	return bytes.Compare(e.ID[:], l.EntryID[:]) > 0
}

// haltAt records f, if there is one, with its audit entry and a critical alert, in tx; err passes through.
func haltAt(ctx context.Context, tx pg.Tx, f *fault, err error) error {
	if err != nil || f == nil {
		return err
	}
	if err := New().InsertAuditChainFault(ctx, tx, InsertAuditChainFaultParams{Kind: f.kind, Seq: f.seq, EntryID: f.entry}); err != nil {
		return fmt.Errorf("record the audit chain's fault: %w", err)
	}
	seq := strconv.FormatInt(f.seq, 10)
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: chainActor, Action: "audit_chain.halt",
		Target: audit.Target{Module: "operations", Kind: "audit_chain", ID: seq},
		Detail: map[string]any{"fault": f.kind, "seq": f.seq, "entry_id": f.entry.String()},
	}); err != nil {
		return err
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: chainFaultSignal, Key: "platform", Severity: "critical",
		Fields: map[string]string{"fault": f.kind, "seq": seq, "entry": f.entry.String()},
	})
}

// chainArgs is one run of the chaining, or with Whole of the check of the whole chain.
type chainArgs struct {
	Whole bool `json:"whole,omitempty"`
}

// Kind implements river.JobArgs.
func (chainArgs) Kind() string { return "operations.audit_chain" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run of each at a time, on the housekeeping queue.
func (chainArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// chainWork is one run of the job. A failed run is logged and not retried: the next comes on its own, and a retrying
// job would keep it from being inserted.
func chainWork(db *pg.DB, scope pg.Scope, logger *slog.Logger) func(context.Context, *river.Job[chainArgs]) error {
	return func(ctx context.Context, job *river.Job[chainArgs]) error {
		if job.Args.Whole {
			if _, err := VerifyAuditChain(ctx, db, scope); err != nil {
				logger.Error("operations: the check of the audit chain stopped", "err", err)
			}
		} else if _, err := ChainAudit(ctx, db, scope); err != nil {
			logger.Error("operations: the audit chain stopped", "err", err)
		}
		return nil
	}
}

// RegisterAuditChain adds the chaining and the check of the whole chain, each every minute, to queue, before it
// starts, on whichever node leads the queue; the check starts a round once a day and carries it on a few batches a
// run. scope is the platform's, which only cmd/ builds.
func RegisterAuditChain(db *pg.DB, scope pg.Scope, queue *pg.Queue, logger *slog.Logger) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(chainWork(db, scope, logger))); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return chainArgs{} })
	queue.Every(time.Minute, func() river.JobArgs { return chainArgs{Whole: true} })
	return nil
}
