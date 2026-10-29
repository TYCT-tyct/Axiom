package observability

import (
	"context"
	"errors"
	"fmt"

	"axiom/internal/observability/internal/store"
	"axiom/internal/platform/pg"
)

// MaxOwnExportRows bounds one complete metadata export.
const MaxOwnExportRows = 10_000

// ErrExportSize asks the caller to narrow the filters before starting a CSV response.
var ErrExportSize = errors.New("too many calls to export")

// CheckOwnExportSize must run in the same ReadTx as the export, before sending its first bytes.
func CheckOwnExportSize(ctx context.Context, tx pg.Tx, q OwnQuery) error {
	if err := planOwnPage(ctx, tx); err != nil {
		return err
	}
	at, id := cursorOf(q)
	count, err := store.New().CountOwnExportCalls(ctx, tx, store.CountOwnExportCallsParams{
		AccountID: q.Account.String(), AfterAt: at, AfterID: id, Since: q.Since, Until: q.Until,
		Model: q.Model, ClientApp: q.ClientApp, KeyID: uuidFilter(q.Key), WorkspaceID: uuidFilter(q.Workspace),
		Status: q.Status, RequestID: uuidFilter(q.RequestID), RowLimit: MaxOwnExportRows + 1,
	})
	if err != nil {
		return fmt.Errorf("check call export size: %w", err)
	}
	if count > MaxOwnExportRows {
		return ErrExportSize
	}
	return nil
}
