package suppliers

import (
	"context"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/pg"
)

func TestCallWritesPreservePlatformAndTenantOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		site, account uuid.UUID
		scope         pg.Scope
		want          []*uuid.UUID
	}{
		{"platform", uuid.UUID{}, uuid.UUID{}, platformScope, []*uuid.UUID{nil, nil}},
		{"tenant", siteA, accountA, pg.NewScope(false, []uuid.UUID{siteA}, nil), []*uuid.UUID{&siteA, &accountA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, 1)
			id := newRequest()
			a := w.attempt(id, 1, "")
			a.Site, a.Account = tc.site, tc.account
			if err := w.db.Tx(t.Context(), tc.scope, func(tx pg.Tx) error {
				writes := CallWrites{}
				if err := writes.RecordAttempts(t.Context(), tx, []AttemptRecord{a}); err != nil {
					return err
				}
				return writes.EndRequest(t.Context(), tx, uuid.UUID(id), tc.site, tc.account, w.clock.now())
			}); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"attempt_costs", "ended_requests"} {
				var site, account *uuid.UUID
				w.tx(func(ctx context.Context, tx pg.Tx) error {
					return tx.QueryRow(ctx, "SELECT site_id, account_id FROM suppliers."+table+" WHERE request_id = $1", uuid.UUID(id)).Scan(&site, &account)
				})
				if diff := cmp.Diff(tc.want, []*uuid.UUID{site, account}); diff != "" {
					t.Errorf("%s tenant (-want +got): %s", table, diff)
				}
			}
		})
	}
}
