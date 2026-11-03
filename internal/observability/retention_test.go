package observability

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

// fakeStore is the archive's view of a bucket in memory, with knobs for the failures the tests need.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
	headErr error
	putPath string
}

func newFakeStore() *fakeStore { return &fakeStore{objects: map[string][]byte{}} }

func (s *fakeStore) Put(_ context.Context, up objectstore.Upload) (objectstore.ObjectInfo, error) {
	if f, ok := up.Body.(*os.File); ok {
		s.mu.Lock()
		s.putPath = f.Name()
		s.mu.Unlock()
	}
	if s.putErr != nil {
		return objectstore.ObjectInfo{}, s.putErr
	}
	body, err := io.ReadAll(up.Body)
	if err != nil {
		return objectstore.ObjectInfo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[up.Key] = body
	return objectstore.ObjectInfo{Key: up.Key, Size: int64(len(body))}, nil
}

func (s *fakeStore) Get(_ context.Context, key string) (*objectstore.Object, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.objects[key]
	if !ok {
		return nil, objectstore.ErrNotFound
	}
	return &objectstore.Object{Key: key, Size: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body))}, nil
}

func (s *fakeStore) Head(_ context.Context, key string) (objectstore.ObjectInfo, error) {
	if s.headErr != nil {
		return objectstore.ObjectInfo{}, s.headErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.objects[key]
	if !ok {
		return objectstore.ObjectInfo{}, objectstore.ErrNotFound
	}
	return objectstore.ObjectInfo{Key: key, Size: int64(len(body))}, nil
}

func newTestRetainer(t *testing.T, db *pg.DB, store ArchiveStore) *retainer {
	t.Helper()
	return newRetainer(RetentionConfig{DB: db, Scope: platformScope, Store: store, Logger: quiet})
}

// openRetention opens the job's own login, which is a member of the role that owns the log tables and not of the
// application's role. The retention tests run its work here, the way cmd/axiom does in production.
func openRetention(t *testing.T, database pgtest.Database) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), database.Retention, pg.WithCompletionReserve(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func countRows(t *testing.T, db *pg.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), sql, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func partitionExists(t *testing.T, db *pg.DB, name string) bool {
	t.Helper()
	var exists bool
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, "observability."+name).Scan(&exists)
	}); err != nil {
		t.Fatal(err)
	}
	return exists
}

