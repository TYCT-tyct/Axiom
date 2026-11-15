package observability

import (
	"strconv"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestUsageTokenKindsMatchActivity(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, input, output string
		usage               map[catalog.Item]int64
	}{
		{"openai", "90", "1000", map[catalog.Item]int64{catalog.ItemInput: 20, catalog.ItemAudioIn: 30, catalog.ItemImageIn: 40, catalog.ItemOutput: 90, catalog.ItemReasoning: 900, catalog.ItemAudioOut: 7, catalog.ItemImageOut: 3}},
		{"gemini", "90", "1000", map[catalog.Item]int64{catalog.ItemInput: 90, catalog.ItemOutput: 100, catalog.ItemReasoning: 900}},
		{"messages", "90", "1000", map[catalog.Item]int64{catalog.ItemInput: 90, catalog.ItemOutput: 1000}},
		{"reasoning only", "", "900", map[catalog.Item]int64{catalog.ItemReasoning: 900}},
		{"modality only", "50", "90", map[catalog.Item]int64{catalog.ItemAudioIn: 20, catalog.ItemImageIn: 30, catalog.ItemAudioOut: 40, catalog.ItemImageOut: 50}},
		{"reported zero", "0", "0", map[catalog.Item]int64{catalog.ItemInput: 0, catalog.ItemOutput: 0}},
		{"unknown", "", "", map[catalog.Item]int64{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := uuid.NewV7()
			call := record(account, at)
			call.Usage = tc.usage
			keep(t, db, call)
			usageBatch(t, db, 1000)
			got := usageRead(t, db, accountScope(account), UsageQuery{Account: account, Since: at, Until: at.Add(time.Hour), Granularity: "hour"})
			m := got.Totals[0]
			if diff := cmp.Diff([]string{tc.input, tc.output}, []string{m.Input, m.Output}); diff != "" {
				t.Fatal(diff)
			}
			assertUsageActivityTokens(t, db, account, at, m)
		})
	}
}

func assertUsageActivityTokens(t *testing.T, db *pg.DB, account uuid.UUID, at time.Time, m UsageMetrics) {
	t.Helper()
	var activity []ActivityItem
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) (err error) {
		activity, err = ActivityOwn(t.Context(), tx, ActivityQuery{Account: account, Since: at, Until: at.Add(time.Hour)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if m.Input != "" && m.Input != strconv.FormatInt(activity[0].PromptTokens, 10) {
		t.Fatalf("input %s, activity %+v", m.Input, activity)
	}
	if m.Output != "" && m.Output != strconv.FormatInt(activity[0].CompletionTokens, 10) {
		t.Fatalf("output %s, activity %+v", m.Output, activity)
	}
}

func TestUsageBackfillDrainsLiveJournal(t *testing.T) {
	db, database := usageDBBeforeRollup(t)
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	account := uuid.NewV7()
	var history []CallRecord
	for i := range 6 {
		call := record(account, at.Add(time.Duration(i)*time.Minute))
		call.Model = "history"
		history = append(history, call)
	}
	keep(t, db, history...)
	if err := pg.Migrate(t.Context(), database.Migrator, Module()); err != nil {
		t.Fatal(err)
	}
	var pending []int
	for _, newRows := range []int{4, 1, 1} {
		var live []CallRecord
		for range newRows {
			call := record(account, at)
			call.Model = "live"
			live = append(live, call)
		}
		keep(t, db, live...)
		usageBatch(t, db, 2)
		if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
			var count int
			if err := tx.QueryRow(t.Context(), `SELECT count(*)::int FROM observability.usage_journal`).Scan(&count); err != nil {
				return err
			}
			pending = append(pending, count)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if diff := cmp.Diff([]int{2, 1, 0}, pending); diff != "" {
		t.Fatal(diff)
	}
	q := UsageQuery{Account: account, Since: at, Until: at.Add(time.Hour), Granularity: "hour", Model: "live"}
	got := usageRead(t, db, accountScope(account), q)
	if got.BackfillComplete || got.Totals[0].Requests != "6" {
		t.Fatalf("live calls delayed: %+v", got)
	}
	usageBatch(t, db, 2)
	after := usageRead(t, db, accountScope(account), q)
	if !after.BackfillComplete {
		t.Fatal("backfill did not finish")
	}
	if diff := cmp.Diff(got.Totals, after.Totals); diff != "" {
		t.Fatal(diff)
	}
}

func usageDBBeforeRollup(t *testing.T) (*pg.DB, pgtest.Database) {
	t.Helper()
	database := pgtest.New(t)
	if err := pg.Migrate(t.Context(), database.Migrator, migrationsBeforeObservability(t, "00042")); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, database
}
