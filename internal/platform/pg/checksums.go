package pg

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
)

// checksumTable records what each applied migration's file was. It is a table of its own rather than a column of
// goose's version tables: goose creates a module's version table when that module first has a migration, which is
// after this record's migration runs.
const checksumTable = "platform.migration_checksums"

// migrationChecksum is one of a module's migrations and the SHA-256 of its file.
type migrationChecksum struct {
	version       int64
	filename      string
	sum           string
	noTransaction bool
}

// migrationChecksums lists a module's migrations in version order with the SHA-256 of each file.
func migrationChecksums(m Module) ([]migrationChecksum, error) {
	names, err := fs.Glob(m.Migrations, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("list the migrations of %s: %w", m.Name, err)
	}
	files := make([]migrationChecksum, 0, len(names))
	for _, name := range names {
		data, err := fs.ReadFile(m.Migrations, name)
		if err != nil {
			return nil, fmt.Errorf("read %s migration %s: %w", m.Name, name, err)
		}
		version, err := goose.NumericComponent(name)
		if err != nil {
			return nil, fmt.Errorf("read the version of %s migration %s: %w", m.Name, name, err)
		}
		digest := sha256.Sum256(data)
		files = append(files, migrationChecksum{
			version:       version,
			filename:      name,
			sum:           hex.EncodeToString(digest[:]),
			noTransaction: noTransaction(data),
		})
	}
	return files, nil
}

// noTransaction reports whether a migration opts out of a transaction, as goose reads its annotation: on a line of its
// own before the Up section. Such a migration commits its earlier statements even when a later one fails.
func noTransaction(data []byte) bool {
	for line := range strings.SplitSeq(string(data), "\n") {
		switch {
		case strings.EqualFold(strings.TrimSpace(line), "-- +goose NO TRANSACTION"):
			return true
		case strings.EqualFold(strings.TrimSpace(line), "-- +goose Up"):
			return false
		}
	}
	return false
}

// execer is what reading and writing the record needs of a database handle: *sql.DB, *sql.Tx, *sql.Conn and goose's
// database.DBTxConn all have it, so one statement records a file inside goose's migration transaction or outside it.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// tableExists reports whether a table exists, without failing on a missing one.
func tableExists(ctx context.Context, db execer, name string) (bool, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// preflight checks every module's history before any pending migration runs. A carried migration whose recorded file
// differs stops the run while the database is still at the versions the previous release left, not after a later
// migration has committed. It baselines the applied migrations of a database migrated before the record existed, and
// freezes the file of a pending NO TRANSACTION migration before that migration can commit a statement. It returns
// whether the record table exists: without it nothing has a record to compare.
func preflight(ctx context.Context, db execer, modules []Module) (bool, error) {
	records, err := tableExists(ctx, db, checksumTable)
	if err != nil {
		return false, fmt.Errorf("read the record of applied migrations: %w", err)
	}
	if !records {
		return false, nil
	}
	var problems []string
	for _, m := range modules {
		found, err := preflightModule(ctx, db, m)
		if err != nil {
			return false, err
		}
		problems = append(problems, found...)
	}
	if len(problems) > 0 {
		return true, errors.New(strings.Join(problems, "; "))
	}
	return true, nil
}

// preflightModule checks one module and returns the carried migrations whose recorded file differs. It writes the
// records a migration applied before the record existed needs, and freezes a pending NO TRANSACTION migration's file,
// because that migration's earlier statements survive a failure.
func preflightModule(ctx context.Context, db execer, m Module) ([]string, error) {
	files, err := migrationChecksums(m)
	if err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, db, m)
	if err != nil {
		return nil, err
	}
	recorded, err := recordedChecksums(ctx, db, m.Name)
	if err != nil {
		return nil, err
	}
	var problems []string
	var baseline []migrationChecksum
	for _, file := range files {
		if rec, ok := recorded[file.version]; ok {
			problem := checksumProblem(m.Name, file.version, file.filename, file.sum, rec.filename, rec.sum)
			if problem != "" {
				problems = append(problems, problem)
			}
			continue
		}
		if applied[file.version] || file.noTransaction {
			baseline = append(baseline, file)
		}
	}
	if err := insertChecksums(ctx, db, m.Name, baseline); err != nil {
		return nil, err
	}
	return problems, nil
}

