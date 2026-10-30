package observability

import (
	"io/fs"
	"strconv"
	"testing"
	"testing/fstest"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// migrationsBeforeObservability is this module's migrations numbered below version.
func migrationsBeforeObservability(t *testing.T, version string) pg.Module {
	t.Helper()
	all, before := Module().Migrations, fstest.MapFS{}
	paths, err := fs.Glob(all, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path >= version {
			continue
		}
		data, err := fs.ReadFile(all, path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = &fstest.MapFile{Data: data}
	}
	return pg.Module{Name: "observability", Migrations: before}
}

// ruleBody builds a legal pre-upgrade alert configuration from rule ids and kinds.
func ruleBody(t *testing.T, rev int, ids, kinds []string) string {
	t.Helper()
	rules := make([]map[string]any, len(ids))
	for i := range ids {
		rules[i] = map[string]any{
			"id": ids[i], "kind": kinds[i], "minimum_severity": "warning", "threshold": 1,
			"duration_seconds": 0, "channel": "", "quiet_start": 0, "quiet_end": 0, "repeat_seconds": 0,
		}
	}
	raw, err := jsonx.MarshalCanonical(map[string]any{"rules": rules, "channels": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return `UPDATE observability.alert_configuration SET rev=` + strconv.Itoa(rev) + `, body='` + string(raw) + `'`
}

// TestSpendRuleAppendKeepsFullConfiguration: the migration appends the default rule only when the configuration has
// room. A full configuration stays legal instead of failing this application's own validation on the next save, and the
// operator adds the rule by hand.
func TestSpendRuleAppendKeepsFullConfiguration(t *testing.T) {
	database := pgtest.New(t)
	if err := pg.Migrate(t.Context(), database.Migrator, migrationsBeforeObservability(t, "00014")); err != nil {
		t.Fatal(err)
	}
	ids, kinds := make([]string, 32), make([]string, 32)
	for i := range ids {
		ids[i], kinds[i] = "rule-"+strconv.Itoa(i), "test.failure"
	}
	if err := pgtest.Run(t.Context(), database.Superuser, ruleBody(t, 9, ids, kinds)); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(t.Context(), database.Migrator, Module()); err != nil {
		t.Fatal(err)
	}
	c := loadAlertConfiguration(t, database)
	if len(c.Rules) != 32 || c.Rev != 9 {
		t.Fatalf("full configuration changed: %d rules, rev %d", len(c.Rules), c.Rev)
	}
	for _, r := range c.Rules {
		if r.Kind == spendAnomalyKind {
			t.Fatalf("a rule was appended to a full configuration: %+v", r)
		}
	}
	checkAlertConfiguration(t, c)
}

// TestSpendRuleAppendUsesFreeID: an existing rule may already use the default id. The migration appends under the next
// free id, so the stored body still has unique ids and saves.
func TestSpendRuleAppendUsesFreeID(t *testing.T) {
	database := pgtest.New(t)
	if err := pg.Migrate(t.Context(), database.Migrator, migrationsBeforeObservability(t, "00014")); err != nil {
		t.Fatal(err)
	}
	body := ruleBody(t, 1, []string{"spend-anomaly", "first", "second"}, []string{"test.failure", "test.failure", "test.failure"})
	if err := pgtest.Run(t.Context(), database.Superuser, body); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(t.Context(), database.Migrator, Module()); err != nil {
		t.Fatal(err)
	}
	c := loadAlertConfiguration(t, database)
	if len(c.Rules) != 4 {
		t.Fatalf("rules %+v", c.Rules)
	}
	last := c.Rules[len(c.Rules)-1]
	if last.Kind != spendAnomalyKind || last.ID != "spend-anomaly-2" {
		t.Fatalf("appended rule %+v, want spend-anomaly-2", last)
	}
	ids := map[string]bool{}
	for _, r := range c.Rules {
		if ids[r.ID] {
			t.Fatalf("duplicate id %q", r.ID)
		}
		ids[r.ID] = true
	}
	checkAlertConfiguration(t, c)
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
