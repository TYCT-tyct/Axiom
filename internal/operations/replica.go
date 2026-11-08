package operations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// The copy of the audit chain to a bucket outside the deployment (docs/domains/operations.md「审计」). Its invariants: every
// link up to the replicated seq is in an object there whose bytes never change; the objects' ranges follow one another
// without overlap; and a batch, once planned, is the next and only one written until it is confirmed. An object is the
// links first..last of one batch, each line a link with the fields its hash covers, so the bucket alone is enough to
// recompute the chain. The copy plans a batch in a transaction (records its range), writes it with no transaction open,
// then confirms it from the state it read; a run that fails between the two finds the same plan next time and writes
// the same bytes to the same key, which the bucket already has, however the chain has grown since. A key that holds
// other bytes is never written over and, its batch staying planned, stops the copy: that is the tampering the copy
// exists to expose, and nothing may step over it.

const (
	// replicaKeys is where the objects are: <first seq>-<last seq>.jsonl, both 20 digits, so the keys sort as the
	// chain does.
	replicaKeys = "audit-chain/v1/"
	// replicaObjects bounds one run, like chainCheckBatches; the next run carries on a minute later.
	replicaObjects = 20
	// replicaTimeout bounds what one object may wait on the bucket, within the queue's job deadline.
	replicaTimeout = time.Minute
	// replicaLagAfter is how long a link may wait to be copied before the lag is alerted: a bucket that is down for a
	// few minutes is not news, one that is down for ten is.
	replicaLagAfter = 10 * time.Minute
	// replicaLagTimeout bounds judging the lag after the copy, which has its own time so that a copy cut off by the
	// job's deadline is still reported; one short transaction needs far less.
	replicaLagTimeout = 10 * time.Second
)

// The alerts of the copy, each of one target as its progress is: links waiting longer than replicaLagAfter (warning,
// keyed by the target, resolved when the copy to that target catches up), and a key a bucket holds with other bytes
// (critical, keyed by the target and the object's key, until the owner closes it). Keyed by target, a node that copies
// to another bucket, as while the deployment is moved from one to another, neither resolves nor merges this one's.
const (
	replicaLagSignal      = "operations.audit_replica_lag"
	replicaConflictSignal = "operations.audit_replica_conflict"
	// targetFieldMax is the longest target an alert carries as a field, the alert fields' limit; a longer one is
	// still told apart by the key.
	targetFieldMax = 512
)

// replicaAlertKey is target's part of an alert's key: the hex of the first 16 bytes of its SHA-256, a fixed length
// within the alerts' 256-character key whatever the target's.
func replicaAlertKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:16])
}

// replicaAlertFields are fields with the target added, when it fits a field.
func replicaAlertFields(target string, fields map[string]string) map[string]string {
	if len(target) <= targetFieldMax {
		fields["target"] = target
	}
	return fields
}

// ReplicaStore is the bucket the chain is copied to. Its credentials write new objects, read them and list the bucket
// (without which S3 answers a missing key 403, not 404), and can neither overwrite nor delete one: the bucket's
// versioning and object lock keep what was written.
type ReplicaStore interface {
	Get(ctx context.Context, key string) (*objectstore.Object, error)
	Put(ctx context.Context, up objectstore.Upload) (objectstore.ObjectInfo, error)
}

// Replica is the bucket the chain is copied to: Store reaches it and Target names it, its endpoint and bucket and never
// its credentials. The copy's progress is kept per Target, so a deployment pointed at another bucket copies the whole
// chain there from the first link instead of carrying on from where the old bucket had got.
type Replica struct {
	Store  ReplicaStore
	Target string
}

// errReplicaConflict is a key that holds other bytes than the links it is named for.
var errReplicaConflict = errors.New("the replica holds other bytes")

// replicaBatch is one object to write: the links first..last.
type replicaBatch struct {
	from, last int64 // from is the replicated seq it was read at
	key        string
	body       []byte
}

// ReplicateAuditChain copies the links chained since the last copy, an object of at most chainBatch links at a time
// and at most replicaObjects a run, then raises or resolves the lag. It returns how many links it copied. It copies
// only links that check against the log (checkedLinks): one that does not is the chain's own fault to report, and
// stays uncopied.
func ReplicateAuditChain(ctx context.Context, db *pg.DB, scope pg.Scope, r Replica) (int, error) {
	n, err := replicate(ctx, db, scope, r)
	// The lag is judged even when the copy ran out of its job's deadline, which is when it matters most: a backlog that
	// takes every run's whole deadline must still be reported.
	lagCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), replicaLagTimeout)
	defer cancel()
	return n, errors.Join(err, db.Tx(lagCtx, scope, func(tx pg.Tx) error { return signalReplicaLag(lagCtx, tx, r.Target) }))
}

