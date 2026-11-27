package main

import (
	"context"
	"errors"
	"net/http"

	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// Channel types (docs/domains/suppliers-direct.md「渠道类型」): the registry's descriptions, and one operation that
// connects a supplier of any direct or cloud type by what its description asks for.

// ListSupplierTypes lists the types the node carries with the models the catalog can sell through each.
func (a adminAPI) ListSupplierTypes(_ context.Context, _ adminapi.ListSupplierTypesRequestObject) (adminapi.ListSupplierTypesResponseObject, error) {
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	types := a.suppliers.Types(snap)
	out := adminapi.ListSupplierTypes200JSONResponse{Data: make([]adminapi.SupplierType, len(types))}
	for i, t := range types {
		out.Data[i] = supplierTypeBody(t)
	}
	return out, nil
}

// supplierTypeBody is the contract's view of a type.
func supplierTypeBody(t suppliers.TypeView) adminapi.SupplierType {
	out := adminapi.SupplierType{
		Kind: string(t.Kind), Mode: adminapi.SupplierMode(t.Mode), Name: t.Name, Group: adminapi.SupplierTypeGroup(t.Group),
		Entries: make([]adminapi.SupplierTypeEntry, len(t.Entries)), Families: make([]adminapi.ApiFormat, len(t.Families)),
		Forms: make([]adminapi.SupplierTypeForm, len(t.Forms)), Fields: fieldsBody(t.Fields),
		ListsModels: t.Lists, ReadsBalance: t.Balance,
	}
	if t.Vendor != "" {
		out.Vendor = &t.Vendor
	}
	if len(t.Serves) > 0 {
		out.Serves = &t.Serves
	}
	for i, e := range t.Entries {
		out.Entries[i] = adminapi.SupplierTypeEntry{Id: e.ID, Origin: e.Origin, Currency: e.Currency}
		if e.Account != "" {
			out.Entries[i].Account = &e.Account
		}
		if t.Models != nil {
			out.Entries[i].Models = &t.Models[i]
		}
	}
	for i, f := range t.Families {
		out.Families[i] = adminapi.ApiFormat(f)
	}
	for i, f := range t.Forms {
		out.Forms[i] = adminapi.SupplierTypeForm{Name: f.Name, Fields: fieldsBody(f.Fields)}
	}
	return out
}

func fieldsBody(fields []suppliers.TypeField) []adminapi.SupplierTypeField {
	out := make([]adminapi.SupplierTypeField, len(fields))
	for i, f := range fields {
		out[i] = adminapi.SupplierTypeField{Name: f.Name, Kind: adminapi.SupplierTypeFieldKind(suppliers.FieldKind(f)), Required: f.Required}
		if f.Pattern != "" {
			out[i].Pattern = &f.Pattern
		}
		if f.Max > 0 {
			out[i].MaxLength = &f.Max
		}
	}
	return out
}

// CreateTypedSupplier connects a supplier of a direct or cloud type: a direct API's keys, or a cloud platform's
// credential in its regions. A relay's type, or a kind the node does not carry, is 404: a relay is detected and logged
// in to (createSupplier).
func (a adminAPI) CreateTypedSupplier(ctx context.Context, request adminapi.CreateTypedSupplierRequestObject) (adminapi.CreateTypedSupplierResponseObject, error) {
	t, ok := a.suppliers.TypeOf(request.Kind)
	if !ok || t.Mode == "relay" {
		return nil, &apiError{status: http.StatusNotFound, code: codeNotFound, message: "no direct or cloud channel type of that kind"}
	}
	b := request.Body
	var (
		body   adminapi.SupplierConnected
		replay bool
		err    error
	)
	if t.Mode == "direct" {
		body, replay, err = a.createDirect(ctx, request.Params.IdempotencyKey, request.Kind, b)
	} else {
		body, replay, err = a.createCloud(ctx, request.Params.IdempotencyKey, request.Kind, b)
	}
	if err != nil {
		return nil, err
	}
	if body.Supplier == nil {
		return adminapi.CreateTypedSupplier200JSONResponse{Body: body, Headers: adminapi.CreateTypedSupplier200ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}, nil
	}
	return adminapi.CreateTypedSupplier201JSONResponse{Body: body, Headers: adminapi.CreateTypedSupplier201ResponseHeaders{IdempotencyReplayed: replayedHeader(replay)}}, nil
}

// SetSupplierCostRatio sets a direct or cloud supplier's line ratio and reprices the line at it; the node takes the new
// plans into routing at once.
func (a adminAPI) SetSupplierCostRatio(ctx context.Context, request adminapi.SetSupplierCostRatioRequestObject) (adminapi.SetSupplierCostRatioResponseObject, error) {
	id, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	snap, err := a.catalog.Current()
	if err != nil {
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	b := request.Body
	supplier, replayed, err := idempotently(ctx, a.db, "setSupplierCostRatio", request.Params.IdempotencyKey, []any{request.SupplierId, b},
		func(tx pg.Tx) (adminapi.Supplier, error) {
			changed, err := a.suppliers.SetCostRatio(ctx, tx, actor(ctx), snap, id, b.Ratio, b.Reason)
			return supplierBody(changed), costRatioProblem(err, "/ratio")
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.SetSupplierCostRatio200JSONResponse{Body: supplier, Headers: adminapi.SetSupplierCostRatio200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// costRatioProblem maps the refusals of a line ratio, the member at pointer, onto the contract; any other error as it
// is.
func costRatioProblem(err error, pointer string) error {
	switch {
	case errors.Is(err, suppliers.ErrCostRatio):
		return unprocessableFields(pointer, "above 0 and at most 10, with at most 6 decimals")
	case errors.Is(err, suppliers.ErrNotPasted):
		return &apiError{status: http.StatusConflict, code: codeConflict, message: "a relay's ratios are read by its sync"}
	}
	return err
}

// typedEntry reads the members every typed request places the supplier by: the entry, or the address and the currency.
func typedEntry(b *adminapi.TypedSupplierCreate) (entry, origin string, currency platform.Currency, err error) {
	entry = textValue(b.Entry)
	if b.Origin != nil {
		if origin, err = suppliers.NormalizeOrigin(*b.Origin); err != nil {
			return "", "", platform.Currency{}, supplierProblem(err)
		}
	}
	if b.SettlementCurrency != nil {
		if currency, err = platform.ParseCurrency(*b.SettlementCurrency); err != nil {
			return "", "", platform.Currency{}, invalidField("/settlement_currency", "not a currency code")
		}
	}
	return entry, origin, currency, nil
}

// fieldsOf is a request's fields map; absent is none.
func fieldsOf(m *map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	return *m
}

// typedProblem maps the refusals every type shares onto the contract; nil for any other.
func typedProblem(err error) error {
	if fieldErr, ok := errors.AsType[*suppliers.FieldError](err); ok {
		return invalidField("/fields/"+fieldErr.Field, fieldErr.Reason)
	}
	switch {
	case errors.Is(err, suppliers.ErrNoType):
		return &apiError{status: http.StatusNotFound, code: codeNotFound, message: "no direct or cloud channel type of that kind"}
	case errors.Is(err, suppliers.ErrTypeOrigin):
		return invalidField("/entry", "a type with price entries is connected at one of them, without an origin or a settlement currency; any other needs an origin")
	case errors.Is(err, suppliers.ErrDirectName):
		return invalidField("/name", "1 to 200 characters")
	case errors.Is(err, suppliers.ErrCostRatio):
		return costRatioProblem(err, "/cost_ratio")
	case errors.Is(err, suppliers.ErrUnknownEgress):
		return unprocessableFields("/egress_id", "no egress proxy has that id")
	}
	return nil
}