// appliedVersions is the versions of m the database has applied; a database without the module's version table has
// applied none.
func appliedVersions(ctx context.Context, db execer, m Module) (map[int64]bool, error) {
	applied := map[int64]bool{}
	exists, err := tableExists(ctx, db, versionTable(m.Name))
	if err != nil {
		return nil, fmt.Errorf("read the version table of %s: %w", m.Name, err)
	}
	if !exists {
		return applied, nil
	}
	rows, err := db.QueryContext(ctx, "SELECT version_id FROM "+versionTable(m.Name)+" WHERE is_applied")
	if err != nil {
		return nil, fmt.Errorf("read the versions of %s: %w", m.Name, err)
	}
	defer rows.Close() //nolint:errcheck // the rows are read to the end, which reports the failure that matters
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("read the versions of %s: %w", m.Name, err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the versions of %s: %w", m.Name, err)
	}
	return applied, nil
}

// recordedChecksums is the files the database recorded for a module.
func recordedChecksums(ctx context.Context, db execer, module string) (map[int64]migrationChecksum, error) {
	rows, err := db.QueryContext(ctx, `SELECT version_id, filename, checksum FROM `+checksumTable+` WHERE module = $1`, module)
	if err != nil {
		return nil, fmt.Errorf("read the recorded checksums of %s: %w", module, err)
	}
	defer rows.Close() //nolint:errcheck // the rows are read to the end, which reports the failure that matters
	recorded := map[int64]migrationChecksum{}
	for rows.Next() {
		var file migrationChecksum
		if err := rows.Scan(&file.version, &file.filename, &file.sum); err != nil {
			return nil, fmt.Errorf("read the recorded checksums of %s: %w", module, err)
		}
		recorded[file.version] = file
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the recorded checksums of %s: %w", module, err)
	}
	return recorded, nil
}

// insertChecksums records files and leaves an existing record as it is, so a change after the record is refused
// rather than recorded.
func insertChecksums(ctx context.Context, db execer, module string, files []migrationChecksum) error {
	if len(files) == 0 {
		return nil
	}
	versions, names, sums := make([]int64, len(files)), make([]string, len(files)), make([]string, len(files))
	for i, f := range files {
		versions[i], names[i], sums[i] = f.version, f.filename, f.sum
	}
	insert := `INSERT INTO ` + checksumTable + ` (module, version_id, filename, checksum)
		SELECT $1, f.version_id, f.filename, f.checksum
		FROM unnest($2::bigint[], $3::text[], $4::text[]) AS f(version_id, filename, checksum)
		ON CONFLICT (module, version_id) DO NOTHING`
	if _, err := db.ExecContext(ctx, insert, module, versions, names, sums); err != nil {
		return fmt.Errorf("record the checksums of %s: %w", module, err)
	}
	return nil
}

// recordChecksums records the files of what the database applied and has no record of. Only `up` calls it, and only
// when the record table did not exist before the run: those versions were applied before the record could be written
// beside their version row, so their files now are the baseline once. A version applied while the table exists is
// recorded in the same transaction as its version, never here.
func recordChecksums(ctx context.Context, db execer, m Module) error {
	files, err := migrationChecksums(m)
	if err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, db, m)
	if err != nil {
		return err
	}
	baseline := make([]migrationChecksum, 0, len(files))
	for _, file := range files {
		if applied[file.version] {
			baseline = append(baseline, file)
		}
	}
	return insertChecksums(ctx, db, m.Name, baseline)
}

// checksumDiffQuery lists the migrations this program carries that the database applied and whose recorded file is not
// the one the program has now. Migrations newer than this program's are not among its parameters, so a database from a
// later release does not fail it.
func checksumDiffQuery(module string) string {
	return `SELECT f.version_id, f.filename, f.checksum, coalesce(c.filename, ''), coalesce(c.checksum, '')
		FROM unnest($2::bigint[], $3::text[], $4::text[]) AS f(version_id, filename, checksum)
		JOIN ` + versionTable(module) + ` g ON g.version_id = f.version_id AND g.is_applied
		LEFT JOIN ` + checksumTable + ` c ON c.module = $1 AND c.version_id = f.version_id
		WHERE c.version_id IS NULL OR c.filename <> f.filename OR c.checksum <> f.checksum
		ORDER BY f.version_id`
}