// replicate is the copying of ReplicateAuditChain.
func replicate(ctx context.Context, db *pg.DB, scope pg.Scope, r Replica) (int, error) {
	copied := 0
	for range replicaObjects {
		var b replicaBatch
		// Choosing a new batch records it, so this transaction writes; it runs only short statements and calls nothing
		// outside the database.
		if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) { b, err = nextReplicaBatch(ctx, tx, r.Target); return err }); err != nil || b.key == "" {
			return copied, err
		}
		if err := writeReplica(ctx, r.Store, b); err != nil {
			return copied, errors.Join(err, raiseConflict(ctx, db, scope, r.Target, b, err))
		}
		var moved int64
		if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
			moved, err = New().AdvanceAuditReplica(ctx, tx, AdvanceAuditReplicaParams{Target: r.Target, LastSeq: b.last, ObjectKey: b.key, FromSeq: b.from})
			return err
		}); err != nil {
			return copied, fmt.Errorf("save the audit replica at %d: %w", b.last, err)
		}
		if moved == 0 { // another run confirmed it first, and carries on from there
			return copied, nil
		}
		copied += int(b.last - b.from)
	}
	return copied, nil
}

// nextReplicaBatch is the batch to write to target next, encoded (nextLinks). A batch with no key is nothing to copy
// now.
func nextReplicaBatch(ctx context.Context, tx pg.Tx, target string) (replicaBatch, error) {
	from, planned, links, entries, err := nextLinks(ctx, tx, target)
	if err != nil || len(links) == 0 {
		return replicaBatch{}, err
	}
	var body bytes.Buffer
	for i, l := range links {
		line, err := jsonx.MarshalCanonical(map[string]any{
			"seq": l.Seq, "prev_hash": hex.EncodeToString(l.PrevHash), "hash": hex.EncodeToString(l.Hash),
			"entry": entryFields(entries[i]),
		})
		if err != nil {
			return replicaBatch{}, fmt.Errorf("encode link %d: %w", l.Seq, err)
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	key := fmt.Sprintf("%s%020d-%020d.jsonl", replicaKeys, from+1, planned)
	return replicaBatch{from: from, last: planned, key: key, body: body.Bytes()}, nil
}

// nextLinks is the batch to copy to target next: the links from+1 to planned with their entries. It is the batch
// planned and not yet confirmed when there is one, exactly as planned, so a retry writes the key and bytes it wrote
// before whatever the chain's head has become; otherwise the links after the replicated seq that check against the
// log, up to chainBatch, which it records in tx as the planned batch. No links is nothing to copy now: no new link, or
// another run planned first. A planned link, or the first new one, that no longer checks is an error, which the
// chain's own check explains.
func nextLinks(ctx context.Context, tx pg.Tx, target string) (from, planned int64, links []AuditChainLinksRow, entries []audit.Entry, err error) {
	q := New()
	state, err := q.AuditReplicaState(ctx, tx, target)
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("read the audit replica: %w", err)
	}
	if len(state) > 0 {
		from, planned = state[0].ReplicatedSeq, state[0].PendingLast
	}
	links, entries, f, err := checkedLinks(ctx, tx, from+1)
	switch {
	case err != nil:
		return 0, 0, nil, nil, err
	case planned > 0 && int64(len(links)) < planned-from:
		return 0, 0, nil, nil, fmt.Errorf("the planned links %d to %d no longer all check, so they are not copied", from+1, planned)
	case planned > 0:
		return from, planned, links[:planned-from], entries[:planned-from], nil
	case len(links) == 0 && f != nil:
		return 0, 0, nil, nil, fmt.Errorf("link %d does not check (%s), so it is not copied", f.seq, f.kind)
	case len(links) == 0:
		return 0, 0, nil, nil, nil
	}
	planned = links[len(links)-1].Seq
	n, err := q.PlanAuditReplica(ctx, tx, PlanAuditReplicaParams{Target: target, FromSeq: from, LastSeq: planned})
	if err != nil {
		return 0, 0, nil, nil, fmt.Errorf("plan links %d to %d of the audit replica: %w", from+1, planned, err)
	}
	if n == 0 { // another run planned first, and writes it
		return 0, 0, nil, nil, nil
	}
	return from, planned, links, entries, nil
}

// writeReplica puts b's object unless the bucket has it already. The key is read first, because a bucket may ignore
// If-None-Match (SeaweedFS does): a retry finds its own earlier write, and a key with other bytes is a conflict, never
// overwritten. NoOverwrite then covers a bucket whose policy demands the condition, and a write that raced the read.
func writeReplica(ctx context.Context, store ReplicaStore, b replicaBatch) error {
	ctx, cancel := context.WithTimeout(ctx, replicaTimeout)
	defer cancel()
	err := holds(ctx, store, b)
	if !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	_, err = store.Put(ctx, objectstore.Upload{
		Key: b.key, Body: bytes.NewReader(b.body), Size: int64(len(b.body)), ContentType: "application/x-ndjson",
		NoOverwrite: true,
	})
	if errors.Is(err, objectstore.ErrExists) {
		return holds(ctx, store, b)
	}
	if err != nil {
		return fmt.Errorf("copy the audit chain to %s: %w", b.key, err)
	}
	return nil
}

// holds is nil when the bucket has b's object with exactly its bytes, wraps ErrNotFound when it has no such key, and
// wraps errReplicaConflict when the key holds other bytes.
func holds(ctx context.Context, store ReplicaStore, b replicaBatch) error {
	object, err := store.Get(ctx, b.key)
	var refused *objectstore.Error
	switch {
	case errors.Is(err, objectstore.ErrNotFound):
		return err
	case errors.As(err, &refused) && refused.Status == http.StatusForbidden:
		// S3 answers 403, not 404, for a key that does not exist when the caller may not list the bucket, so a 403 is
		// not taken for "absent": that would write over whatever the credentials cannot see.
		return fmt.Errorf("read %s from the audit replica: the credentials need s3:GetObject and s3:ListBucket: %w", b.key, err)
	case err != nil:
		return fmt.Errorf("read %s from the audit replica: %w", b.key, err)
	}
	got, readErr := io.ReadAll(io.LimitReader(object.Body, int64(len(b.body))+1))
	if err := errors.Join(readErr, object.Body.Close()); err != nil {
		return fmt.Errorf("read %s from the audit replica: %w", b.key, err)
	}
	if !bytes.Equal(got, b.body) {
		return fmt.Errorf("%w at %s: links %d to %d", errReplicaConflict, b.key, b.from+1, b.last)
	}
	return nil
}

// raiseConflict alerts err when it is a conflict in target's bucket; the copy stays where it is, so the lag follows.
func raiseConflict(ctx context.Context, db *pg.DB, scope pg.Scope, target string, b replicaBatch, err error) error {
	if !errors.Is(err, errReplicaConflict) {
		return nil
	}
	return db.Tx(ctx, scope, func(tx pg.Tx) error {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
			Kind: replicaConflictSignal, Key: replicaAlertKey(target) + "/" + b.key, Severity: "critical",
			Fields: replicaAlertFields(target, map[string]string{
				"object": b.key, "first_seq": strconv.FormatInt(b.from+1, 10), "last_seq": strconv.FormatInt(b.last, 10),
			}),
		})
	})
}

