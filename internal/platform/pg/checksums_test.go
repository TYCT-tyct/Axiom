package pg_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// migrationChecksums is what the database recorded for a module: version to file name and SHA-256.
func migrationChecksums(t *testing.T, db *pg.DB, module string) map[int64][2]string {
	t.Helper()
	recorded := map[int64][2]string{}
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(),
			`SELECT version_id, filename, checksum FROM platform.migration_checksums WHERE module = $1`, module)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var version int64
			var filename, checksum string
			if err := rows.Scan(&version, &filename, &checksum); err != nil {
				return err
			}
			recorded[version] = [2]string{filename, checksum}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return recorded
}

// sum is the SHA-256 of data as the record writes it.
func sum(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// qualityMigrations is a module of two migrations, as another module's migrations are; the second stands for one a
// later change leaves alone.
func qualityMigrations(first string) pg.Module {
	return pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_first.sql":  {Data: []byte(up + first)},
		"00002_second.sql": {Data: []byte(up + "SELECT 2;")},
	}}
}

// appliedVersion is the highest version of a module the database applied. It reads as the superuser: a run that failed
// before its module finished has not granted the application the version table yet.
func appliedVersion(t *testing.T, database pgtest.Database, module string) int64 {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context()) //nolint:errcheck // the test is over when it closes
	var version int64
	if err := conn.QueryRow(t.Context(),
		"SELECT coalesce(max(version_id), 0) FROM platform.goose_"+module+" WHERE is_applied").Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

// TestMigrateRecordsEachAppliedMigrationsChecksum: every migration this program applies is recorded with the SHA-256 of
// its file, once; a later run adds nothing and overwrites nothing. The platform migrations of the test template are
// recorded too, so a database this program finds already migrated has its history from the start.
func TestMigrateRecordsEachAppliedMigrationsChecksum(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	quality := qualityMigrations("SELECT 1;")
	if err := pg.Migrate(ctx, database.Migrator, quality); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	want := map[int64][2]string{
		1: {"00001_first.sql", sum([]byte(up + "SELECT 1;"))},
		2: {"00002_second.sql", sum([]byte(up + "SELECT 2;"))},
	}
	if got := migrationChecksums(t, db, "quality"); len(got) != len(want) || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("the record of quality is %v, want %v", got, want)
	}
	platform := migrationChecksums(t, db, "platform")
	if len(platform) == 0 {
		t.Error("the template's platform migrations were not recorded")
	}
	if err := pg.Migrate(ctx, database.Migrator, quality); err != nil {
		t.Fatalf("the run after the migrations were recorded: %v", err)
	}
	if got := migrationChecksums(t, db, "quality"); len(got) != len(want) || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("a later run changed the record to %v, want %v", got, want)
	}
}

// TestAChangedHistoryStopsBeforeAnyPendingMigrationRuns: every module's history is checked before any pending migration
// runs. routing's pending migration would run first, but operations' applied file changed, so it must not run.
func TestAChangedHistoryStopsBeforeAnyPendingMigrationRuns(t *testing.T) {
	database := pgtest.New(t,
		pg.Module{Name: "routing", Migrations: fstest.MapFS{
			"00001_first.sql": {Data: []byte(up + "CREATE TABLE routing.one (id int);")},
		}},
		pg.Module{Name: "operations", Migrations: fstest.MapFS{
			"00001_first.sql": {Data: []byte(up + "CREATE TABLE operations.one (id int);")},
		}},
	)
	routing := pg.Module{Name: "routing", Migrations: fstest.MapFS{
		"00001_first.sql":  {Data: []byte(up + "CREATE TABLE routing.one (id int);")},
		"00002_second.sql": {Data: []byte(up + "CREATE TABLE routing.two (id int);")},
	}}
	operations := pg.Module{Name: "operations", Migrations: fstest.MapFS{
		"00001_first.sql": {Data: []byte(up + "CREATE TABLE operations.one (id int, changed int);")},
	}}
	err := pg.Migrate(t.Context(), database.Migrator, routing, operations)
	want := "operations migration 00001_first.sql changed after it was applied"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Migrate with a later module's changed history: %v, want %q", err, want)
	}
	if got := appliedVersion(t, database, "routing"); got != 1 {
		t.Errorf("routing migration 2 ran before the changed history was reported (version %d)", got)
	}
}

