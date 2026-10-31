package observability

import (
	"context"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestCheckRetentionRole: the application login cannot set the job role, while the retention login can; startup uses
// this to refuse a URL whose grant is missing instead of running a job that fails every day.
func TestCheckRetentionRole(t *testing.T) {
	t.Parallel()
	app, database := openDBWithURL(t)
	if err := CheckRetentionRole(t.Context(), app, platformScope); err == nil {
		t.Fatal("the application login passed the retention role check")
	}
	retention := openRetention(t, database)
	if err := CheckRetentionRole(t.Context(), retention, platformScope); err != nil {
		t.Fatal(err)
	}
}

// TestRetentionRoleCanBeGrantedAfterMigration: a database migrated before the deployment created its login has the job
// role but no membership. The startup check refuses that state, and the grant deploy/postgres/roles.sh repeats on every
// start enables it without another migration. The test is not parallel: it changes a cluster-wide membership, then
// restores it.
func TestRetentionRoleCanBeGrantedAfterMigration(t *testing.T) {
	_, database := openDBWithURL(t)
	retention := openRetention(t, database)
	if err := pgtest.Run(t.Context(), database.Superuser, `REVOKE axiom_retention_job FROM axiom_retention GRANTED BY axiom_owner`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := pgtest.Run(ctx, database.Superuser, `GRANT axiom_retention_job TO axiom_retention WITH INHERIT TRUE, SET TRUE`); err != nil {
			t.Errorf("restore retention membership: %v", err)
		}
	})
	if err := CheckRetentionRole(t.Context(), retention, platformScope); err == nil {
		t.Fatal("a login without the job role passed the startup check")
	}
	if err := pgtest.Run(t.Context(), database.Superuser, `GRANT axiom_retention_job TO axiom_retention WITH INHERIT TRUE, SET TRUE`); err != nil {
		t.Fatal(err)
	}
	if err := CheckRetentionRole(t.Context(), retention, platformScope); err != nil {
		t.Fatalf("the repeated deployment grant did not enable retention: %v", err)
	}
}
