package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"uuid"

	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

func beginContentExport(ctx context.Context, db *pg.DB, id uuid.UUID, approval contentApproval, digest [32]byte,
	stats observability.ContentStats, cache gateway.CacheStats,
) error {
	scope, err := approval.scope()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO observability.content_exports
				(id, approval_digest, owner_key, owner2_key, recipient_key, since, until, site_id, account_id, destination,
				 format, status, content_count, content_bytes, cache_count, cache_bytes, started_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'started', $12, $13, $14, $15, $16)
			ON CONFLICT (approval_digest) DO NOTHING`,
			id, digest[:], approval.Owner, approval.Owner2, approval.Recipient, scope.Since, scope.Until,
			nullableUUID(scope.Site), nullableUUID(scope.Account), approval.Destination, approval.Format,
			stats.Count, stats.Bytes, cache.Count, cache.Bytes, now)
		if err != nil {
			return fmt.Errorf("content export: record the attempt: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errors.New("content export: this approval has already been used")
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor:  audit.System("observability.export"),
			Action: "content_export.start",
			Target: audit.Target{Module: "observability", Kind: "content_export", ID: id.String()},
			Detail: map[string]any{
				"approval_digest": hex.EncodeToString(digest[:]),
				"owner_key":       approval.Owner,
				"owner2_key":      approval.Owner2,
				"recipient_key":   approval.Recipient,
				"since":           approval.Since,
				"until":           approval.Until,
				"site_id":         approval.Site,
				"account_id":      approval.Account,
				"destination":     approval.Destination,
				"format":          approval.Format,
				"content_count":   stats.Count,
				"content_bytes":   stats.Bytes,
				"cache_count":     cache.Count,
				"cache_bytes":     cache.Bytes,
			},
		})
	})
}

func finishContentExport(ctx context.Context, db *pg.DB, id uuid.UUID, outputBytes int64, sum [32]byte) error {
	now := time.Now().UTC()
	return db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE observability.content_exports
			SET status = 'completed', finished_at = $2, output_bytes = $3, output_sha256 = $4
			WHERE id = $1 AND status = 'started'`, id, now, outputBytes, sum[:])
		if err != nil {
			return fmt.Errorf("content export: record completion: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errors.New("content export: the attempt is no longer started")
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor:  audit.System("observability.export"),
			Action: "content_export.complete",
			Target: audit.Target{Module: "observability", Kind: "content_export", ID: id.String()},
			Detail: map[string]any{
				"output_bytes":  outputBytes,
				"output_sha256": hex.EncodeToString(sum[:]),
			},
		})
	})
}

// failContentExport records a failed attempt with a new short deadline: the export's context may already be canceled.
// It is best effort, because the start row and its audit entry are the record that matters; the caller returns the
// original error either way.
func failContentExport(ctx context.Context, db *pg.DB, id uuid.UUID, kind string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = db.Tx(ctx, pg.PlatformScope(), func(tx pg.Tx) error { //nolint:errcheck // best effort; the start row is the record
		tag, err := tx.Exec(ctx, `
			UPDATE observability.content_exports
			SET status = 'failed', finished_at = $2, error_kind = $3
			WHERE id = $1 AND status = 'started'`, id, time.Now().UTC(), kind)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("not started")
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor:  audit.System("observability.export"),
			Action: "content_export.fail",
			Target: audit.Target{Module: "observability", Kind: "content_export", ID: id.String()},
			Detail: map[string]any{"error_kind": kind},
		})
	})
}

// checkContentDestination refuses an existing path before an approval is consumed. The final link repeats the check
// atomically, so a race cannot overwrite an owner's file.
func checkContentDestination(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("destination directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination directory %s is not a directory", dir)
	}
	return nil
}

// createContentExportTemp makes a private file in the destination's directory, so publishing is a same-filesystem link
// rather than a copy.
func createContentExportTemp(destination string) (*os.File, error) {
	dir := filepath.Dir(destination)
	file, err := os.CreateTemp(dir, "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		discard(file)
		removeTemp(file.Name())
		return nil, err
	}
	return file, nil
}

// publishNoReplace links temp to destination and removes temp. A link fails if destination exists, which is the
// no-overwrite guarantee a rename cannot give portably; both names are in one directory, so they share a filesystem.
func publishNoReplace(temp, destination string) error {
	if err := os.Link(temp, destination); err != nil {
		return fmt.Errorf("publish %s: %w", destination, err)
	}
	if err := os.Remove(temp); err != nil {
		return fmt.Errorf("published %s but could not remove %s: %w", destination, temp, err)
	}
	dir, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(destination), err)
	}
	defer dir.Close() //nolint:errcheck // read-only directory handle
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", filepath.Dir(destination), err)
	}
	return nil
}

// discard closes a file whose close error cannot change the outcome; removeTemp is the same for a temporary file.
func discard(file *os.File) {
	_ = file.Close() //nolint:errcheck // best effort after the real result is known
}

func removeTemp(path string) {
	_ = os.Remove(path) //nolint:errcheck // best effort; a leaked temp file is reported by the next run
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) // #nosec G304 -- the operator names the path
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		discard(file)
		removeTemp(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		discard(file)
		removeTemp(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		removeTemp(path)
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func nullableUUID(id uuid.UUID) any {
	if id == (uuid.UUID{}) {
		return nil
	}
	return id
}
