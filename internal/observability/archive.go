package observability

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/pg"
)

// The tables whose partitions are archived together. call_attempt is dropped first: the same rule the writer relies
// on, a call names its attempts, so an interrupted drop leaves the call readable rather than the attempts orphaned.
var archiveTables = []string{"call_attempt", "call_log"}

const archiveFormat = "postgresql-copy-csv-gzip"

var partitionName = regexp.MustCompile(`^(call_log|call_attempt)_([0-9]{4})_([0-9]{2})$`)

// errNothingToArchive says a month has neither partition left and no manifest; the run logs it and moves on.
var errNothingToArchive = errors.New("observability: nothing left to archive")

// pastWindow lists the whole months whose end is not after cutoff, oldest first. Only leaf partitions named for a month
// are candidates; the default partition has no month and stays where it is.
func (r *retainer) pastWindow(ctx context.Context, cutoff time.Time) ([]time.Time, error) {
	seen := make(map[time.Time]bool)
	for _, table := range archiveTables {
		names, err := r.leafPartitions(ctx, table)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			month, ok := monthOf(table, name)
			if !ok {
				continue
			}
			// Not after cutoff: a month ending exactly on it is wholly outside the window.
			if !month.AddDate(0, 1, 0).After(cutoff) {
				seen[month] = true
			}
		}
	}
	months := make([]time.Time, 0, len(seen))
	for month := range seen {
		months = append(months, month)
	}
	slices.SortFunc(months, time.Time.Compare)
	return months, nil
}

