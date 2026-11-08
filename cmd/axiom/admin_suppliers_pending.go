package main

import (
	"context"
	"encoding/base64"
	"time"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// The queue of upstream names waiting to be mapped (internal/suppliers/README.md「管理接口」).

// pendingCursor is where a page of the queue of names to map ended, in the cursor's own form.
type pendingCursor struct {
	Seen  time.Time `json:"s"`
	Group uuid.UUID `json:"g"`
	Name  string    `json:"n"`
}

func pendingAfter(cursor *string) (*suppliers.PendingMapping, error) {
	if cursor == nil {
		return nil, nil //nolint:nilnil // no cursor is the first page
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*cursor)
	var c pendingCursor
	if err != nil || jsonx.Unmarshal(decoded, &c) != nil || c.Name == "" {
		return nil, invalidField("cursor", "not a cursor this API gave out")
	}
	return &suppliers.PendingMapping{FirstSeen: c.Seen, Group: c.Group, UpstreamName: c.Name}, nil
}

// ListPendingMappings lists the upstream names no model is mapped to, oldest first, a page at a time.
func (a adminAPI) ListPendingMappings(ctx context.Context, request adminapi.ListPendingMappingsRequestObject) (adminapi.ListPendingMappingsResponseObject, error) {
	return a.listPendingMappings(ctx, request, suppliers.EveryOwner)
}

// listPendingMappings is ListPendingMappings of the owners' suppliers.
func (a adminAPI) listPendingMappings(ctx context.Context, request adminapi.ListPendingMappingsRequestObject, owners suppliers.Owners) (adminapi.ListPendingMappingsResponseObject, error) {
	p := request.Params
	q := suppliers.PendingQuery{Owners: owners, PageSize: pageSize(p.Limit)}
	var err error
	if p.SupplierId != nil {
		if q.Supplier, err = idIn("supplier_id", *p.SupplierId); err != nil {
			return nil, err
		}
	}
	if q.After, err = pendingAfter(p.Cursor); err != nil {
		return nil, err
	}
	var names []suppliers.PendingMapping
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		names, more, err = a.suppliers.PendingMappings(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.PendingMappingList{Data: make([]adminapi.PendingMapping, len(names))}
	for i, n := range names {
		list.Data[i] = adminapi.PendingMapping{
			SupplierId: n.Supplier.String(), GroupId: n.Group.String(), GroupCodename: text(n.GroupCodename), UpstreamName: n.UpstreamName,
			Formats: modelFormats(n.Formats),
		}
	}
	if more {
		last := names[len(names)-1]
		encoded, err := jsonx.MarshalCanonical(pendingCursor{last.FirstSeen, last.Group, last.UpstreamName})
		if err != nil {
			return nil, err
		}
		next := base64.RawURLEncoding.EncodeToString(encoded)
		list.NextCursor = &next
	}
	return adminapi.ListPendingMappings200JSONResponse(list), nil
}
