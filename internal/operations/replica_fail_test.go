package operations

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// failingStore answers every request with err, or for Get an object whose body fails to read.
type failingStore struct {
	err      error
	badBody  bool
	putCalls int
}

func (s *failingStore) Get(context.Context, string) (*objectstore.Object, error) {
	if s.badBody {
		return &objectstore.Object{Body: io.NopCloser(iotestErrReader{})}, nil
	}
	return nil, s.err
}

func (s *failingStore) Put(context.Context, objectstore.Upload) (objectstore.ObjectInfo, error) {
	s.putCalls++
	return objectstore.ObjectInfo{}, s.err
}

type iotestErrReader struct{}

func (iotestErrReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// openFailing is a database with the module's tables, the platform scope and a context already canceled.
func openFailing(t *testing.T) (*pg.DB, pg.Scope, context.Context) {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, Module()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	return db, pg.NewScope(true, nil, nil), canceled
}

// TestReplicaAndDigestStepsFail: each step of the copy and the digest hands a failure of its database or bucket to
// its caller, and the copy writes nothing it could not first read.
func TestReplicaAndDigestStepsFail(t *testing.T) {
	t.Parallel()
	db, scope, canceled := openFailing(t)
	for name, step := range map[string]func(tx pg.Tx) error{
		"nextReplicaBatch": func(tx pg.Tx) error { _, _, err := nextReplicaBatch(canceled, tx); return err },
		"signalReplicaLag": func(tx pg.Tx) error { return signalReplicaLag(canceled, tx) },
		"digestOf":         func(tx pg.Tx) error { _, err := digestOf(canceled, tx, time.Now(), true); return err },
		"digestMoney":      func(tx pg.Tx) error { _, err := digestMoney(canceled, tx, time.Now()); return err },
		"digestChain":      func(tx pg.Tx) error { _, err := digestChain(canceled, tx); return err },
		"digestReplica":    func(tx pg.Tx) error { _, err := digestReplica(canceled, tx, true); return err },
		"AuditDigests":     func(tx pg.Tx) error { _, _, err := AuditDigests(canceled, tx, nil, 1); return err },
		"closeDigests":     func(tx pg.Tx) error { return closeDigests(canceled, tx) },
	} {
		if err := db.Tx(t.Context(), scope, step); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}

	for name, run := range map[string]func() error{
		"nextDigestDay":    func() error { _, _, err := nextDigestDay(canceled, db, scope, 0); return err },
		"saveDigest":       func() error { _, err := saveDigest(canceled, db, scope, time.Now(), AuditDigest{}); return err },
		"makeAuditDigests": func() error { _, err := makeAuditDigests(canceled, db, scope, false, 0); return err },
	} {
		if err := run(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s with a failing database: %v", name, err)
		}
	}
}

// TestWriteReplicaRefusals: the copy writes nothing it could not first read, a 403 on the read included, and passes a
// refused write or an unreadable body to its caller; only a conflict is alerted.
func TestWriteReplicaRefusals(t *testing.T) {
	t.Parallel()
	db, scope, _ := openFailing(t)
	b := replicaBatch{key: "audit-chain/v1/k", body: []byte("line\n")}
	down := &failingStore{err: errors.New("bucket down")}
	if err := writeReplica(t.Context(), down, b); err == nil || !strings.Contains(err.Error(), "bucket down") || down.putCalls != 0 {
		t.Errorf("a failing read: %v, %d writes", err, down.putCalls)
	}
	unlisted := &failingStore{err: &objectstore.Error{Status: 403, Code: "AccessDenied"}} // S3's answer for a missing key without s3:ListBucket
	if err := writeReplica(t.Context(), unlisted, b); err == nil || !strings.Contains(err.Error(), "s3:ListBucket") || unlisted.putCalls != 0 {
		t.Errorf("a 403 on the read: %v, %d writes", err, unlisted.putCalls)
	}
	refusing := &failingStore{err: &objectstore.Error{Status: 404}}
	if err := writeReplica(t.Context(), refusing, b); err == nil || refusing.putCalls != 1 {
		t.Errorf("a refused write: %v", err)
	}
	if err := writeReplica(t.Context(), &failingStore{badBody: true}, b); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("a body that fails to read: %v", err)
	}
	if err := raiseConflict(t.Context(), db, scope, b, errors.New("other")); err != nil {
		t.Errorf("a failure that is not a conflict raised: %v", err)
	}
}

// TestReplicaAndDigestWork: a failing run of either job is logged and not retried, and a successful one logs nothing.
func TestReplicaAndDigestWork(t *testing.T) {
	t.Parallel()
	db, scope, canceled := openFailing(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	down := &failingStore{err: errors.New("bucket down")}
	replica, digest := replicaWork(db, scope, down, logger), digestWork(db, scope, false, logger)
	if err := errors.Join(replica(t.Context(), nil), digest(t.Context(), nil)); err != nil || logs.Len() != 0 {
		t.Errorf("runs with nothing to copy and a digest to make: %v %s", err, logs.String())
	}
	if err := errors.Join(replica(canceled, nil), digest(canceled, nil)); err != nil ||
		!strings.Contains(logs.String(), "the copy of the audit chain stopped") || !strings.Contains(logs.String(), "the audit digest stopped") {
		t.Errorf("failing runs: %v %s", err, logs.String())
	}
	for kind, args := range map[string]interface {
		river.JobArgs
		river.JobArgsWithInsertOpts
	}{"operations.audit_replica": replicaArgs{}, "operations.audit_digest": digestArgs{}} {
		if opts := args.InsertOpts(); args.Kind() != kind || opts.Queue != pg.QueueArchive || !opts.UniqueOpts.ByArgs {
			t.Errorf("%s: %+v", kind, opts)
		}
	}
}