// leafPartitions lists the leaf tables of one partition tree. The default partition has no month and is no candidate.
func (r *retainer) leafPartitions(ctx context.Context, table string) ([]string, error) {
	var names []string
	err := r.db.ReadTx(ctx, r.scope, func(tx pg.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.relname
 FROM pg_partition_tree($1::regclass) t
 JOIN pg_class c ON c.oid = t.relid
 WHERE t.isleaf`, "observability."+table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			names = append(names, name)
		}
		return rows.Err()
	})
	return names, err
}

// monthOf returns the month a partition name stands for, if it names one of table's months.
func monthOf(table, name string) (time.Time, bool) {
	match := partitionName.FindStringSubmatch(name)
	if match == nil || match[1] != table {
		return time.Time{}, false
	}
	month, err := time.Parse("2006_01", match[2]+"_"+match[3])
	if err != nil {
		return time.Time{}, false
	}
	return month, true
}

// archiveMonth moves one month to the object store and drops it only after the archive is confirmed. If a manifest is
// already there, its objects are checked by size and only the drop remains; a manifest the run cannot trust is written
// again from the partition, which is still present until the drop.
func (r *retainer) archiveMonth(ctx context.Context, month time.Time) error {
	manifest, ready, err := r.readyArchive(ctx, month)
	if err != nil {
		return err
	}
	if ready {
		return r.dropMonth(ctx, month, manifest)
	}
	// A month with neither partition left and no manifest has nothing to do: an operator removed it, or an earlier
	// drop committed. Retrying forever would only fill the log.
	empty, err := r.bothPartitionsMissing(ctx, month)
	if err != nil {
		return err
	}
	if empty {
		r.logger.WarnContext(ctx, "observability: a month past the window has no partitions to archive", "month", monthKey(month))
		return nil
	}
	manifest, err = r.writeArchive(ctx, month)
	if errors.Is(err, errNothingToArchive) {
		r.logger.WarnContext(ctx, "observability: a month past the window has no partitions to archive", "month", monthKey(month))
		return nil
	}
	if err != nil {
		return err
	}
	ok, err := r.confirm(ctx, manifest)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("observability: the archive does not match what was uploaded")
	}
	for _, entry := range manifest.Tables {
		r.partitions.Add(ctx, 1, metric.WithAttributes(attribute.String("action", "archived"), attribute.String("table", entry.Table)))
	}
	return r.dropMonth(ctx, month, manifest)
}

// readyArchive reports whether a month's manifest can be trusted as complete: every present partition is named in it
// and every object it names is at the recorded size. A manifest that fails either check is rewritten from the partition.
func (r *retainer) readyArchive(ctx context.Context, month time.Time) (archiveManifest, bool, error) {
	manifest, found, err := r.loadManifest(ctx, month)
	if err != nil || !found {
		return manifest, false, err
	}
	covered, err := r.manifestCovers(ctx, manifest, month)
	if err != nil {
		return manifest, false, err
	}
	if !covered {
		r.logger.WarnContext(ctx, "observability: a partition is missing from the archive; writing it again", "month", monthKey(month))
		return manifest, false, nil
	}
	ok, err := r.confirm(ctx, manifest)
	if err != nil {
		return manifest, false, err
	}
	if !ok {
		r.logger.WarnContext(ctx, "observability: archived call log does not match its manifest; writing it again", "month", monthKey(month))
		return manifest, false, nil
	}
	return manifest, true, nil
}

// loadManifest reads the month's manifest. A missing object is not an error; a manifest that names another month or
// another format is, because the run must not drop a partition on the strength of an object it cannot understand.
func (r *retainer) loadManifest(ctx context.Context, month time.Time) (archiveManifest, bool, error) {
	object, err := r.store.Get(ctx, manifestKey(month))
	if errors.Is(err, objectstore.ErrNotFound) {
		return archiveManifest{}, false, nil
	}
	if err != nil {
		return archiveManifest{}, false, err
	}
	defer func() { _ = object.Body.Close() }() //nolint:errcheck // the body is read below
	raw, err := io.ReadAll(io.LimitReader(object.Body, 1<<20))
	if err != nil {
		return archiveManifest{}, false, err
	}
	var manifest archiveManifest
	if err := jsonx.Unmarshal(raw, &manifest); err != nil {
		return archiveManifest{}, false, nil // rewrite it from the partition below
	}
	if manifest.Version != archiveManifestVersion || manifest.Format != archiveFormat || manifest.Month != monthKey(month) || len(manifest.Tables) == 0 {
		return archiveManifest{}, false, nil
	}
	return manifest, true, nil
}

// manifestCovers reports whether every partition that still exists is named by the manifest. A manifest missing one
// (a backfill, or a partition created by hand after the run) cannot justify dropping the month.
func (r *retainer) manifestCovers(ctx context.Context, manifest archiveManifest, month time.Time) (bool, error) {
	listed := make(map[string]bool, len(manifest.Tables))
	for _, entry := range manifest.Tables {
		listed[entry.Table] = true
	}
	for _, table := range archiveTables {
		present, err := r.partitionExists(ctx, partitionNameOf(table, month))
		if err != nil {
			return false, err
		}
		if present && !listed[table] {
			return false, nil
		}
	}
	return true, nil
}

// confirm checks that every object the manifest names is present at the size it recorded. The manifest is written
// last, so finding it proves both uploads completed; the sizes catch a truncated retry or a damaged object.
func (r *retainer) confirm(ctx context.Context, manifest archiveManifest) (bool, error) {
	for _, table := range manifest.Tables {
		info, err := r.store.Head(ctx, table.Key)
		if errors.Is(err, objectstore.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Size != table.Bytes {
			return false, nil
		}
	}
	return true, nil
}

// writeArchive copies the month's partitions into gzip-compressed COPY output, uploads them and puts the manifest last.
// The file is on local disk because objectstore.Put needs a seekable body and retries from its start; a failed step
// leaves an object the next run overwrites, never a dropped partition.
func (r *retainer) writeArchive(ctx context.Context, month time.Time) (archiveManifest, error) {
	manifest := archiveManifest{
		Version:   archiveManifestVersion,
		Month:     monthKey(month),
		CreatedAt: time.Now().UTC(),
		Format:    archiveFormat,
	}
	written := 0
	for _, table := range archiveTables {
		part := partitionNameOf(table, month)
		present, err := r.partitionExists(ctx, part)
		if err != nil {
			return manifest, err
		}
		if !present {
			continue // an earlier run dropped this table's partition already
		}
		entry, err := r.copyPartition(ctx, table, part, month)
		if err != nil {
			return manifest, err
		}
		manifest.Tables = append(manifest.Tables, entry)
		written++
	}
	if written == 0 {
		return manifest, errNothingToArchive
	}
	raw, err := jsonx.MarshalCanonical(manifest)
	if err != nil {
		return manifest, err
	}
	// The manifest goes last: its presence is the only marker of a complete set, and a half-written set is rewritten
	// by the next run.
	if _, err := r.store.Put(ctx, objectstore.Upload{
		Key: manifestKey(month), Body: strings.NewReader(string(raw)),
		Size: int64(len(raw)), ContentType: "application/json",
	}); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func (r *retainer) bothPartitionsMissing(ctx context.Context, month time.Time) (bool, error) {
	for _, table := range archiveTables {
		present, err := r.partitionExists(ctx, partitionNameOf(table, month))
		if err != nil {
			return false, err
		}
		if present {
			return false, nil
		}
	}
	return true, nil
}

func (r *retainer) partitionExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.db.ReadTx(ctx, r.scope, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "observability."+name).Scan(&exists)
	})
	return exists, err
}

// copyPartition writes one partition's rows as PostgreSQL COPY CSV (a header line names the columns) into a gzip file
// and uploads it. The read runs as the job role, which owns the partition and, in the platform scope, sees every row.
func (r *retainer) copyPartition(ctx context.Context, table, part string, month time.Time) (archivedTable, error) {
	file, err := os.CreateTemp("", "axiom-retention-*.csv.gz")
	if err != nil {
		return archivedTable{}, err
	}
	path := file.Name()
	defer func() {
		_ = file.Close()    //nolint:errcheck // best effort; the upload already read it
		_ = os.Remove(path) //nolint:errcheck // best effort; a leaked temp file is not worth failing the run
	}()

	columns, rows, err := r.copyToFile(ctx, part, file)
	if err != nil {
		return archivedTable{}, err
	}
	size, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return archivedTable{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return archivedTable{}, err
	}
	key := objectKey(month, table+".csv.gz")
	info, err := r.store.Put(ctx, objectstore.Upload{Key: key, Body: file, Size: size, ContentType: "application/gzip"})
	if err != nil {
		return archivedTable{}, err
	}
	if info.Size != size {
		return archivedTable{}, fmt.Errorf("observability: %s uploaded %d bytes, wrote %d", part, info.Size, size)
	}
	digest, err := fileSum(file)
	if err != nil {
		return archivedTable{}, err
	}
	r.logger.InfoContext(ctx, "observability: archived a call-log partition", "partition", part, "object", key, "rows", rows, "bytes", size)
	return archivedTable{Table: table, Key: key, Rows: rows, Bytes: size, SHA256: digest, Columns: columns}, nil
}

// copyToFile runs COPY as the job role and writes the gzip stream to file, returning the column list and the row count.
// copyToFile returns only after the gzip trailer is written, so the file's size and digest cover the whole object.
func (r *retainer) copyToFile(ctx context.Context, part string, file *os.File) ([]archiveColumn, int64, error) {
	var columns []archiveColumn
	var rows int64
	gz := gzip.NewWriter(file)
	err := r.db.ReadTx(ctx, r.scope, func(tx pg.Tx) error {
		if err := tx.SetRole(ctx, retentionJobRole); err != nil {
			return err
		}
		var err error
		if columns, err = tableColumns(ctx, tx, "observability."+part); err != nil {
			return err
		}
		rows, err = tx.CopyTo(ctx, gz, "COPY (SELECT * FROM observability."+part+") TO STDOUT WITH (FORMAT csv, HEADER true)")
		return err
	})
	closeErr := gz.Close()
	if err != nil {
		return nil, 0, err
	}
	if closeErr != nil {
		return nil, 0, closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if columns == nil {
		return nil, 0, errors.New("observability: the partition has no columns")
	}
	return columns, rows, nil
}

// fileSum returns the SHA-256 of the file from its current position, which is its start after the upload.
func fileSum(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func tableColumns(ctx context.Context, tx pg.Tx, table string) ([]archiveColumn, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname, format_type(a.atttypid, a.atttypmod)
 FROM pg_attribute a
 WHERE a.attrelid = $1::regclass AND a.attnum > 0 AND NOT a.attisdropped
 ORDER BY a.attnum`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []archiveColumn
	for rows.Next() {
		var column archiveColumn
		if err := rows.Scan(&column.Name, &column.Type); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

// dropMonth drops the month's partitions in one transaction, attempts first (see archiveTables). A drop that cannot get
// its lock rolls back and the whole month is retried; the archive stays and the next run tries again.
func (r *retainer) dropMonth(ctx context.Context, month time.Time, manifest archiveManifest) error {
	err := r.db.Tx(ctx, r.scope, func(tx pg.Tx) error {
		if err := tx.SetRole(ctx, retentionJobRole); err != nil {
			return err
		}
		// A drop waits for every reader; five seconds is enough in steady state and lets the next run try again rather
		// than holding writes behind a long report.
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '5s'"); err != nil {
			return err
		}
		for _, table := range archiveTables {
			if !manifest.has(table) {
				continue // it appeared after the manifest; the next run archives and drops it
			}
			if _, err := tx.Exec(ctx, "DROP TABLE IF EXISTS observability."+partitionNameOf(table, month)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, entry := range manifest.Tables {
		r.partitions.Add(ctx, 1, metric.WithAttributes(attribute.String("action", "dropped"), attribute.String("table", entry.Table)))
	}
	r.logger.InfoContext(ctx, "observability: dropped archived call-log partitions", "month", monthKey(month))
	return nil
}

func partitionNameOf(table string, month time.Time) string {
	return table + "_" + month.Format("2006_01")
}
func monthKey(month time.Time) string    { return month.Format("2006-01") }
func manifestKey(month time.Time) string { return archivePrefix + monthKey(month) + "/manifest.json" }

func objectKey(month time.Time, name string) string {
	return archivePrefix + monthKey(month) + "/" + name
}

// archiveManifest is what is stored last for a month. It names the format, the month, the objects and their sizes and
// digests, so a reader can tell a complete set from a half-written one and restore the CSV with its column types.
func (m archiveManifest) has(table string) bool {
	for _, entry := range m.Tables {
		if entry.Table == table {
			return true
		}
	}
	return false
}

type archiveManifest struct {
	Version   int             `json:"version"`
	Month     string          `json:"month"`
	CreatedAt time.Time       `json:"created_at"`
	Format    string          `json:"format"`
	Tables    []archivedTable `json:"tables"`
}

type archivedTable struct {
	Table   string          `json:"table"`
	Key     string          `json:"key"`
	Rows    int64           `json:"rows"`
	Bytes   int64           `json:"bytes"`
	SHA256  string          `json:"sha256"`
	Columns []archiveColumn `json:"columns"`
}

type archiveColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
