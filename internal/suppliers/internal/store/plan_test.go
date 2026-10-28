package store_test

import (
	"context"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// TestPlanVersionsReadsOneRowOfAVersionStoredTwice: one schedule, confirmed by a person and then published by the
// supplier, keeps one fingerprint and is stored twice. Reading it back gives one row, the newest, so the basis a cost
// schedule records is where the price came from rather than a matter of the order the rows come in.
func TestPlanVersionsReadsOneRowOfAVersionStoredTwice(t *testing.T) {
	t.Parallel()
	db := open(t)
	w := seed(t, db)
	supplier := w.supplier["platform"]
	key := store.PlanVersion{Endpoint: "default/gpt", Fingerprint: "5f2a1c0e"}
	err := run(t, db, platform, func(ctx context.Context, tx pg.Tx) error {
		for i, basis := range []string{"confirmed", "published"} {
			err := store.InsertSyncPlans(ctx, tx, supplier, int64(i+1), []store.SyncPlanRow{{
				Endpoint: key.Endpoint, Fingerprint: key.Fingerprint, Basis: basis,
				Schedule: `{"v":1,"currency":"XXX","rates":{"input":"0.075","output":"0.3"}}`,
			}})
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []store.PlanVersion
	err = run(t, db, platform, func(ctx context.Context, tx pg.Tx) (err error) {
		got, err = store.PlanVersions(ctx, tx, supplier, []store.PlanVersion{key})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Endpoint != key.Endpoint || got[0].Fingerprint != key.Fingerprint || got[0].Basis != "published" {
		t.Errorf("PlanVersions = %+v, want the one row of the version, with the basis of the newest: published", got)
	}
}