// signalReplicaLag raises the lag when a link has waited longer than replicaLagAfter to be copied to target, by the
// database's clock, and resolves it otherwise.
func signalReplicaLag(ctx context.Context, tx pg.Tx, target string) error {
	lag, err := New().AuditReplicaLag(ctx, tx, target)
	if err != nil {
		return fmt.Errorf("read the audit replica's lag: %w", err)
	}
	if lag.PendingSince == nil || lag.Now.Sub(*lag.PendingSince) <= replicaLagAfter {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: replicaLagSignal, Key: replicaAlertKey(target), Severity: "info", Resolved: true})
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: replicaLagSignal, Key: replicaAlertKey(target), Severity: "warning",
		Fields: replicaAlertFields(target, map[string]string{
			"behind_links":   strconv.FormatInt(lag.HeadSeq-lag.ReplicatedSeq, 10),
			"behind_seconds": strconv.FormatInt(int64(lag.Now.Sub(*lag.PendingSince)/time.Second), 10),
		}),
	})
}

// replicaArgs is one run of the copy.
type replicaArgs struct{}

// Kind implements river.JobArgs.
func (replicaArgs) Kind() string { return "operations.audit_replica" }

// InsertOpts implements river.JobArgsWithInsertOpts: one run at a time, on the housekeeping queue.
func (replicaArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueArchive, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable,
		rivertype.JobStateScheduled,
	}}}
}

// replicaWork is one run of the job, logged when it fails.
func replicaWork(db *pg.DB, scope pg.Scope, r Replica, logger *slog.Logger) func(context.Context, *river.Job[replicaArgs]) error {
	return func(ctx context.Context, _ *river.Job[replicaArgs]) error {
		if _, err := ReplicateAuditChain(ctx, db, scope, r); err != nil {
			logger.Error("operations: the copy of the audit chain stopped", "err", err)
		}
		return nil
	}
}

// RegisterAuditReplica adds the copy of the chain to r, every minute, to queue, on whichever node leads it. A
// failed run is logged and not retried, like the chaining: the next comes on its own, and the lag alert reports a copy
// that keeps failing. scope is the platform's, which only cmd/ builds.
func RegisterAuditReplica(db *pg.DB, scope pg.Scope, queue *pg.Queue, r Replica, logger *slog.Logger) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(replicaWork(db, scope, r, logger))); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return replicaArgs{} })
	return nil
}
