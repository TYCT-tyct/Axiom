package operations

import (
	"testing"
	"time"

	"axiom/internal/platform/pg"
)

func TestExpiryJob(t *testing.T) {
	var args expiryArgs
	if got := args.Kind(); got != "operations.approval_expiry" {
		t.Errorf("Kind() = %q", got)
	}
	opts := args.InsertOpts()
	if opts.Queue != pg.QueueArchive || !opts.UniqueOpts.ByArgs || len(opts.UniqueOpts.ByState) == 0 {
		t.Errorf("InsertOpts() = %+v", opts)
	}
	if expiryBatch != 100 || approvalTTL != 30*time.Minute {
		t.Errorf("expiryBatch = %d, approvalTTL = %v", expiryBatch, approvalTTL)
	}
}
