package persist_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/syncer"
)

// Every reason the projection writes can recover with the same plan. Reconciliation's
// verdict survives repeated listings, including a disappearance, until the plan changes.
func TestResyncPreservesReconciliationUntilThePlanChanges(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{
		"under_estimated", string(syncer.Unmapped), string(syncer.NotListed),
		string(syncer.NoFormat), string(syncer.CostUnknown), string(syncer.Gone),
	} {
		t.Run(reason, func(t *testing.T) {
			c := newCluster(t)
			c.model("gpt-4o", catalog.StatusListed)
			s := c.acquire(c.node("a"))
			groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
				{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
			}}}
			c.commitListing(s, 1, groups)
			err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = $1
					WHERE supplier_id = $2`, reason, c.supplier)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			want := "known -"
			if reason == "under_estimated" {
				want = "unknown under_estimated"
			}
			c.commitListing(s, 2, groups)
			same(t, "same fingerprint", c.column("endpoints", "cost_state || ' ' || coalesce(cost_state_reason, '-')", "id"), []string{want})
			c.commitListing(s, 3, nil)
			c.commitListing(s, 4, groups)
			same(t, "same fingerprint returning", c.column("endpoints", "cost_state || ' ' || coalesce(cost_state_reason, '-')", "id"), []string{want})
			groups[0].models[0].plan = &plusVAT
			c.commitListing(s, 5, groups)
			same(t, "new fingerprint", c.column("endpoints", "cost_state || ' ' || coalesce(cost_state_reason, '-')", "id"), []string{"known -"})
		})
	}
}

// TestNewFactorsAtTheSamePricesKeepReconciliationsVerdict: a plan whose factors changed and whose prices did not
// (a model ratio of 2 and a group ratio of 1 become 1 and 2) is a new version with another fingerprint, which the
// endpoint takes up, so its explanation is the current one; but its estimate is the one reconciliation found too low,
// so the endpoint stays out of routing until a price changes. A verdict of the projection's own is lifted as before.
func TestNewFactorsAtTheSamePricesKeepReconciliationsVerdict(t *testing.T) {
	t.Parallel()
	refactored := priced{"c4e9a7b2", unitPrice.schedule, unitPrice.basis}
	for reason, want := range map[string]string{
		"under_estimated":          "unknown under_estimated c4e9a7b2",
		string(syncer.CostUnknown): "known - c4e9a7b2",
	} {
		t.Run(reason, func(t *testing.T) {
			c := newCluster(t)
			c.model("gpt-4o", catalog.StatusListed)
			s := c.acquire(c.node("a"))
			groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
				{name: "gpt-4o", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
			}}}
			c.commitListing(s, 1, groups)
			err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE suppliers.endpoints SET cost_state = 'unknown', cost_state_reason = $1
					WHERE supplier_id = $2`, reason, c.supplier)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			state := func() []string {
				return c.column("endpoints", "cost_state || ' ' || coalesce(cost_state_reason, '-') || ' ' || "+
					"(SELECT cs.fingerprint FROM suppliers.cost_schedules cs WHERE cs.id = endpoints.cost_schedule_id)", "id")
			}
			groups[0].models[0].plan = &refactored
			c.commitListing(s, 2, groups)
			same(t, "the same prices under new factors", state(), []string{want})
			c.commitListing(s, 3, groups)
			same(t, "listed again", state(), []string{want})
			groups[0].models[0].plan = &plusVAT
			c.commitListing(s, 4, groups)
			same(t, "a new price", state(), []string{"known - 8b1d3f44"})
		})
	}
}
