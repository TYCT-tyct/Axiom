package audit_test

import (
	"errors"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

func TestAuditSearchFiltersPagesAndScope(t *testing.T) {
	t.Parallel()
	db := open(t)
	ctx := t.Context()
	seedAuditSearch(t, db)

	base := audit.Search{Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour), Limit: 1}
	ids := auditSearchIDs(t, db, base)

	if len(ids) != 3 || ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatalf("pages: %v", ids)
	}
	for _, tc := range []struct {
		name  string
		edit  func(*audit.Search)
		scope pg.Scope
		want  int
	}{
		{"all", func(*audit.Search) {}, platform, 3},
		{"later cursor", func(q *audit.Search) { q.After = &page.Cursor{At: q.Until.Add(time.Hour), ID: uuid.NewV7()} }, platform, 3},
		{"account", func(q *audit.Search) { q.Account = account.String() }, platform, 1},
		{"site", func(q *audit.Search) { q.Site = site.String() }, platform, 1},
		{"action", func(q *audit.Search) { q.Action = "not.found" }, platform, 0},
		{"actor", func(q *audit.Search) { q.ActorID = "catalog.sync"; q.ActorKind = "system" }, platform, 3},
		{"target", func(q *audit.Search) { q.TargetID = "m"; q.TargetKind = "model"; q.Module = "catalog" }, platform, 3},
		{"request", func(q *audit.Search) { q.RequestID = uuid.NewV7().String() }, platform, 0},
		{"RLS", func(*audit.Search) {}, pg.NewScope(false, nil, []uuid.UUID{account}), 1},
		{"empty scope", func(*audit.Search) {}, pg.Scope{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			q.After = nil
			q.Limit = 20
			tc.edit(&q)
			rows := auditSearch(t, db, tc.scope, q)
			if len(rows) != tc.want {
				t.Fatalf("got %d want %d", len(rows), tc.want)
			}
		})
	}
	if err := db.ReadTx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		_, err := audit.Get(ctx, tx, ids[0])
		if !errors.Is(err, pg.ErrNoRows) {
			t.Fatalf("empty scope read: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReadTx(ctx, platform, func(tx pg.Tx) error {
		row, err := audit.Get(ctx, tx, ids[0])
		if err == nil && !strings.Contains(string(row.Detail), `"n"`) {
			t.Fatal("missing detail")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func seedAuditSearch(t *testing.T, db *pg.DB) {
	t.Helper()
	ctx := t.Context()
	if err := db.Tx(ctx, platform, func(tx pg.Tx) error {
		if err := audit.RegisterSite(ctx, tx, site); err != nil {
			return err
		}
		if err := audit.RegisterAccount(ctx, tx, site, account); err != nil {
			return err
		}
		for i := range 3 {
			e := event()
			e.Detail = map[string]any{"n": i, "amount": int64(9007199254740993)}
			if i == 1 {
				e.Target.Site = site
				e.Target.Account = account
			}
			if err := audit.Record(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func auditSearch(t *testing.T, db *pg.DB, scope pg.Scope, q audit.Search) (rows []audit.Entry) {
	t.Helper()
	if err := db.ReadTx(t.Context(), scope, func(tx pg.Tx) (err error) { rows, _, err = audit.List(t.Context(), tx, q); return err }); err != nil {
		t.Fatal(err)
	}
	return rows
}

func auditSearchIDs(t *testing.T, db *pg.DB, base audit.Search) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	for {
		var rows []audit.Entry
		var next *page.Cursor
		if err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) (err error) { rows, next, err = audit.List(t.Context(), tx, base); return err }); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if next == nil {
			return ids
		}
		base.After = next
	}
}
