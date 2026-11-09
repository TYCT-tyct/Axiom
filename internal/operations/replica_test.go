package operations_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/operations"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
)

// bucket is a replica store in memory. Like SeaweedFS it ignores If-None-Match, so only the copy's own read of a key keeps
// it from overwriting one; down fails every request.
type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	down    bool
	lost    bool   // a write is stored but its answer never arrives, as when the connection drops after the upload
	target  string // the replica's target, its endpoint and bucket
}

func newBucket() *bucket {
	return &bucket{objects: map[string][]byte{}, target: "https://s3.example.com/a"}
}

func (b *bucket) Get(ctx context.Context, key string) (*objectstore.Object, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return nil, errors.New("bucket down")
	}
	body, ok := b.objects[key]
	if !ok {
		return nil, &objectstore.Error{Status: 404}
	}
	return &objectstore.Object{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (b *bucket) Put(ctx context.Context, up objectstore.Upload) (objectstore.ObjectInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return objectstore.ObjectInfo{}, errors.New("bucket down")
	}
	if !up.NoOverwrite {
		return objectstore.ObjectInfo{}, errors.New("an unconditional write")
	}
	body, err := io.ReadAll(up.Body)
	b.objects[up.Key] = body
	b.puts++
	if b.lost {
		return objectstore.ObjectInfo{}, errors.New("connection reset after the upload")
	}
	return objectstore.ObjectInfo{Key: up.Key, Size: int64(len(body))}, err
}

// List pages the keys in order, after StartAfter and under Prefix, as S3's list-type=2 does.
func (b *bucket) List(ctx context.Context, opts objectstore.ListOptions) (objectstore.ListPage, error) {
	b.mu.Lock()
	down := b.down
	b.mu.Unlock()
	if down {
		return objectstore.ListPage{}, errors.New("bucket down")
	}
	var page objectstore.ListPage
	for _, key := range b.keys() {
		if strings.HasPrefix(key, opts.Prefix) && key > opts.StartAfter && len(page.Objects) < opts.MaxKeys {
			page.Objects = append(page.Objects, objectstore.ObjectInfo{Key: key})
		}
	}
	return page, nil
}

// keys are the objects' keys in order.
func (b *bucket) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	keys := make([]string, 0, len(b.objects))
	for k := range b.objects {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (db opsDB) replicate(t *testing.T, b *bucket) (int, error) {
	t.Helper()
	return operations.ReplicateAuditChain(t.Context(), db.DB, platformScope, operations.Replica{Store: b, Target: b.target})
}

func objectKey(first, last int) string {
	return fmt.Sprintf("audit-chain/v1/%020d-%020d.jsonl", first, last)
}

// replicaLink is one line of a replica object.
type replicaLink struct {
	Seq      int64  `json:"seq"`
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
	Entry    []any  `json:"entry"`
}

// verifyReplica recomputes the chain from the bucket alone, as a verifier outside Axiom would: each line's hash is
// SHA-256 of the previous line's hash and the entry's canonical JSON, the first following 32 zero bytes. It returns the
// entry IDs in the chain's order.
func verifyReplica(t *testing.T, b *bucket) []string {
	t.Helper()
	prev, ids := make([]byte, sha256.Size), []string{}
	for _, key := range b.keys() {
		lines := bufio.NewScanner(bytes.NewReader(b.objects[key]))
		for lines.Scan() {
			var link replicaLink
			if err := jsonx.Unmarshal(lines.Bytes(), &link); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
			prev = checkReplicaLink(t, key, link, int64(len(ids))+1, prev)
			ids = append(ids, link.Entry[1].(string)) //nolint:forcetypeassert // the encoding's second field is the entry ID
		}
	}
	return ids
}

// checkReplicaLink checks that link is seq after the link whose hash is prev and hashes as it says; it returns its hash.
func checkReplicaLink(t *testing.T, key string, link replicaLink, seq int64, prev []byte) []byte {
	t.Helper()
	if link.Seq != seq || link.PrevHash != hex.EncodeToString(prev) {
		t.Fatalf("%s: link %d follows %s, want %d after %x", key, link.Seq, link.PrevHash, seq, prev)
	}
	encoded, err := jsonx.MarshalCanonical(link.Entry)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append(slices.Clone(prev), encoded...))
	if hex.EncodeToString(sum[:]) != link.Hash {
		t.Fatalf("%s: link %d does not hash to %s", key, link.Seq, link.Hash)
	}
	return sum[:]
}

