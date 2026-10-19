package suppliers

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// fakeKind is the adapter of the call tests: the quirks of a made-up system whose request id is in X-Ref.
type fakeKind struct{ adapter.Adapter }

func (fakeKind) Kind() adapter.Kind            { return "fake" }
func (fakeKind) Buckets() []adapter.BucketSpec { return nil }
func (fakeKind) Calls() adapter.CallQuirks     { return fakeQuirks{} }

type fakeQuirks struct{}

func (fakeQuirks) Policy() adapter.CallPolicy {
	return adapter.CallPolicy{Streams: true, Cancels: true}
}

func (fakeQuirks) Formats(adapter.GroupFacts) []protocol.Format {
	return []protocol.Format{protocol.Chat}
}

func (fakeQuirks) ChargeRef(h http.Header) (adapter.ChargeRef, bool) {
	v := h.Get("X-Ref")
	return adapter.ChargeRef(v), v != ""
}

// Classify: 401 the key is refused, 403 its allowance is used up, 429 it is asked to wait, 5xx the supplier failed.
func (fakeQuirks) Classify(status int, _ http.Header, _ []byte) adapter.Failure {
	switch status {
	case 401:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}
	case 403:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialQuotaExhausted{}}
	case 429:
		return adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.RateLimited{RetryAfter: 3 * time.Second}}
	case 500:
		return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: 500}}
	}
	return adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Unknown{Status: status}}
}

// world is a database with one supplier, settled in USD at 2 a unit, its group, credential and endpoint priced at 0.075
// per input token and 0.3 per output token, and a service that has loaded them.
type world struct {
	t                                         *testing.T
	db                                        *pg.DB
	database                                  pgtest.Database
	keys                                      *secret.Keyring
	supplier, group, credential, endpoint, sc uuid.UUID
	svc                                       *Service
	clock                                     *testClock
	second                                    bool // seed makes the second supplier
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var platformScope = pg.PlatformScope()

// scheduleJSON is a cost schedule in the canonical form catalog.Schedule writes back, so a test can compare a schedule
// read from the database with it as text.
const scheduleJSON = `{"currency":"XXX","rates":{"input":"0.075","output":"0.3"},"v":1}`

func newKeys(t *testing.T) *secret.Keyring {
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

// newWorld seeds the tables and loads a service. maxConcurrency is the credential's limit.
func newWorld(t *testing.T, maxConcurrency int32) *world {
	t.Helper()
	database := pgtest.New(t, Module())
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	w := &world{t: t, db: db, database: database, keys: newKeys(t), clock: &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}}
	w.credential = uuid.NewV7()
	w.tx(func(ctx context.Context, tx pg.Tx) error { return w.seed(ctx, tx, maxConcurrency) })
	reg, err := registry.New(fakeKind{})
	if err != nil {
		t.Fatal(err)
	}
	w.svc, err = New(Config{
		DB: db, Scope: platformScope, Keys: w.keys, Egress: egress.NewForTesting(egresstest.Loopback(t)),
		Registry: reg, Clock: w.clock.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	return w
}

// seed makes the supplier, group, credential, endpoint and its schedule, chosen and active.
func (w *world) seed(ctx context.Context, tx pg.Tx, maxConcurrency int32) (err error) {
	q := store.New()
	sealed := w.keys.Seal(secret.New("sk-test-key"), secret.Location{Table: "suppliers.credentials", RowID: w.credential.String(), Purpose: "key"})
	version := int64(sealed.KeyVersion)
	if w.supplier, err = q.InsertSupplier(ctx, tx, store.InsertSupplierParams{
		OwnerScope: "platform", Name: "s", Kind: "fake",
		Origin: w.origin(), UpstreamAccount: "me", NativeUnit: "quota", SettlementCurrency: "USD", MgmtEgressID: "direct",
	}); err != nil {
		return err
	}
	if w.group, err = q.InsertGroup(ctx, tx, store.InsertGroupParams{SupplierID: w.supplier, UpstreamRef: "g", Name: "g", Ratio: "1"}); err != nil {
		return err
	}
	if err = q.InsertCredential(ctx, tx, store.InsertCredentialParams{
		ID: w.credential, SupplierID: w.supplier, SupplierGroupID: w.group,
		UpstreamKeyRef: "7", Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
		Status: "active", MaxConcurrency: maxConcurrency,
	}); err != nil {
		return err
	}
	if w.endpoint, err = q.InsertEndpoint(ctx, tx, store.InsertEndpointParams{
		SupplierID: w.supplier, SupplierGroupID: w.group,
		ModelID: uuid.NewV7(), UpstreamName: "gpt", Formats: []string{"chat"},
	}); err != nil {
		return err
	}
	if w.sc, err = q.InsertCostSchedule(ctx, tx, store.InsertCostScheduleParams{
		SupplierID: w.supplier, EndpointID: w.endpoint,
		NativeUnit: "quota", Schedule: []byte(scheduleJSON), Basis: "published", Fingerprint: "v1",
	}); err != nil {
		return err
	}
	if err = q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: w.endpoint, CostScheduleID: &w.sc}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE suppliers.suppliers SET status = 'active', balance_native = 100 WHERE id = $1", w.supplier); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 2, 'test', 'seed')", w.supplier); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE suppliers.supplier_groups SET selected = true, codename = $2 WHERE id = $1", w.group, w.codename())
	return err
}

// origin and codename are the first supplier's; a second one (addSupplier) gets its own, which the tables keep unique.
func (w *world) origin() string {
	if w.second {
		return "https://second.example"
	}
	return "https://fake.example"
}

func (w *world) codename() string {
	if w.second {
		return "birch"
	}
	return "maple"
}

// tx runs fn in a platform transaction and fails the test on its error.
func (w *world) tx(fn func(ctx context.Context, tx pg.Tx) error) {
	w.t.Helper()
	if err := w.db.Tx(w.t.Context(), platformScope, func(tx pg.Tx) error { return fn(w.t.Context(), tx) }); err != nil {
		w.t.Fatal(err)
	}
}

// addValue gives the supplier another unit value and returns the row's id.
func (w *world) addValue(v string) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return tx.QueryRow(ctx, "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, $2::numeric, 'test', 'later') RETURNING id", w.supplier, v).Scan(&id)
	})
	return id
}

// insertValue writes a unit value and returns the database's answer, for a value the table may refuse.
func (w *world) insertValue(v string) error {
	w.t.Helper()
	return w.db.Tx(w.t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(w.t.Context(), "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, $2::numeric, 'test', 'bad')", w.supplier, v)
		return err
	})
}

// pointAt makes the endpoint's calls go to a test server instead of the supplier's address.
func (w *world) pointAt(serverURL string) {
	w.t.Helper()
	w.svc.snap.Load().endpoints[w.endpoint].origin = serverURL
}

// uuidOf is an id no row has.
func uuidOf(n byte) uuid.UUID { return uuid.UUID{15: n} }