// TestRetentionArchivesAndDropsOneMonth: a month past the window leaves PostgreSQL, both its tables' partitions go, the
// next month stays, and the archive holds the rows with a manifest that names them. A row in another schema ("the
// ledger" stands in here; observability cannot import billing) is untouched.
//
//nolint:cyclop,gocognit // one whole month through archive, confirmation, drop and the surviving neighbour is the point.
func TestRetentionArchivesAndDropsOneMonth(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	old, recent := record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)), record(account, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	keep(t, db, old, recent)

	if err := pgtest.Run(t.Context(), database.Superuser,
		`CREATE SCHEMA billing`,
		`CREATE TABLE billing.ledger_marker (note text)`,
		`INSERT INTO billing.ledger_marker VALUES ('must survive the call log')`); err != nil {
		t.Fatal(err)
	}

	store := newFakeStore()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	if err := newTestRetainer(t, retention, store).run(t.Context(), time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	if partitionExists(t, db, "call_log_2026_10") || partitionExists(t, db, "call_attempt_2026_10") {
		t.Fatal("an archived month's partitions are still in PostgreSQL")
	}
	if !partitionExists(t, db, "call_log_2026_11") {
		t.Error("the month inside the window was dropped")
	}
	if got := countRows(t, db, `SELECT count(*) FROM observability.call_log WHERE at < '2026-11-01'`); got != 0 {
		t.Errorf("%d dropped rows are still readable", got)
	}
	if got := countRows(t, db, `SELECT count(*) FROM observability.call_log WHERE at >= '2026-11-01'`); got != 1 {
		t.Errorf("the recent month has %d rows, want 1", got)
	}

	manifest := readManifest(t, store, "2026-10")
	if manifest.Version != archiveManifestVersion || manifest.Month != "2026-10" || manifest.Format != "postgresql-copy-csv-gzip" {
		t.Errorf("manifest = %+v", manifest)
	}
	if len(manifest.Tables) != 2 {
		t.Fatalf("manifest names %d tables, want call_log and call_attempt", len(manifest.Tables))
	}
	rows := map[string]int64{}
	for _, table := range manifest.Tables {
		raw := store.objects[table.Key]
		if int64(len(raw)) != table.Bytes {
			t.Errorf("%s is %d bytes, manifest says %d", table.Key, len(raw), table.Bytes)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != table.SHA256 {
			t.Errorf("%s digest does not match the manifest", table.Key)
		}
		lines := ungzipCSV(t, raw)
		if len(lines) != int(table.Rows)+1 { // the header
			t.Errorf("%s has %d CSV lines, want %d rows and a header", table.Key, len(lines), table.Rows)
		}
		wantFirst := map[string]string{"call_log": "id", "call_attempt": "request_id"}[table.Table]
		if len(table.Columns) == 0 || table.Columns[0].Name != wantFirst {
			t.Errorf("%s columns = %+v", table.Key, table.Columns)
		}
		rows[table.Table] = table.Rows
	}
	if rows["call_log"] != 1 {
		t.Errorf("call_log archived %d rows, want 1", rows["call_log"])
	}
	if rows["call_attempt"] != int64(len(old.Attempts)) {
		t.Errorf("call_attempt archived %d rows, want %d", rows["call_attempt"], len(old.Attempts))
	}
	if err := pgtest.Run(t.Context(), database.Superuser, `DO $$
BEGIN
	IF (SELECT count(*) FROM billing.ledger_marker) <> 1 THEN
		RAISE EXCEPTION 'the ledger moved with the call log';
	END IF;
END $$`); err != nil {
		t.Error(err)
	}
}

// TestRetentionWithoutABucketKeepsEverything: a deployment with no object store keeps every partition, even one far
// past the window, because dropping without a confirmed archive would lose records.
func TestRetentionWithoutABucketKeepsEverything(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	if err := newTestRetainer(t, retention, nil).run(t.Context(), time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !partitionExists(t, db, "call_log_2026_10") {
		t.Error("a partition was dropped with no object store to archive it to")
	}
}

// TestRetentionWindowZeroKeepsEverything: zero is "never", not "everything is past the window".
func TestRetentionWindowZeroKeepsEverything(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 0})
	}); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	if err := newTestRetainer(t, retention, store).run(t.Context(), time.Date(2027, 6, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if !partitionExists(t, db, "call_log_2026_10") {
		t.Error("a partition was dropped with the window off")
	}
	if len(store.objects) != 0 {
		t.Errorf("the window off still uploaded %d objects", len(store.objects))
	}
}

// TestRetentionKeepsPartitionsWhenTheArchiveFails: a failed upload or an object that does not match its manifest never
// lets the partition go, and the next run repairs it.
func TestRetentionKeepsPartitionsWhenTheArchiveFails(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.putErr = errors.New("bucket down")
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err == nil {
		t.Fatal("a failed upload reported success")
	}
	if !partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("a partition was dropped although its upload failed")
	}

	store.putErr = nil
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the repaired run did not drop the archived partition")
	}
}

// TestRetentionDoesNotDropWhenConfirmationFails: a manifest plus objects that cannot be checked is not an archive the
// job trusts; the partition stays until a later run sees every object at its recorded size.
func TestRetentionDoesNotDropWhenConfirmationFails(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.headErr = errors.New("bucket head down")
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err == nil {
		t.Fatal("a failed confirmation reported success")
	}
	if !partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the partition was dropped although confirmation failed")
	}
	store.headErr = nil
	if err := newTestRetainer(t, retention, store).run(t.Context(), at); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, db, "call_log_2026_10") {
		t.Error("the partition was not dropped after the confirmation succeeded")
	}
}