// TestAuditReplicaCopiesTheChain: each run copies the links chained since the last, an object of at most a batch at a
// time, and the bucket alone recomputes the chain the database has; a run with nothing new writes nothing.
func TestAuditReplicaCopiesTheChain(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	superuser(t, db, `INSERT INTO platform.audit_log (at, actor_kind, actor_id, action, module, target_kind, target_id,
		detail) SELECT now() - interval '10 minutes' + g * interval '1 millisecond', 'system', 'test.chain', 'thing.touch',
		'operations', 'thing', 't' || g, '{"n": "über"}' FROM generate_series(1, 1001) g`)
	db.chain(t, time.Minute)
	b := newBucket()
	if n, err := db.replicate(t, b); n != 1001 || err != nil {
		t.Fatalf("copied %d: %v", n, err)
	}
	db.backdated(t, "3 minutes")
	db.chain(t, time.Minute)
	if n, err := db.replicate(t, b); n != 1 || err != nil {
		t.Fatalf("copied %d: %v", n, err)
	}
	if n, err := db.replicate(t, b); n != 0 || err != nil || b.puts != 4 {
		t.Fatalf("copied %d again with %d writes: %v", n, b.puts, err)
	}
	want := []string{objectKey(1, 500), objectKey(501, 1000), objectKey(1001, 1001), objectKey(1002, 1002)}
	if keys := b.keys(); !slices.Equal(keys, want) {
		t.Errorf("objects %v, want %v", keys, want)
	}
	if got := strings.Join(verifyReplica(t, b), ","); got != db.linked(t) {
		t.Error("the replica's chain is not the database's")
	}
	db.expect(t, "the copy", `SELECT replicated_seq || ' ' || object_key FROM operations.audit_replica`, "1002 "+objectKey(1002, 1002))
}

// TestAuditReplicaNeverOverwrites: a key the bucket holds with other bytes is a conflict that is alerted and stops the copy, the bucket
// keeping what it had though it does not enforce If-None-Match, and keeps stopping it after the chain has grown: the
// conflicting batch stays planned, so no batch named after the later head steps over it; a link waiting too long raises
// the lag, which the run that copies it resolves.
func TestAuditReplicaNeverOverwrites(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	b := newBucket()
	if n, err := db.replicate(t, b); n != 2 || err != nil {
		t.Fatalf("copied %d: %v", n, err)
	}

	db.backdated(t, "3 minutes")
	db.chain(t, time.Minute)
	b.objects[objectKey(3, 3)] = []byte("other bytes")
	superuser(t, db, `UPDATE operations.audit_chain SET chained_at = now() - interval '11 minutes' WHERE seq = 3`)
	n, err := db.replicate(t, b)
	if n != 0 || !errors.Is(err, operations.ErrReplicaConflict) || string(b.objects[objectKey(3, 3)]) != "other bytes" {
		t.Errorf("copied %d over a conflict: %v", n, err)
	}
	db.expect(t, "the conflict", `SELECT string_agg((body->>'severity') || ' ' || (body->>'key'), ',') FROM platform.alert_signals
		WHERE body->>'kind' = 'operations.audit_replica_conflict'`, "critical "+operations.ReplicaAlertKey(b.target)+"/"+objectKey(3, 3))
	db.expect(t, "the copy", `SELECT replicated_seq::text FROM operations.audit_replica`, "2")
	lag := `SELECT string_agg((body->>'severity') || ' ' || coalesce(body->'fields'->>'behind_links', '-'), ',' ORDER BY id)
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_replica_lag'`
	db.expect(t, "the lag", lag, "info -,warning 1")

	db.backdated(t, "2 minutes")
	db.chain(t, time.Minute)
	if n, err := db.replicate(t, b); n != 0 || !errors.Is(err, operations.ErrReplicaConflict) {
		t.Errorf("copied %d after the chain grew past a conflict: %v", n, err)
	}
	if keys := b.keys(); !slices.Equal(keys, []string{objectKey(1, 2), objectKey(3, 3)}) {
		t.Errorf("objects %v after the chain grew past a conflict", keys)
	}
	db.expect(t, "the copy after the chain grew", `SELECT replicated_seq::text FROM operations.audit_replica`, "2")
	db.expect(t, "the lag after the chain grew", lag, "info -,warning 1,warning 2")

	delete(b.objects, objectKey(3, 3))
	if n, err := db.replicate(t, b); n != 2 || err != nil {
		t.Errorf("copied %d once the conflict was cleared: %v", n, err)
	}
	if keys := b.keys(); !slices.Equal(keys, []string{objectKey(1, 2), objectKey(3, 3), objectKey(4, 4)}) {
		t.Errorf("objects %v once the conflict was cleared", keys)
	}
	db.expect(t, "the recovery", lag, "info -,warning 1,warning 2,info -")
}

