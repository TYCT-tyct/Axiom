package operations_test

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/operations"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func count(value int64) *int64 { return new(value) }

func money(t *testing.T, amount string) *operations.Money {
	t.Helper()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	return &operations.Money{Amount: amount, Currency: currency}
}

func usd(t *testing.T) platform.Currency {
	t.Helper()
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	return currency
}

// TestTargetsRoundTrip: the owner sets every target of a month and reads the same values back; a legacy editor's write
// replaces only the first-release four and keeps the commercial ones; a stale revision refuses without changing
// anything; a clear removes every row and moves the revision.
func TestTargetsRoundTrip(t *testing.T) {
	t.Parallel()
	db := openOperations(t)
	site := uuid.NewV7()
	registerSite(t, db, site)
	month := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	set := operations.Targets{
		Month: month, Calls: count(3100), Errors: count(12),
		Revenue: money(t, "5000000000000"), Cost: money(t, "3000000000000"),
		PaidUsers: count(300), MonthlyRevenue: money(t, "600000000000"),
		GrossMargin: count(3500), Retention: count(7000),
	}
	if err := writeTargets(t, db, site, set, 0, operations.SetOptions{}); err != nil {
		t.Fatalf("set the targets: %v", err)
	}
	want := set
	want.Revision = 1
	assertTargets(t, readTargets(t, db, site, month), want)

	// The month is its UTC first day whatever day the caller names.
	assertTargets(t, readTargets(t, db, site, month.AddDate(0, 0, 19)), want)

	legacy := operations.Targets{
		Month: month, Calls: count(3200), Revenue: money(t, "5100000000000"),
	}
	if err := writeTargets(t, db, site, legacy, 1, operations.SetOptions{KeepCommercial: true}); err != nil {
		t.Fatalf("legacy write: %v", err)
	}
	afterLegacy := operations.Targets{
		Month: month, Revision: 2, Calls: count(3200), Revenue: money(t, "5100000000000"),
		PaidUsers: count(300), MonthlyRevenue: money(t, "600000000000"),
		GrossMargin: count(3500), Retention: count(7000),
	}
	assertTargets(t, readTargets(t, db, site, month), afterLegacy)

	stale := operations.Targets{Month: month, Calls: count(1)}
	if err := writeTargets(t, db, site, stale, 1, operations.SetOptions{}); !errors.Is(err, operations.ErrStaleRevision) {
		t.Fatalf("a write from revision 1 after revision 2 returned %v, want ErrStaleRevision", err)
	}
	assertTargets(t, readTargets(t, db, site, month), afterLegacy)

	if err := writeTargets(t, db, site, operations.Targets{Month: month}, 2, operations.SetOptions{}); err != nil {
		t.Fatalf("clear the targets: %v", err)
	}
	assertTargets(t, readTargets(t, db, site, month), operations.Targets{Month: month, Revision: 3})
}

// TestExpectedForProratesStoredTargets: ExpectedFor reads both months a window touches, prorates each quantity in its
// own unit and takes the rates from the month the window ends in; a window with no duration has no targets.
func TestExpectedForProratesStoredTargets(t *testing.T) {
	t.Parallel()
	db := openOperations(t)
	site := uuid.NewV7()
	registerSite(t, db, site)
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	november := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	if err := writeTargets(t, db, site, operations.Targets{
		Month: october, Calls: count(3100), Revenue: money(t, "2678400000000"), GrossMargin: count(2000),
	}, 0, operations.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := writeTargets(t, db, site, operations.Targets{
		Month: november, Calls: count(3000), Revenue: money(t, "2592000000000"),
		GrossMargin: count(3500), Retention: count(8000),
	}, 0, operations.SetOptions{}); err != nil {
		t.Fatal(err)
	}

	since := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	want := operations.Expected{
		Calls: count(3100), Revenue: money(t, "2678400000000"),
		GrossMargin: count(3500), Retention: count(8000),
	}
	if got := readExpected(t, db, site, since, until); !cmp.Equal(want, got, cmp.AllowUnexported(platform.Currency{})) {
		t.Errorf("expected targets differ (-want +got):\n%s",
			cmp.Diff(want, got, cmp.AllowUnexported(platform.Currency{})))
	}
	if got := readExpected(t, db, site, since, since); got != (operations.Expected{}) {
		t.Errorf("a window with no duration expected %+v, want nothing", got)
	}
}

func registerSite(t *testing.T, db *pg.DB, site uuid.UUID) {
	t.Helper()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return audit.RegisterSite(t.Context(), tx, site)
	}); err != nil {
		t.Fatalf("register the site: %v", err)
	}
}

func writeTargets(t *testing.T, db *pg.DB, site uuid.UUID, set operations.Targets, revision int64,
	options operations.SetOptions,
) error {
	t.Helper()
	return db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return operations.Set(t.Context(), tx, audit.System("operations.test"), site, set, revision, "test targets", options)
	})
}

func readTargets(t *testing.T, db *pg.DB, site uuid.UUID, month time.Time) operations.Targets {
	t.Helper()
	var targets operations.Targets
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		targets, err = operations.Read(t.Context(), tx, site, month)
		return err
	}); err != nil {
		t.Fatalf("read the targets: %v", err)
	}
	return targets
}

func readExpected(t *testing.T, db *pg.DB, site uuid.UUID, since, until time.Time) operations.Expected {
	t.Helper()
	var expected operations.Expected
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		expected, err = operations.ExpectedFor(t.Context(), tx, site, usd(t), since, until)
		return err
	}); err != nil {
		t.Fatalf("read the expected targets: %v", err)
	}
	return expected
}

func assertTargets(t *testing.T, got, want operations.Targets) {
	t.Helper()
	if !got.Month.Equal(want.Month) {
		t.Errorf("month = %s, want %s", got.Month, want.Month)
	}
	got.Month, want.Month = time.Time{}, time.Time{}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(platform.Currency{})); diff != "" {
		t.Errorf("targets differ (-want +got):\n%s", diff)
	}
}
