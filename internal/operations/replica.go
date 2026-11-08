package operations

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// The copy of the audit chain to a bucket outside the deployment (docs/domains/operations.md「审计」). Its invariant: every
// link up to the replicated seq is in an object there whose bytes never change. An object is the links first..last of
// one batch, each line a link with the fields its hash covers, so the bucket alone is enough to recompute the chain. The
// copy reads a batch in a transaction, writes it with no transaction open, then moves the replicated seq on from where
// it read it; a run that fails between the two writes the same bytes to the same key next time, which the bucket
// already has. A key that holds other bytes is never written over: that is the tampering the copy exists to expose.

const (
	// replicaKeys is where the objects are: <first seq>-<last seq>.jsonl, both 20 digits, so the keys sort as the
	// chain does. A run that read more links than a failed one wrote makes a second object over the same first seq;
	// the links they share are the same bytes.
	replicaKeys = "audit-chain/v1/"
	// replicaObjects bounds one run, like chainCheckBatches; the next run carries on a minute later.
	replicaObjects = 20
	// replicaTimeout bounds what one object may wait on the bucket, within the queue's job deadline.
	replicaTimeout = time.Minute
	// replicaLagAfter is how long a link may wait to be copied before the lag is alerted: a bucket that is down for a
	// few minutes is not news, one that is down for ten is.
	replicaLagAfter = 10 * time.Minute
)

// The alerts of the copy: links waiting longer than replicaLagAfter (warning, resolved when the copy catches up), and
// a key the bucket holds with other bytes (critical, keyed by the key, until the owner closes it).
const (
	replicaLagSignal      = "operations.audit_replica_lag"
	replicaConflictSignal = "operations.audit_replica_conflict"
)