// TestAuditReplicaHoldsItsPlace: a bucket that is down and an entry changed after it was chained both leave the copy
// where it was, with nothing written.
func TestAuditReplicaHoldsItsPlace(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	b := newBucket()
	if _, err := db.replicate(t, b); err != nil {
		t.Fatal(err)
	}
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	b.down = true
	if n, err := db.replicate(t, b); n != 0 || err == nil {
		t.Errorf("copied %d to a bucket that is down: %v", n, err)
	}
	b.down = false
	superuser(t, db, `UPDATE platform.audit_log SET detail = '{"n": 2}' WHERE id = (SELECT entry_id FROM operations.audit_chain WHERE seq = 2)`)
	if n, err := db.replicate(t, b); n != 0 || err == nil || !strings.Contains(err.Error(), "the planned links 2 to 2 no longer all check") {
		t.Errorf("copied %d of a changed entry: %v", n, err)
	}
	db.expect(t, "the copy", `SELECT replicated_seq::text FROM operations.audit_replica`, "1")
	if keys := b.keys(); len(keys) != 1 {
		t.Errorf("objects %v", keys)
	}
}

// TestAuditReplicaLagOutlivesTheDeadline: a run whose context ends before it could copy anything, as the job's deadline
// ends a run stuck behind a backlog, still records the lag.
func TestAuditReplicaLagOutlivesTheDeadline(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	superuser(t, db, `UPDATE operations.audit_chain SET chained_at = now() - interval '11 minutes'`)
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	if n, err := operations.ReplicateAuditChain(ended, db.DB, platformScope, operations.Replica{Store: newBucket(), Target: "t"}); n != 0 || !errors.Is(err, context.Canceled) {
		t.Errorf("copied %d after the deadline: %v", n, err)
	}
	db.expect(t, "the lag", `SELECT string_agg((body->>'severity') || ' ' || (body->'fields'->>'behind_links'), ',')
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_replica_lag'`, "warning 1")
}