// TestAFailedRunRecordsTheMigrationItApplied: a migration that commits before a later one fails is recorded in the same
// transaction as its version, so a retry cannot mistake a changed file for the file that ran. The failed migration
// itself is transactional: its file may still be fixed, and the fixed file runs on the retry.
func TestAFailedRunRecordsTheMigrationItApplied(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	first := up + "CREATE TABLE quality.one (id int);"
	failing := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_first.sql":  {Data: []byte(first)},
		"00002_second.sql": {Data: []byte(up + "SELECT * FROM quality.no_such_table;")},
	}}
	if err := pg.Migrate(ctx, database.Migrator, failing); err == nil {
		t.Fatal("Migrate with a failing second migration: want an error")
	}
	db := open(t, database.App)
	if got := migrationChecksums(t, db, "quality")[1]; got != [2]string{"00001_first.sql", sum([]byte(first))} {
		t.Errorf("the file of the committed migration is not recorded: %v", got)
	}
	if _, ok := migrationChecksums(t, db, "quality")[2]; ok {
		t.Error("the failed migration was recorded")
	}
	changed := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_first.sql":  {Data: []byte(up + "CREATE TABLE quality.one (id int, changed int);")},
		"00002_second.sql": {Data: []byte(up + "SELECT 2;")},
	}}
	if err := pg.Migrate(ctx, database.Migrator, changed); err == nil ||
		!strings.Contains(err.Error(), "00001_first.sql changed after it was applied") {
		t.Errorf("Migrate after changing the applied file: %v", err)
	}
	if got := appliedVersion(t, database, "quality"); got != 1 {
		t.Errorf("the pending migration ran before the changed history was reported (version %d)", got)
	}
	restored := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_first.sql":  {Data: []byte(first)},
		"00002_second.sql": {Data: []byte(up + "SELECT 2;")},
	}}
	if err := pg.Migrate(ctx, database.Migrator, restored); err != nil {
		t.Fatalf("Migrate after restoring the applied file and fixing the failed one: %v", err)
	}
	if got := appliedVersion(t, database, "quality"); got != 2 {
		t.Errorf("the fixed migration did not run (version %d)", got)
	}
}

// TestAFailedNonTransactionalMigrationIsFrozenBeforeItRuns: a migration that opts out of a transaction commits its
// earlier statements even when a later one fails, so its file is recorded before it runs and a retry with a changed
// file is refused before it executes anything.
func TestAFailedNonTransactionalMigrationIsFrozenBeforeItRuns(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	broken := "-- +goose NO TRANSACTION\n" + up +
		"CREATE TABLE quality.partial (id int);\nSELECT * FROM quality.no_such_table;"
	module := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_partial.sql": {Data: []byte(broken)},
	}}
	if err := pg.Migrate(ctx, database.Migrator, module); err == nil {
		t.Fatal("Migrate with a failing migration: want an error")
	}
	db := open(t, database.App)
	if got := migrationChecksums(t, db, "quality")[1]; got != [2]string{"00001_partial.sql", sum([]byte(broken))} {
		t.Errorf("the file was not recorded before it ran: %v", got)
	}
	changed := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_partial.sql": {Data: []byte("-- +goose NO TRANSACTION\n" + up +
			"CREATE TABLE quality.changed (id int);\nSELECT 1;")},
	}}
	if err := pg.Migrate(ctx, database.Migrator, changed); err == nil ||
		!strings.Contains(err.Error(), "00001_partial.sql changed after it was applied") {
		t.Errorf("Migrate with the changed non-transactional file: %v", err)
	}
	if got := appliedVersion(t, database, "quality"); got != 0 {
		t.Errorf("the changed non-transactional migration ran (version %d)", got)
	}
}

