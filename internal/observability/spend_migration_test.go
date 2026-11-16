package observability

import (
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestBaselineAlertConfiguration(t *testing.T) {
	database := pgtest.New(t, Module())
	c := loadAlertConfiguration(t, database)
	checkAlertConfiguration(t, c)
	found := false
	for _, rule := range c.Rules {
		if rule.Kind == spendAnomalyKind {
			found = true
		}
	}
	if !found {
		t.Fatal("the baseline has no spend anomaly rule")
	}
}

func loadAlertConfiguration(t *testing.T, database pgtest.Database) AlertConfiguration {
	t.Helper()
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	var c AlertConfiguration
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		c, err = GetAlertConfiguration(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// checkAlertConfiguration runs the application's own validation, which an administrator's next save would run; the
// migration must not leave a body it rejects.
func checkAlertConfiguration(t *testing.T, c AlertConfiguration) {
	t.Helper()
	if err := validateAlertConfiguration(c); err != nil {
		t.Fatalf("the stored configuration no longer validates: %v", err)
	}
}