// ReplicaStore is the bucket the chain is copied to. Its credentials write new objects and read them, and can neither
// overwrite nor delete one: the bucket's versioning and object lock keep what was written.
type ReplicaStore interface {
	Get(ctx context.Context, key string) (*objectstore.Object, error)
	Put(ctx context.Context, up objectstore.Upload) (objectstore.ObjectInfo, error)
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
func ReplicateAuditChain(ctx context.Context, db *pg.DB, scope pg.Scope, store ReplicaStore) (int, error) {
	n, err := replicate(ctx, db, scope, store)
	return n, errors.Join(err, db.Tx(ctx, scope, func(tx pg.Tx) error { return signalReplicaLag(ctx, tx) }))
}

// replicate is the copying of ReplicateAuditChain.
func replicate(ctx context.Context, db *pg.DB, scope pg.Scope, store ReplicaStore) (int, error) {
	copied := 0
	for range replicaObjects {
		var b replicaBatch
		var full bool
		if err := db.ReadTx(ctx, scope, func(tx pg.Tx) (err error) { b, full, err = nextReplicaBatch(ctx, tx); return err }); err != nil || b.key == "" {
			return copied, err
		}
		if err := writeReplica(ctx, store, b); err != nil {
			return copied, errors.Join(err, raiseConflict(ctx, db, scope, b, err))
		}
		var moved int64
		if err := db.Tx(ctx, scope, func(tx pg.Tx) (err error) {
			moved, err = New().AdvanceAuditReplica(ctx, tx, AdvanceAuditReplicaParams{LastSeq: b.last, ObjectKey: b.key, FromSeq: b.from})
			return err
		}); err != nil {
			return copied, fmt.Errorf("save the audit replica at %d: %w", b.last, err)
		}
		if moved == 0 { // another run copied it first, and carries on from there
			return copied, nil
		}
		copied += int(b.last - b.from)
		if !full {
			return copied, nil
		}
	}
	return copied, nil
}

// nextReplicaBatch reads the links after the replicated seq that check against the log and encodes them, and reports
// whether the batch is full, so more may follow. A batch with no key is nothing to copy; a fault at the first link is
// an error.
func nextReplicaBatch(ctx context.Context, tx pg.Tx) (replicaBatch, bool, error) {
	state, err := New().AuditReplicaState(ctx, tx)
	if err != nil {
		return replicaBatch{}, false, fmt.Errorf("read the audit replica: %w", err)
	}
	var from int64
	if len(state) > 0 {
		from = state[0].ReplicatedSeq
	}
	links, entries, f, err := checkedLinks(ctx, tx, from+1)
	switch {
	case err != nil:
		return replicaBatch{}, false, err
	case len(links) == 0 && f != nil:
		return replicaBatch{}, false, fmt.Errorf("link %d does not check (%s), so it is not copied", f.seq, f.kind)
	case len(links) == 0:
		return replicaBatch{}, false, nil
	}
	var body bytes.Buffer
	for i, l := range links {
		line, err := jsonx.MarshalCanonical(map[string]any{
			"seq": l.Seq, "prev_hash": hex.EncodeToString(l.PrevHash), "hash": hex.EncodeToString(l.Hash),
			"entry": entryFields(entries[i]),
		})
		if err != nil {
			return replicaBatch{}, false, fmt.Errorf("encode link %d: %w", l.Seq, err)
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	last := links[len(links)-1].Seq
	key := fmt.Sprintf("%s%020d-%020d.jsonl", replicaKeys, from+1, last)
	return replicaBatch{from: from, last: last, key: key, body: body.Bytes()}, f == nil && len(links) == chainBatch, nil
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
	if errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	if err != nil {
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

// raiseConflict alerts err when it is a conflict; the copy stays where it is, so the lag follows.
func raiseConflict(ctx context.Context, db *pg.DB, scope pg.Scope, b replicaBatch, err error) error {
	if !errors.Is(err, errReplicaConflict) {
		return nil
	}
	return db.Tx(ctx, scope, func(tx pg.Tx) error {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
			Kind: replicaConflictSignal, Key: b.key, Severity: "critical",
			Fields: map[string]string{"first_seq": strconv.FormatInt(b.from+1, 10), "last_seq": strconv.FormatInt(b.last, 10)},
		})
	})
}

// replicaLag is how far the copy is behind the chain, by the database's clock.
type replicaLag struct {
	AuditReplicaLagRow
	behind bool // the oldest link not copied was chained more than replicaLagAfter ago
}

// readReplicaLag reads the copy's lag.
func readReplicaLag(ctx context.Context, tx pg.Tx) (replicaLag, error) {
	row, err := New().AuditReplicaLag(ctx, tx)
	if err != nil {
		return replicaLag{}, fmt.Errorf("read the audit replica's lag: %w", err)
	}
	return replicaLag{row, row.PendingSince != nil && row.Now.Sub(*row.PendingSince) > replicaLagAfter}, nil
}

// signalReplicaLag raises the lag when a link has waited longer than replicaLagAfter to be copied, and resolves it
// otherwise.
func signalReplicaLag(ctx context.Context, tx pg.Tx) error {
	lag, err := readReplicaLag(ctx, tx)
	if err != nil {
		return err
	}
	if !lag.behind {
		return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: replicaLagSignal, Key: "platform", Severity: "info", Resolved: true})
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: replicaLagSignal, Key: "platform", Severity: "warning",
		Fields: map[string]string{
			"behind_links":   strconv.FormatInt(lag.HeadSeq-lag.ReplicatedSeq, 10),
			"behind_seconds": strconv.FormatInt(int64(lag.Now.Sub(*lag.PendingSince)/time.Second), 10),
		},
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
func replicaWork(db *pg.DB, scope pg.Scope, store ReplicaStore, logger *slog.Logger) func(context.Context, *river.Job[replicaArgs]) error {
	return func(ctx context.Context, _ *river.Job[replicaArgs]) error {
		if _, err := ReplicateAuditChain(ctx, db, scope, store); err != nil {
			logger.Error("operations: the copy of the audit chain stopped", "err", err)
		}
		return nil
	}
}

// RegisterAuditReplica adds the copy of the chain to store, every minute, to queue, on whichever node leads it. A
// failed run is logged and not retried, like the chaining: the next comes on its own, and the lag alert reports a copy
// that keeps failing. scope is the platform's, which only cmd/ builds.
func RegisterAuditReplica(db *pg.DB, scope pg.Scope, queue *pg.Queue, store ReplicaStore, logger *slog.Logger) error {
	if err := river.AddWorkerSafely(queue.Workers(), river.WorkFunc(replicaWork(db, scope, store, logger))); err != nil {
		return fmt.Errorf("operations: %w", err)
	}
	queue.Every(time.Minute, func() river.JobArgs { return replicaArgs{} })
	return nil
}
