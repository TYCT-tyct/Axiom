package main

import (
	"context"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// MapSupplierModel maps a supplier model to a catalog model, optionally declaring call formats the adapter cannot report.
func (a adminAPI) MapSupplierModel(ctx context.Context, request adminapi.MapSupplierModelRequestObject) (adminapi.MapSupplierModelResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	model, err := idIn("/model_id", b.ModelId)
	if err != nil {
		return nil, err
	}
	var group *uuid.UUID
	if b.GroupId != nil {
		id, err := idIn("/group_id", *b.GroupId)
		if err != nil {
			return nil, err
		}
		group = &id
	}
	mapped, replayed, err := idempotently(ctx, a.db, "mapSupplierModel", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.ModelMapping, error) {
			// Checked by the request that makes the change only: a model retired since is no reason to refuse its retry.
			if err := mappableModel(ctx, tx, model); err != nil {
				return adminapi.ModelMapping{}, err
			}
			m, err := a.suppliers.MapModel(ctx, tx, actor(ctx), supplier, group, b.UpstreamName, model, declaredFormats(b.Formats), b.Rev, b.Reason)
			return mappingBody(m), bodyGroup(err)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.MapSupplierModel201JSONResponse{Body: mapped, Headers: adminapi.MapSupplierModel201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// declaredFormats converts the API's optional list: nil leaves an existing declaration alone, and an empty list is sent
// as an empty declaration so the store clears it.
func declaredFormats(in *[]adminapi.ModelFormat) *[]string {
	if in == nil {
		return nil
	}
	out := make([]string, len(*in))
	for i, f := range *in {
		out[i] = string(f)
	}
	return &out
}

// mappingBody renders a mapping for the API, including the declaration only when there is one.
func mappingBody(m suppliers.Mapping) adminapi.ModelMapping {
	body := adminapi.ModelMapping{Id: m.ID.String(), SupplierId: m.Supplier.String(), UpstreamName: m.UpstreamName, ModelId: m.Model.String(), Revision: &m.Revision}
	if len(m.Formats) > 0 {
		formats := make([]adminapi.ModelFormat, len(m.Formats))
		for i, f := range m.Formats {
			formats[i] = adminapi.ModelFormat(f)
		}
		body.Formats = &formats
	}
	if m.Group != nil {
		id := m.Group.String()
		body.GroupId = &id
	}
	return body
}
