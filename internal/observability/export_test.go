package observability

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func checkOwnExportFilters(t *testing.T, db *pg.DB) {
	t.Helper()
	account := uuid.MustParse(heavyAccount)
	since, until := window()
	rare := allPages(t, db, OwnQuery{Account: account, Since: since, Until: until, Model: "rare"})
	first, last := rare[0], rare[len(rare)-1]
	absent := uuid.NewV7()
	for _, filter := range []OwnQuery{
		{Model: "rare"},
		{ClientApp: "codex"},
		{Key: &first.Key},
		{Workspace: &first.Workspace},
		{Status: "error"},
		{RequestID: &first.ID},
		{Key: &absent},
		{After: &Cursor{At: last.At, ID: last.ID}},
	} {
		filter.Account, filter.Since, filter.Until = account, since, until
		if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
			return CheckOwnExportSize(t.Context(), tx, filter)
		}); err != nil {
			t.Errorf("filtered export %+v: %v", filter, err)
		}
	}
	var fetched int
	var sizeErr error
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		before, err := fetchedCallRows(t.Context(), tx)
		if err != nil {
			return err
		}
		sizeErr = CheckOwnExportSize(t.Context(), tx, OwnQuery{Account: account, Since: since, Until: until})
		if !errors.Is(sizeErr, ErrExportSize) {
			return sizeErr
		}
		after, err := fetchedCallRows(t.Context(), tx)
		fetched = after - before
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sizeErr, ErrExportSize) {
		t.Fatalf("oversized export: %v", sizeErr)
	}
	if fetched > MaxOwnExportRows+1 {
		t.Errorf("probe fetched %d rows, want at most %d", fetched, MaxOwnExportRows+1)
	}
}

func fetchedCallRows(ctx context.Context, tx pg.Tx) (n int, err error) {
	err = tx.QueryRow(ctx, `SELECT coalesce(sum(coalesce(idx_tup_fetch, 0) + seq_tup_read), 0)::int
        FROM pg_stat_xact_user_tables WHERE relname LIKE 'call_log\_20%' OR relname = 'call_log_default'`).Scan(&n)
	return n, err
}
