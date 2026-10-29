package observability

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

func TestOwnFiltersAndAggregates(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	at := time.Date(2026, 10, 28, 23, 59, 59, 0, time.UTC)
	a, b, c, outsider := record(account, at), record(account, at.Add(time.Second)), record(account, at.Add(2*time.Second)), record(uuid.NewV7(), at)
	a.PriceNano, b.PriceNano, c.PriceNano = math.MaxInt64, math.MaxInt64, 0
	b.Workspace, b.Key, b.Model, b.ClientApp = a.Workspace, a.Key, a.Model, a.ClientApp
	a.ErrorKind, b.ErrorKind, c.ErrorKind, c.Model = "", "upstream", "", "other"
	keep(t, db, a, b, c, outsider)
	base := OwnQuery{Account: account, Since: at, Until: at.Add(3 * time.Second), PageSize: 1}
	for name, tc := range map[string]struct {
		key, workspace, request *uuid.UUID
		status                  string
		want                    []uuid.UUID
	}{
		"key":               {key: &a.Key, want: []uuid.UUID{b.ID, a.ID}},
		"workspace":         {workspace: &a.Workspace, want: []uuid.UUID{b.ID, a.ID}},
		"success":           {status: "success", want: []uuid.UUID{c.ID, a.ID}},
		"error":             {status: "error", want: []uuid.UUID{b.ID}},
		"request":           {request: &a.ID, want: []uuid.UUID{a.ID}},
		"combined":          {key: &a.Key, workspace: &a.Workspace, status: "error", request: &b.ID, want: []uuid.UUID{b.ID}},
		"wrong combination": {key: &c.Key, workspace: &a.Workspace},
		"foreign request":   {request: &outsider.ID},
		"foreign key":       {key: &outsider.Key},
		"foreign workspace": {workspace: &outsider.Workspace},
	} {
		t.Run(name, func(t *testing.T) {
			q := base
			q.Key, q.Workspace, q.RequestID, q.Status = tc.key, tc.workspace, tc.request, tc.status
			checkOwnFilter(t, db, q, tc.want)
		})
	}
	checkOwnGroupTotals(t, db, base)
	base.Account = outsider.Account
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		rows, err := AggregateOwn(t.Context(), tx, base, "day")
		if len(rows) != 0 {
			t.Errorf("foreign totals: %+v", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func checkOwnFilter(t *testing.T, db *pg.DB, q OwnQuery, want []uuid.UUID) {
	t.Helper()
	calls := allPages(t, db, q)
	var ids []uuid.UUID
	for _, c := range calls {
		ids = append(ids, c.ID)
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("IDs %v, want %v", ids, want)
	}
	count := 0
	for _, row := range ownAggregates(t, db, q, "day") {
		n, err := strconv.Atoi(row.Calls)
		if err != nil {
			t.Fatal(err)
		}
		count += n
	}
	if count != len(want) {
		t.Errorf("aggregate count %d, want %d", count, len(want))
	}
}

func ownAggregates(t *testing.T, db *pg.DB, q OwnQuery, group string) (rows []OwnAggregate) {
	t.Helper()
	if err := db.ReadTx(t.Context(), accountScope(q.Account), func(tx pg.Tx) (err error) {
		rows, err = AggregateOwn(t.Context(), tx, q, group)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func checkOwnGroupTotals(t *testing.T, db *pg.DB, q OwnQuery) {
	t.Helper()
	for _, group := range []string{"day", "model", "key"} {
		rows := ownAggregates(t, db, q, group)
		if len(rows) != 2 {
			t.Fatalf("%s: %+v", group, rows)
		}
		if group == "day" {
			if rows[0].Bucket != "2026-10-28" || rows[1].Bucket != "2026-10-29" {
				t.Errorf("UTC buckets %+v", rows)
			}
			continue
		}
		if !slices.ContainsFunc(rows, func(row OwnAggregate) bool { return row.PriceNano == "18446744073709551614" && row.Calls == "2" }) {
			t.Errorf("%s loses precision: %+v", group, rows)
		}
	}
}

func TestOwnAggregateRejectsPartialTotals(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	for offset := 0; offset < maxOwnAggregateGroups+1; offset += 8 {
		var rows []CallRecord
		for n := offset; n < min(offset+8, maxOwnAggregateGroups+1); n++ {
			r := record(account, now)
			r.Model = fmt.Sprintf("model-%d", n)
			rows = append(rows, r)
		}
		keep(t, db, rows...)
	}
	err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		_, err := AggregateOwn(t.Context(), tx, day(account), "model")
		return err
	})
	if !errors.Is(err, ErrAggregateSize) {
		t.Fatalf("got %v", err)
	}
}

// Reuse #408's production-query EXPLAIN and first/deep block comparison, under both RLS scopes.
func checkOwnFilterPlans(t *testing.T, db *pg.DB) {
	t.Helper()
	account := uuid.MustParse(heavyAccount)
	since, until := window()
	denseWorkspace, sparseWorkspace := uuid.MustParse("00000000-0000-4000-8000-000000000111"), uuid.MustParse("00000000-0000-4000-8000-000000000112")
	denseKey, sparseKey := uuid.MustParse("00000000-0000-4000-8000-000000000121"), uuid.MustParse("00000000-0000-4000-8000-000000000122")
	cases := []OwnQuery{{Key: &denseKey}, {Key: &sparseKey}, {Workspace: &denseWorkspace}, {Workspace: &sparseWorkspace}, {Status: "success"}, {Status: "error"}, {Key: &denseKey, Workspace: &denseWorkspace, Status: "success", Model: "m", ClientApp: "cli"}, {Key: &sparseKey, Workspace: &sparseWorkspace, Status: "error", Model: "rare", ClientApp: "codex"}}
	for n, q := range cases {
		q.Account, q.Since, q.Until, q.PageSize = account, since, until, pageSize
		calls := allPages(t, db, q)
		if len(calls) == 0 {
			t.Fatal("empty plan fixture")
		}
		deep := calls[len(calls)*3/4]
		for _, scope := range []pg.Scope{platformScope, accountScope(account)} {
			first := explain(t, db, q, scope)
			q.After = &Cursor{At: deep.At, ID: deep.ID}
			t.Logf("new filter %d", n)
			checkListPagePlans(t, first, explain(t, db, q, scope))
			q.After = nil
		}
	}
	absent := uuid.NewV7()
	for _, q := range []OwnQuery{{Key: &absent}, {Workspace: &absent}, {RequestID: &absent}} {
		q.Account, q.Since, q.Until = account, since, until
		for _, scope := range []pg.Scope{platformScope, accountScope(account)} {
			plan := explain(t, db, q, scope)
			checkListPagePlans(t, plan, plan)
		}
	}
}

func TestOwnAggregateKeepsCurrenciesSeparate(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	account := uuid.NewV7()
	usd := record(account, now)
	eur := usd
	eur.ID, eur.Currency = uuid.NewV7(), mustCurrency("EUR")
	usd.PriceNano, eur.PriceNano = 100, 200
	keep(t, db, usd, eur)
	for _, group := range []string{"day", "model", "key"} {
		rows := ownAggregates(t, db, day(account), group)
		if len(rows) != 2 {
			t.Fatalf("currencies merged: %+v", rows)
		}
		want := []OwnAggregate{{Bucket: rows[0].Bucket, Currency: "EUR", Calls: "1", PriceNano: "200"}, {Bucket: rows[0].Bucket, Currency: "USD", Calls: "1", PriceNano: "100"}}
		if !slices.Equal(rows, want) {
			t.Errorf("%s: %+v", group, rows)
		}
	}
}

func TestSparseSuccessReadsFollowTheStatusIndex(t *testing.T) {
	t.Parallel()
	db, database := openDBWithURL(t)
	seedForPlans(t, database, true)
	account := uuid.MustParse(heavyAccount)
	since, until := window()
	q := OwnQuery{Account: account, Since: since, Until: until, Status: "success", PageSize: pageSize}
	calls := allPages(t, db, q)
	if len(calls) != 100 {
		t.Fatalf("success fixture: %d", len(calls))
	}
	deep := calls[75]
	for _, scope := range []pg.Scope{platformScope, accountScope(account)} {
		first := explain(t, db, q, scope)
		q.After = &Cursor{At: deep.At, ID: deep.ID}
		checkListPagePlans(t, first, explain(t, db, q, scope))
		q.After = nil
	}
	var fetched int
	if err := db.ReadTx(t.Context(), accountScope(account), func(tx pg.Tx) error {
		before, err := fetchedCallRows(t.Context(), tx)
		if err != nil {
			return err
		}
		if err := CheckOwnExportSize(t.Context(), tx, q); err != nil {
			return err
		}
		after, err := fetchedCallRows(t.Context(), tx)
		fetched = after - before
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("sparse success export probe fetched %d rows", fetched)
	if fetched != 100 {
		t.Errorf("sparse success export probe fetched %d rows for 100 matches", fetched)
	}
}
