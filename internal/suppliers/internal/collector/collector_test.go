package collector

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/riverqueue/river/rivertype"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

var platform = pg.NewScope(true, nil, nil)

func open(t *testing.T) *pg.DB {
	t.Helper()
	database := pgtest.New(t, pg.Module{Name: "suppliers", Migrations: store.Migrations()})
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func insertSupplier(t *testing.T, db *pg.DB) uuid.UUID {
	t.Helper()
	return insertSupplierKind(t, db, "cpa", "connecting")
}

func insertSupplierKind(t *testing.T, db *pg.DB, kind, status string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		var err error
		id, err = store.New().InsertSupplier(t.Context(), tx, store.InsertSupplierParams{
			OwnerScope: "platform", Name: kind, Kind: kind, Origin: "https://cpa.example", UpstreamAccount: "cpa",
			NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
		})
		if err != nil || status == "connecting" {
			return err
		}
		return store.New().SetSupplierStatusBumpingConfigGen(t.Context(), tx, store.SetSupplierStatusBumpingConfigGenParams{
			ID: id, Status: status,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type parser struct{}

func (parser) DrainUsage(context.Context, int) ([]adapter.UsageRecord, error) { return nil, nil }

func (parser) ParseUsage(raw []byte) (adapter.Charge, error) {
	if strings.Contains(string(raw), `"bad"`) {
		return adapter.Charge{}, errors.New("unreadable")
	}
	u, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 3, catalog.ItemOutput: 2}, catalog.ServiceTierStandard, nil)
	if err != nil {
		return adapter.Charge{}, err
	}
	return adapter.Charge{
		Ref: "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f", OccurredAt: time.Now().UTC(), Amount: "0",
		Usage: u, Reported: 1<<catalog.ItemInput | 1<<catalog.ItemOutput, Funding: adapter.FundingWallet, Model: "m",
	}, nil
}

func worker(db *pg.DB) *Worker {
	return &Worker{cfg: Config{
		DB: db, Scope: platform, Now: time.Now,
		Logger: slog.New(slog.DiscardHandler),
	}}
}

func count(t *testing.T, db *pg.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

type offlineAdapter struct{}

func (offlineAdapter) Kind() adapter.Kind            { return "fake" }
func (offlineAdapter) Buckets() []adapter.BucketSpec { return nil }
func (offlineAdapter) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return adapter.Detection{}, nil
}

func (offlineAdapter) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return nil, errors.New("offline adapter has no manager")
}
func (offlineAdapter) Calls() adapter.CallQuirks { return nil }
func (offlineAdapter) ParseUsage(raw []byte) (adapter.Charge, error) {
	return parser{}.ParseUsage(raw)
}

type queueAdapter struct{ calls *int }

func (queueAdapter) Kind() adapter.Kind            { return "fake" }
func (queueAdapter) Buckets() []adapter.BucketSpec { return nil }
func (queueAdapter) Detect(context.Context, adapter.Client) (adapter.Detection, error) {
	return adapter.Detection{}, nil
}

func (q queueAdapter) Open(context.Context, adapter.Client, adapter.LoginStore) (adapter.Manager, error) {
	return queueManager(q), nil
}
func (queueAdapter) Calls() adapter.CallQuirks { return nil }
func (queueAdapter) UsesUsageQueue()           {}
func (queueAdapter) ParseUsage(raw []byte) (adapter.Charge, error) {
	return parser{}.ParseUsage(raw)
}

type queueManager struct{ calls *int }

func (queueManager) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{}, errors.New("not used")
}

func (queueManager) Offer(context.Context, adapter.Listing) (adapter.Page[adapter.Group], error) {
	return adapter.Page[adapter.Group]{}, errors.New("not used")
}

func (queueManager) Keys(context.Context, adapter.Listing) (adapter.Page[adapter.KeyState], error) {
	return adapter.Page[adapter.KeyState]{}, errors.New("not used")
}

