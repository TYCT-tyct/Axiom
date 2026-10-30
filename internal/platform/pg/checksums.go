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
)

// checksumTable records what each applied migration's file was. It is a table of its own rather than a column of
// goose's version tables: goose creates a module's version table when that module first has a migration, which is
// after this record's migration runs.
const checksumTable = "platform.migration_checksums"

// migrationChecksum is one of a module's migrations and the SHA-256 of its file.
type migrationChecksum struct {
	version  int64
	filename string
	sum      string
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
		sum := sha256.Sum256(data)
		files = append(files, migrationChecksum{version: version, filename: name, sum: hex.EncodeToString(sum[:])})
	}
	return files, nil
}

// checksumArrays is files as the queries' parameters.
func checksumArrays(files []migrationChecksum) (versions []int64, names, sums []string) {
	versions, names, sums = make([]int64, len(files)), make([]string, len(files)), make([]string, len(files))
	for i, f := range files {
		versions[i], names[i], sums[i] = f.version, f.filename, f.sum
	}
	return versions, names, sums
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

// recordChecksums records what each applied migration's file is, for the versions the database has no record of. A
// database migrated before this record existed gets its files as they are now, once; a recorded version is never
// overwritten, so a change after that is refused rather than recorded.
func recordChecksums(ctx context.Context, db *sql.DB, m Module) error {
	files, err := migrationChecksums(m)
	if err != nil {
		return err
	}
	versions, names, sums := checksumArrays(files)
	//nolint:gosec // ordered admits only the names in moduleOrder
	record := `INSERT INTO ` + checksumTable + ` (module, version_id, filename, checksum)
		SELECT $1, f.version_id, f.filename, f.checksum
		FROM unnest($2::bigint[], $3::text[], $4::text[]) AS f(version_id, filename, checksum)
		JOIN ` + versionTable(m.Name) + ` g ON g.version_id = f.version_id AND g.is_applied
		ON CONFLICT (module, version_id) DO NOTHING`
	if _, err := db.ExecContext(ctx, record, m.Name, versions, names, sums); err != nil {
		return fmt.Errorf("record the checksums of %s: %w", m.Name, err)
	}
	return nil
}

// verifyChecksums refuses an applied migration whose file is not the one the database recorded. The run stops before a
// node starts against a schema that its recorded history no longer explains.
func verifyChecksums(ctx context.Context, db *sql.DB, m Module) error {
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

// compareChecksums reports the migrations whose file is not the one the database recorded.
func compareChecksums(rows checksumRows, module string) error {
	var problems []string
	for rows.Next() {
		var version int64
		var filename, sum, recordedName, recordedSum string
		if err := rows.Scan(&version, &filename, &sum, &recordedName, &recordedSum); err != nil {
			return fmt.Errorf("read the recorded checksums of %s: %w", module, err)
		}
		switch {
		case recordedSum == "":
			problems = append(problems, fmt.Sprintf("%s migration %s has no recorded checksum: run axiom migrate",
				module, filename))
		case recordedName != filename:
			problems = append(problems, fmt.Sprintf("%s migration %d was applied as %s, this program carries %s: "+
				"merged migrations never change; restore the file or add a new migration",
				module, version, recordedName, filename))
		default:
			problems = append(problems, fmt.Sprintf("%s migration %s changed after it was applied "+
				"(recorded SHA-256 %s, this program carries %s): merged migrations never change; restore the file or "+
				"add a new migration", module, filename, recordedSum, sum))
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