// TestAuditReplicaFollowsItsBucket: a deployment pointed at another bucket copies the whole chain there from the first
// link, not from where the first bucket had got, so the new bucket alone recomputes the chain; the first bucket keeps
// its own progress.
func TestAuditReplicaFollowsItsBucket(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	first := newBucket()
	if n, err := db.replicate(t, first); n != 2 || err != nil {
		t.Fatalf("copied %d to the first bucket: %v", n, err)
	}
	db.backdated(t, "3 minutes")
	db.chain(t, time.Minute)
	second := newBucket()
	second.target = "https://s3.example.com/b"
	if n, err := db.replicate(t, second); n != 3 || err != nil {
		t.Fatalf("copied %d to the new bucket: %v", n, err)
	}
	if got := strings.Join(verifyReplica(t, second), ","); got != db.linked(t) {
		t.Error("the new bucket's chain is not the database's")
	}
	db.expect(t, "the progress", `SELECT string_agg(target || ' ' || replicated_seq, ',' ORDER BY target) FROM operations.audit_replica`,
		"https://s3.example.com/a 2,https://s3.example.com/b 3")
}

// TestAuditReplicaKeepsToOneHistory: a database restored to an earlier point carries on from its own copy while the
// bucket holds the abandoned history beyond it. A batch cut at other boundaries than that history's objects is a
// conflict, alerted, with nothing written, and stays one after the chain has grown; an object that starts right after
// a batch is no overlap, and a new bucket copies the restored chain from its first link.
func TestAuditReplicaKeepsToOneHistory(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "6 minutes")
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	b := newBucket()
	if n, err := db.replicate(t, b); n != 2 || err != nil {
		t.Fatalf("copied %d: %v", n, err)
	}
	b.objects[objectKey(3, 5)] = []byte("the abandoned history\n")
	db.backdated(t, "4 minutes")
	db.backdated(t, "3 minutes")
	db.chain(t, time.Minute)
	for range 2 {
		if n, err := db.replicate(t, b); n != 0 || !errors.Is(err, operations.ErrReplicaConflict) {
			t.Fatalf("copied %d over another history: %v", n, err)
		}
		db.backdated(t, "2 minutes")
		db.chain(t, time.Minute)
	}
	if keys := b.keys(); !slices.Equal(keys, []string{objectKey(1, 2), objectKey(3, 5)}) {
		t.Errorf("objects %v over another history", keys)
	}
	db.expect(t, "the conflict", `SELECT string_agg(DISTINCT body->>'key', ',') FROM platform.alert_signals
		WHERE body->>'kind' = 'operations.audit_replica_conflict'`, operations.ReplicaAlertKey(b.target)+"/"+objectKey(3, 4))

	fresh := newBucket()
	fresh.target = "https://s3.example.com/fresh"
	fresh.objects[objectKey(7, 9)] = []byte("starts after the chain\n")
	if n, err := db.replicate(t, fresh); n != 6 || err != nil {
		t.Fatalf("copied %d to a new bucket: %v", n, err)
	}
	if keys := fresh.keys(); !slices.Equal(keys, []string{objectKey(1, 6), objectKey(7, 9)}) {
		t.Errorf("objects %v in the new bucket", keys)
	}
}

// TestAuditReplicaFinishesItsPlan: a batch whose upload landed but whose answer was lost is finished as planned after
// the chain has grown: the next run reads back the same key and bytes instead of writing an overlapping object, then
// carries on, so the objects' ranges follow one another and recompute the chain.
func TestAuditReplicaFinishesItsPlan(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	b := newBucket()
	if _, err := db.replicate(t, b); err != nil {
		t.Fatal(err)
	}
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	b.lost = true
	if n, err := db.replicate(t, b); n != 0 || err == nil {
		t.Fatalf("copied %d though the answer was lost: %v", n, err)
	}
	db.expect(t, "the plan", `SELECT replicated_seq || ' ' || pending_last FROM operations.audit_replica`, "1 2")
	b.lost = false
	db.backdated(t, "3 minutes")
	db.chain(t, time.Minute)
	if n, err := db.replicate(t, b); n != 2 || err != nil || b.puts != 3 {
		t.Fatalf("copied %d with %d writes after the lost answer: %v", n, b.puts, err)
	}
	if keys := b.keys(); !slices.Equal(keys, []string{objectKey(1, 1), objectKey(2, 2), objectKey(3, 3)}) {
		t.Errorf("objects %v, want consecutive ranges", keys)
	}
	if got := strings.Join(verifyReplica(t, b), ","); got != db.linked(t) {
		t.Error("the replica's chain is not the database's")
	}
	db.expect(t, "the copy", `SELECT replicated_seq || ' ' || coalesce(pending_last::text, '-') FROM operations.audit_replica`, "3 -")
}