// TestRetentionCreatesTheMonthsAhead: the job extends both tables before it archives, including a month the migrations
// did not reach, and the new partition carries the scope policy and a write from the application.
func TestRetentionCreatesTheMonthsAhead(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	if err := newTestRetainer(t, retention, nil).run(t.Context(), time.Date(2029, 6, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"call_log_2029_06", "call_log_2030_06", "call_attempt_2029_06", "call_attempt_2030_06"} {
		if !partitionExists(t, db, name) {
			t.Errorf("%s was not created", name)
		}
	}
	var forced, policies int
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_class c
			WHERE c.oid = 'observability.call_log_2029_06'::regclass AND c.relrowsecurity AND c.relforcerowsecurity`).Scan(&forced); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM pg_policy WHERE polrelid = 'observability.call_log_2029_06'::regclass`).Scan(&policies)
	}); err != nil {
		t.Fatal(err)
	}
	if forced != 1 || policies != 1 {
		t.Errorf("new partition: forced RLS = %d, policies = %d, want 1 and 1", forced, policies)
	}
	account := uuid.NewV7()
	future := record(account, time.Date(2029, 6, 20, 12, 0, 0, 0, time.UTC))
	keep(t, db, future)
	if got := countRows(t, db, `SELECT count(*) FROM observability.call_log WHERE id = $1`, future.ID); got != 1 {
		t.Errorf("the application wrote %d rows into the created partition, want 1", got)
	}
	// The startup audit sees the created partitions too: forced RLS, one policy, and the InitPlan form.
	if err := db.Check(t.Context(), Module()); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionConfiguration: the default before a save, the round trip, the contract, and a damaged saved row.
func TestRetentionConfiguration(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	read := func() (Retention, error) {
		var r Retention
		err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			var err error
			r, err = GetRetention(t.Context(), tx)
			return err
		})
		return r, err
	}
	if r, err := read(); err != nil || r != defaultRetention() {
		t.Fatalf("before a save: %+v, %v; want the default %+v", r, err, defaultRetention())
	}
	for _, bad := range []Retention{{HotDays: -1}, {HotDays: 3651}} {
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			return SetRetention(t.Context(), tx, bad)
		}); !errors.Is(err, ErrRetention) {
			t.Errorf("SetRetention(%+v) = %v, want ErrRetention", bad, err)
		}
	}
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return SetRetention(t.Context(), tx, Retention{HotDays: 365})
	}); err != nil {
		t.Fatal(err)
	}
	if r, err := read(); err != nil || r.HotDays != 365 {
		t.Errorf("after a save: %+v, %v", r, err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser,
		`UPDATE observability.retention SET body = '{"hot_days": -5}'`); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); !errors.Is(err, ErrRetention) {
		t.Errorf("a damaged saved row returned %v, want ErrRetention", err)
	}
}

func readManifest(t *testing.T, store *fakeStore, month string) archiveManifest {
	t.Helper()
	raw, ok := store.objects["call-log/v1/"+month+"/manifest.json"]
	if !ok {
		t.Fatalf("no manifest for %s", month)
	}
	var manifest archiveManifest
	if err := jsonx.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func ungzipCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }() //nolint:errcheck // the reader reports a truncated stream
	rows, err := csv.NewReader(gz).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestRetentionArchivesToSeaweedFS is the archive against the deployment's object store: the objects are where the
// manifest says, they unpack to the CSV, and the month is gone from PostgreSQL only afterwards.
func TestRetentionArchivesToSeaweedFS(t *testing.T) {
	if testing.Short() {
		t.Skip("starts SeaweedFS")
	}
	ctx := t.Context()
	s3 := containertest.StartSeaweedFS(t, "axiom")
	client, err := objectstore.Open(objectstore.Config{
		Endpoint: s3.Endpoint, Bucket: "axiom", Region: "us-east-1",
		AccessKeyID: containertest.SeaweedFSAccessKey, SecretAccessKey: secret.New(containertest.SeaweedFSSecretKey), PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	db, database := openDBWithURL(t)
	retention := openRetention(t, database)
	account := uuid.NewV7()
	keep(t, db, record(account, time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)))
	if err := db.Tx(ctx, platformScope, func(tx pg.Tx) error {
		return SetRetention(ctx, tx, Retention{HotDays: 30})
	}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := newTestRetainer(t, retention, client).run(runCtx, time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if partitionExists(t, db, "call_log_2026_10") {
		t.Fatal("the archived partition is still in PostgreSQL")
	}

	object, err := client.Get(runCtx, "call-log/v1/2026-10/call_log.csv.gz")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(object.Body)
	_ = object.Body.Close() //nolint:errcheck // the test reads it to EOF
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ungzipCSV(t, raw)); got != 2 { // the header and the one call
		t.Errorf("the archived call log has %d CSV lines, want 2", got)
	}
	page, err := client.List(runCtx, objectstore.ListOptions{Prefix: "call-log/v1/2026-10/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 3 {
		t.Errorf("the month has %d objects, want the two tables and the manifest", len(page.Objects))
	}
}