func (queueManager) KeySecret(context.Context, adapter.KeyRef) (secret.Secret, error) {
	return secret.Secret{}, errors.New("not used")
}

func (queueManager) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{}, nil, errors.New("not used")
}

func (q queueManager) DrainUsage(context.Context, int) ([]adapter.UsageRecord, error) {
	if q.calls != nil {
		*q.calls++
	}
	return nil, adapter.ErrAuthRejected
}

func (queueManager) ParseUsage(raw []byte) (adapter.Charge, error) {
	return parser{}.ParseUsage(raw)
}

func workerWith(db *pg.DB, reg *registry.Registry, keys *secret.Keyring) *Worker {
	return &Worker{cfg: Config{
		DB: db, Scope: platform, Now: time.Now, Logger: slog.New(slog.DiscardHandler),
		Registry: reg, Keys: keys,
	}}
}

func keyring(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keys")
	if err := os.WriteFile(path, []byte("1:"+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func addLogin(t *testing.T, db *pg.DB, keys *secret.Keyring, id uuid.UUID) {
	t.Helper()
	sealed := keys.Seal(secret.New(`{"v":1}`), secret.Location{
		Table: "suppliers.supplier_logins", RowID: id.String(), Purpose: "login",
	})
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		return store.New().InsertSupplierLogin(t.Context(), tx, store.InsertSupplierLoginParams{
			SupplierID: id, Kind: "fake", Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce,
			WrappedDek: sealed.WrappedDEK, KeyVersion: int64(sealed.KeyVersion),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStagingSurvivesARestartAndIngestIsIdempotent(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	records := []adapter.UsageRecord{{Ref: "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f", Raw: []byte(`{"v":1,"ok":true}`)}}
	if err := w.stage(t.Context(), id, records); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1 AND state = 'new'`, id); got != 1 {
		t.Fatalf("staged rows = %d", got)
	}

	// A new worker represents the process after a restart: only the staging row is left.
	if err := worker(db).ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows = %d", got)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1`, id); got != 0 {
		t.Fatalf("staging rows after ingest = %d", got)
	}

	// A replay of the same popped batch inserts its staging row again, then reconciliation dedupes the charge.
	if err := w.stage(t.Context(), id, records); err != nil {
		t.Fatal(err)
	}
	if err := w.ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows after replay = %d", got)
	}
}

func TestAnUnreadableRecordIsKeptAndMarked(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	if err := w.stage(t.Context(), id, []adapter.UsageRecord{{Raw: []byte(`{"v":1,"bad":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := w.ingest(t.Context(), id, parser{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1 AND state = 'rejected'`, id); got != 1 {
		t.Fatalf("rejected rows = %d", got)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 0 {
		t.Fatalf("charge rows = %d", got)
	}
}

func TestARemovedSupplierStillReplaysStagedUsage(t *testing.T) {
	db := open(t)
	id := insertSupplierKind(t, db, "fake", "removed")
	reg, err := registry.New(offlineAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	w := workerWith(db, reg, nil)
	if err := w.stage(t.Context(), id, []adapter.UsageRecord{{Raw: []byte(`{"v":1,"ok":true}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := w.work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows = %d", got)
	}
}

func TestAuthBackoffSurvivesWorkersAndRestarts(t *testing.T) {
	db := open(t)
	id := insertSupplierKind(t, db, "fake", "active")
	keys := keyring(t)
	addLogin(t, db, keys, id)
	calls := 0
	reg, err := registry.New(queueAdapter{calls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	if err := workerWith(db, reg, keys).work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("first worker calls = %d", calls)
	}
	if err := workerWith(db, reg, keys).work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("second worker calls = %d", calls)
	}
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE suppliers.supplier_logins SET version = version + 1 WHERE supplier_id = $1`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := workerWith(db, reg, keys).work(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("a new login version was still blocked: calls = %d", calls)
	}
}

// TestRegisterRefusesAnIncompleteConfig: a node that lacks one of the collector's dependencies refuses to start it,
// instead of running a drain that cannot work.
func TestRegisterRefusesAnIncompleteConfig(t *testing.T) {
	if err := Register(Config{}); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("Register with an empty config returned %v, want the missing-dependency error", err)
	}
}

// TestDrainJobIsUnique: a drain still queued or running is not stacked with another; the next interval starts a fresh
// pass and the queue keeps the records until then.
func TestDrainJobIsUnique(t *testing.T) {
	if got := (Args{}).Kind(); got != "suppliers.usage_drain" {
		t.Fatalf("job kind = %q", got)
	}
	opts := (Args{}).InsertOpts()
	if opts.MaxAttempts != 1 || !opts.UniqueOpts.ByArgs {
		t.Fatalf("insert options = %+v", opts)
	}
	for _, state := range []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateRetryable, rivertype.JobStateScheduled,
	} {
		if !slices.Contains(opts.UniqueOpts.ByState, state) {
			t.Errorf("a drain in state %s is not unique", state)
		}
	}
}

// recordQueue returns its records on the first drain and nothing afterwards, as a queue consumed once does.
type recordQueue struct {
	queueManager
	records []adapter.UsageRecord
	calls   int
}

func (q *recordQueue) DrainUsage(context.Context, int) ([]adapter.UsageRecord, error) {
	q.calls++
	if q.calls > 1 {
		return nil, nil
	}
	return q.records, nil
}

// TestDrainStagesThenIngests: the records a drain takes are staged before they are parsed, and the same pass ingests
// them into charge records and removes the staging rows.
func TestDrainStagesThenIngests(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	hold, err := w.hold(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.release(t.Context(), hold, id)
	queue := &recordQueue{records: []adapter.UsageRecord{{
		Ref: "019c0f3e-6a4b-7c8d-9e0f-1a2b3c4d5e6f", Raw: []byte(`{"v":1,"ok":true}`),
	}}}
	if err := w.drain(t.Context(), supplier{id: id}, queue, 1, hold); err != nil {
		t.Fatal(err)
	}
	if queue.calls != 2 {
		t.Fatalf("drain calls = %d, want two (the second empty)", queue.calls)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.charge_records WHERE supplier_id = $1`, id); got != 1 {
		t.Fatalf("charge rows = %d", got)
	}
	if got := count(t, db, `SELECT count(*) FROM suppliers.usage_records WHERE supplier_id = $1`, id); got != 0 {
		t.Fatalf("staging rows after ingest = %d", got)
	}
}

func TestReleaseLetsNextDrainHoldSameSupplier(t *testing.T) {
	w := worker(open(t))
	id := uuid.NewV4()
	held, err := w.hold(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	w.release(t.Context(), held, id)

	next, err := w.hold(t.Context(), id)
	if err != nil {
		t.Fatalf("hold immediately after release: %v", err)
	}
	w.release(t.Context(), next, id)
}

// errorQueue fails every drain with one error.
type errorQueue struct {
	queueManager
	err error
}

func (q errorQueue) DrainUsage(context.Context, int) ([]adapter.UsageRecord, error) {
	return nil, q.err
}

// TestDrainErrorsKeepTheirMeaning: a blocked supplier or a full bucket delays the next attempt without failing the
// job; any other error fails it, so the queue retries.
func TestDrainErrorsKeepTheirMeaning(t *testing.T) {
	db := open(t)
	id := insertSupplier(t, db)
	w := worker(db)
	network := errors.New("network down")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"blocked", adapter.Blocked{Until: time.Now().Add(time.Minute)}, nil},
		{"buckets full", adapter.ErrBucketsFull, nil},
		{"network", network, network},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hold, err := w.hold(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			defer w.release(t.Context(), hold, id)
			got := w.drain(t.Context(), supplier{id: id}, errorQueue{err: tc.err}, 1, hold)
			if tc.want == nil && got != nil {
				t.Fatalf("drain returned %v, want a delay", got)
			}
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Fatalf("drain returned %v, want %v", got, tc.want)
			}
		})
	}
}
