package main

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/gateway"
	"axiom/internal/observability"
	"axiom/internal/platform/pg"
)

//nolint:cyclop,gocognit // the start, completion, failure and replay are one audit journey.
func TestContentExportRowsAndAudit(t *testing.T) {
	c := newCommandLine(t)
	now := time.Now().UTC()
	approval := contentApproval{
		Action:      contentExportAction,
		Version:     contentApprovalVersion,
		Owner:       strings.Repeat("a", 64),
		Owner2:      strings.Repeat("c", 64),
		Recipient:   strings.Repeat("b", 64),
		Since:       now.Add(-time.Hour).Format(time.RFC3339Nano),
		Until:       now.Format(time.RFC3339Nano),
		Site:        uuid.NewV7().String(),
		Destination: "/tmp/content-export-test.enc",
		Format:      contentExportFormat,
		Nonce:       "bm5ubm5ubm5ubm5ubm5ubg==",
	}
	digest := sha256.Sum256([]byte("the owner's approval"))
	stats := observability.ContentStats{Count: 2, Bytes: 123}
	id := uuid.NewV7()
	if err := beginContentExport(t.Context(), c.db, id, approval, digest, stats, gateway.CacheStats{}); err != nil {
		t.Fatal(err)
	}
	var status string
	var count, bytes int64
	if err := c.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT status, content_count, content_bytes
			FROM observability.content_exports WHERE id = $1`, id).Scan(&status, &count, &bytes)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "started" || count != 2 || bytes != 123 {
		t.Fatalf("the attempt is %s, %d, %d", status, count, bytes)
	}
	if got := auditCount(t, c, "content_export.start", id); got != 1 {
		t.Fatalf("start audit entries: %d", got)
	}

	replay := uuid.NewV7()
	if err := beginContentExport(t.Context(), c.db, replay, approval, digest, stats, gateway.CacheStats{}); err == nil {
		t.Fatal("the same approval was used twice")
	}
	if got := auditCount(t, c, "content_export.start", replay); got != 0 {
		t.Fatalf("replay audit entries: %d", got)
	}

	output := sha256.Sum256([]byte("the encrypted file"))
	if err := finishContentExport(t.Context(), c.db, id, 456, output); err != nil {
		t.Fatal(err)
	}
	var sum []byte
	if err := c.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT status, output_bytes, output_sha256
			FROM observability.content_exports WHERE id = $1`, id).Scan(&status, &count, &sum)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || count != 456 || string(sum) != string(output[:]) {
		t.Fatalf("the completed attempt is %s, %d, %x", status, count, sum)
	}
	if got := auditCount(t, c, "content_export.complete", id); got != 1 {
		t.Fatalf("complete audit entries: %d", got)
	}

	failed := uuid.NewV7()
	other := sha256.Sum256([]byte("another approval"))
	if err := beginContentExport(t.Context(), c.db, failed, approval, other, stats, gateway.CacheStats{}); err != nil {
		t.Fatal(err)
	}
	failContentExport(t.Context(), c.db, failed, "write")
	var errorKind string
	if err := c.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT status, error_kind
			FROM observability.content_exports WHERE id = $1`, failed).Scan(&status, &errorKind)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errorKind != "write" {
		t.Fatalf("the failed attempt is %s, %q", status, errorKind)
	}
	if got := auditCount(t, c, "content_export.fail", failed); got != 1 {
		t.Fatalf("fail audit entries: %d", got)
	}
}

func auditCount(t *testing.T, c commandLine, action string, id uuid.UUID) int {
	t.Helper()
	var count int
	if err := c.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM platform.audit_log
			WHERE action = $1 AND target_id = $2`, action, id.String()).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}