// TestChangedMigrationStopsTheRunAndTheNode: a merged migration whose file changes after it was applied makes both
// `axiom migrate` and every node's start fail, naming the file and both checksums; restoring the file, or renaming it,
// is refused as well until the record and the program agree again.
func TestChangedMigrationStopsTheRunAndTheNode(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	original := qualityMigrations("SELECT 1;")
	if err := pg.Migrate(ctx, database.Migrator, original); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	changed := qualityMigrations("SELECT 3;")
	want := "quality migration 00001_first.sql changed after it was applied"
	if err := pg.Migrate(ctx, database.Migrator, changed); err == nil ||
		!strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), sum([]byte(up+"SELECT 1;"))) {
		t.Errorf("Migrate with a changed migration: %v, want %q and the recorded checksum", err, want)
	}
	if err := db.Check(ctx, changed); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "merged migrations never change") {
		t.Errorf("Check with a changed migration: %v, want %q", err, want)
	}
	renamed := pg.Module{Name: "quality", Migrations: fstest.MapFS{
		"00001_renamed.sql": {Data: []byte(up + "SELECT 1;")},
		"00002_second.sql":  {Data: []byte(up + "SELECT 2;")},
	}}
	if err := db.Check(ctx, renamed); err == nil ||
		!strings.Contains(err.Error(), "quality migration 1 was applied as 00001_first.sql, this program carries 00001_renamed.sql") {
		t.Errorf("Check with a renamed migration: %v", err)
	}
	if err := db.Check(ctx, original); err != nil {
		t.Errorf("Check after restoring the file: %v", err)
	}
}

// TestMigrateRecordsWhatWasAppliedBeforeTheRecordExisted: a database migrated before the checksum record existed has
// no row for its applied migrations. A node refuses to start until migrate records them, and then they are recorded as
// they are, the first-run baseline that later changes are compared with.
func TestMigrateRecordsWhatWasAppliedBeforeTheRecordExisted(t *testing.T) {
	database := pgtest.New(t)
	ctx := t.Context()
	quality := qualityMigrations("SELECT 1;")
	if err := pg.Migrate(ctx, database.Migrator, quality); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	if err := pgtest.Run(ctx, database.Superuser,
		`DELETE FROM platform.migration_checksums WHERE module = 'quality' AND version_id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := db.Check(ctx, quality); err == nil ||
		!strings.Contains(err.Error(), "quality migration 00001_first.sql has no recorded checksum: run axiom migrate") {
		t.Errorf("Check without a record: %v", err)
	}
	if err := pg.Migrate(ctx, database.Migrator, quality); err != nil {
		t.Fatalf("the run that records what was applied: %v", err)
	}
	if err := db.Check(ctx, quality); err != nil {
		t.Errorf("Check after recording it: %v", err)
	}
}

// TestAnOlderNodeStartsAgainstTheNewSchema: during the rolling release a node of the previous release runs against a
// database this release migrated. It refuses a database that lacks one of its own migrations, and the new platform
// migration is above its newest, which it allows; it also audits every table in Axiom's schemas and knows nothing of
// the checksum record. Its audit is frozen here: an unknown table in platform is an offender unless it forces
// row-level security and has a policy, so the record must do both, and nothing else this migration does is read by
// the previous release's start. This program's own check is the same plus the checksum verification.
func TestAnOlderNodeStartsAgainstTheNewSchema(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	db := open(t, database.App)
	if err := db.Check(t.Context()); err != nil {
		t.Fatalf("this program's start on the schema it migrated: %v", err)
	}
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		var audited bool
		if err := tx.QueryRow(t.Context(), `SELECT c.relrowsecurity AND c.relforcerowsecurity
			AND EXISTS (SELECT FROM pg_policy WHERE polrelid = c.oid)
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = 'platform' AND c.relname = 'migration_checksums'`).Scan(&audited); err != nil {
			return err
		}
		if !audited {
			t.Error("the previous release's audit refuses platform.migration_checksums")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAChecksumRecordOfANewerMigrationIsIgnored: a database migrated by a later release records migrations this
// program does not carry. Starting against it is allowed, because migrations only expand, and the records of versions
// above this program's newest are not compared: a node of the previous release keeps running through the rolling
// release.
func TestAChecksumRecordOfANewerMigrationIsIgnored(t *testing.T) {
	t.Parallel()
	database := pgtest.New(t)
	if err := pgtest.Run(t.Context(), database.Superuser,
		`INSERT INTO platform.goose_platform (version_id, is_applied) VALUES (99999, true)`,
		`INSERT INTO platform.migration_checksums (module, version_id, filename, checksum)
			VALUES ('platform', 99999, '99999_future.sql', repeat('0', 64))`); err != nil {
		t.Fatal(err)
	}
	if err := open(t, database.App).Check(t.Context()); err != nil {
		t.Errorf("Check against a database of a later release: %v", err)
	}
}
