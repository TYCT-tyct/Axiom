package billing

import (
	"strings"
	"testing"

	"axiom/internal/platform/pgtest"
)

// TestSubscriptionMigrationReruns: migration 00034 is nontransactional, so a failure after one policy exists must
// leave a state the next run can pass. Every object is guarded and every policy is dropped before it is created; a
// second run of the whole Up section proves the guards, including the one at the first policy.
func TestSubscriptionMigrationReruns(t *testing.T) {
	t.Parallel()

	db := pgtest.New(t, Migrations())
	raw, err := migrations.ReadFile("migrations/00035_subscriptions.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(raw), "-- +goose Down")
	if !ok {
		t.Fatal("the migration has no Down section")
	}
	if err := pgtest.Run(t.Context(), db.Superuser, up); err != nil {
		t.Fatalf("running the Up section again: %v", err)
	}
}