// checksumRows is what reading the recorded checksums needs of a result set; *sql.Rows and pgx.Rows both have it.
type checksumRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// checksumProblem says how a carried migration's file is not the one the database recorded, or "" when they agree.
func checksumProblem(module string, version int64, filename, sum, recordedName, recordedSum string) string {
	switch {
	case recordedSum == "":
		return fmt.Sprintf("%s migration %s has no recorded checksum: run axiom migrate", module, filename)
	case recordedName != filename:
		return fmt.Sprintf("%s migration %d was applied as %s, this program carries %s: "+
			"merged migrations never change; restore the file or add a new migration",
			module, version, recordedName, filename)
	case recordedSum != sum:
		return fmt.Sprintf("%s migration %s changed after it was applied "+
			"(recorded SHA-256 %s, this program carries %s): merged migrations never change; restore the file or "+
			"add a new migration", module, filename, recordedSum, sum)
	}
	return ""
}

// compareChecksums reports the migrations whose file is not the one the database recorded.
func compareChecksums(rows checksumRows, module string) error {
	var problems []string
	for rows.Next() {
		var version int64
		var filename, sum, recordedName, recordedSum string
		if err := rows.Scan(&version, &filename, &sum, &recordedName, &recordedSum); err != nil {
			return fmt.Errorf("read the recorded checksums of %s: %w", module, err)
		}
		if problem := checksumProblem(module, version, filename, sum, recordedName, recordedSum); problem != "" {
			problems = append(problems, problem)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read the recorded checksums of %s: %w", module, err)
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

// verifyChecksums refuses an applied migration whose file is not the one the database recorded. The run stops before a
// node starts against a schema that its recorded history no longer explains.
func verifyChecksums(ctx context.Context, db execer, m Module) error {
	files, err := migrationChecksums(m)
	if err != nil {
		return err
	}
	versions, names, sums := checksumArrays(files)
	rows, err := db.QueryContext(ctx, checksumDiffQuery(m.Name), m.Name, versions, names, sums)
	if err != nil {
		return fmt.Errorf("read the recorded checksums of %s (run axiom migrate first): %w", m.Name, err)
	}
	defer rows.Close() //nolint:errcheck // the rows are read to the end, which reports the failure that matters
	return compareChecksums(rows, m.Name)
}

// checksumArrays is files as the queries' parameters.
func checksumArrays(files []migrationChecksum) (versions []int64, names, sums []string) {
	versions, names, sums = make([]int64, len(files)), make([]string, len(files)), make([]string, len(files))
	for i, f := range files {
		versions[i], names[i], sums[i] = f.version, f.filename, f.sum
	}
	return versions, names, sums
}

// checksumStore writes an applied migration's file in the same transaction as its version row: goose hands Insert the
// transaction the migration ran in, so a failed run cannot leave a version whose file was not recorded.
type checksumStore struct {
	database.Store
	module string
	files  map[int64]migrationChecksum
	found  bool // the record table exists, so it is not looked for again
}

// Insert records the version and, in the same transaction, the file it was applied from.
func (s *checksumStore) Insert(ctx context.Context, db database.DBTxConn, req database.InsertRequest) error {
	if err := s.Store.Insert(ctx, db, req); err != nil {
		return err
	}
	file, ok := s.files[req.Version]
	if !ok {
		return nil
	}
	ready, err := s.ready(ctx, db)
	if err != nil || !ready {
		return err
	}
	return insertChecksums(ctx, db, s.module, []migrationChecksum{file})
}

// Delete removes the version and the record of its file, so a later up records the file it then applies.
func (s *checksumStore) Delete(ctx context.Context, db database.DBTxConn, version int64) error {
	if err := s.Store.Delete(ctx, db, version); err != nil {
		return err
	}
	ready, err := s.ready(ctx, db)
	if err != nil || !ready {
		return err
	}
	deleteRecord := `DELETE FROM ` + checksumTable + ` WHERE module = $1 AND version_id = $2`
	if _, err := db.ExecContext(ctx, deleteRecord, s.module, version); err != nil {
		return fmt.Errorf("delete the recorded checksum of %s migration %d: %w", s.module, version, err)
	}
	return nil
}

// ready reports whether the record table exists. It is looked for before each insert until found: the migration that
// creates it is itself applied before the table can be recorded.
func (s *checksumStore) ready(ctx context.Context, db database.DBTxConn) (bool, error) {
	if s.found {
		return true, nil
	}
	exists, err := tableExists(ctx, db, checksumTable)
	if err != nil {
		return false, fmt.Errorf("read the record of applied migrations: %w", err)
	}
	s.found = exists
	return exists, nil
}