// TestAuditReplicaReadsBackALostSave: a key the bucket holds with the same bytes, as after a run that wrote and then
// lost its progress, is passed over without a write.
func TestAuditReplicaReadsBackALostSave(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	b := newBucket()
	if _, err := db.replicate(t, b); err != nil {
		t.Fatal(err)
	}
	superuser(t, db, `DELETE FROM operations.audit_replica`)
	if n, err := db.replicate(t, b); n != 2 || err != nil || b.puts != 1 {
		t.Errorf("copied %d with %d writes after a lost save: %v", n, b.puts, err)
	}
}

// TestAuditReplicaCopiesOnlyWhatChecks: a first link whose entry was changed after it was chained is neither planned
// nor written.
func TestAuditReplicaCopiesOnlyWhatChecks(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	superuser(t, db, `UPDATE platform.audit_log SET detail = '{"n": 2}'`)
	b := newBucket()
	if n, err := db.replicate(t, b); n != 0 || err == nil || !strings.Contains(err.Error(), "link 1 does not check (mismatch)") {
		t.Errorf("copied %d of a changed entry: %v", n, err)
	}
	db.expect(t, "the copy", `SELECT count(*)::text FROM operations.audit_replica`, "0")
	if keys := b.keys(); len(keys) != 0 {
		t.Errorf("objects %v", keys)
	}
}

// TestAuditReplicaAlertsPerBucket: while some nodes copy to one bucket and some to another, as when the deployment is
// moved, each bucket's alerts are its own: the bucket that caught up resolves its own lag and not the other's, and the
// same conflicting key in both is two alerts.
func TestAuditReplicaAlertsPerBucket(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	superuser(t, db, `UPDATE operations.audit_chain SET chained_at = now() - interval '11 minutes'`)
	first, second := newBucket(), newBucket()
	second.target, second.down = "https://s3.example.com/b", true
	if _, err := db.replicate(t, second); err == nil {
		t.Fatal("copied to a bucket that is down")
	}
	if _, err := db.replicate(t, first); err != nil {
		t.Fatal(err)
	}
	last := `SELECT (SELECT body->>'severity' || ' ' || coalesce(body->>'resolved', 'false') FROM platform.alert_signals
		WHERE body->>'kind' = 'operations.audit_replica_lag' AND body->>'key' = '%s' ORDER BY id DESC LIMIT 1)`
	db.expect(t, "the lagging bucket's lag", fmt.Sprintf(last, operations.ReplicaAlertKey(second.target)), "warning false")
	db.expect(t, "the bucket that caught up", fmt.Sprintf(last, operations.ReplicaAlertKey(first.target)), "info true")

	second.down = false
	if n, err := db.replicate(t, second); n != 1 || err != nil {
		t.Fatalf("copied %d to the second bucket once it was back: %v", n, err)
	}
	db.backdated(t, "4 minutes")
	db.chain(t, time.Minute)
	for _, b := range []*bucket{first, second} {
		b.objects[objectKey(2, 2)] = []byte("other bytes")
		if _, err := db.replicate(t, b); !errors.Is(err, operations.ErrReplicaConflict) {
			t.Fatalf("%s: %v", b.target, err)
		}
	}
	db.expect(t, "the conflicts", `SELECT count(DISTINCT body->>'key')::text || ' ' || count(*) FILTER (WHERE body->'fields'->>'object' = '`+
		objectKey(2, 2)+`')::text FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_replica_conflict'`, "2 2")
}
