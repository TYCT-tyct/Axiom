package main

import (
	"context"
	"time"

	"axiom/internal/operations"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
)

// AuditDigestsList lists the owner's daily digests newest first; the cursor is the last day of the page before.
func (o operationsAPI) AuditDigestsList(ctx context.Context, r operationsapi.AuditDigestsListRequestObject) (operationsapi.AuditDigestsListResponseObject, error) {
	var before *time.Time
	if r.Params.Cursor != nil {
		day, err := time.Parse(time.DateOnly, *r.Params.Cursor)
		if err != nil {
			return nil, invalidField("cursor", "not a cursor this API gave out")
		}
		before = &day
	}
	var digests []operations.AuditDigest
	var more bool
	if err := o.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		digests, more, err = operations.AuditDigests(ctx, tx, before, pageSize(r.Params.Limit))
		return err
	}); err != nil {
		return nil, err
	}
	list, err := adminJSON[operationsapi.AuditDigestList](map[string]any{"data": digests})
	if err != nil {
		return nil, err
	}
	if more {
		list.NextCursor = new(digests[len(digests)-1].Day)
	}
	return operationsapi.AuditDigestsList200JSONResponse(list), nil
}
