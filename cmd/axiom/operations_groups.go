package main

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/operationsapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// SupplierGroupSelect selects or unselects one of a supplier's groups, at once: a routine write of the agent bringing a
// supplier in (docs/domains/operations.md「运营接口」).
func (o operationsAPI) SupplierGroupSelect(ctx context.Context, r operationsapi.SupplierGroupSelectRequestObject) (operationsapi.SupplierGroupSelectResponseObject, error) {
	b := r.Body
	p := operations.Proposal{
		Tool: "supplier_group_select", Target: operations.Target{Kind: "supplier_group", ID: r.GroupId},
		Params: map[string]any{"supplier_id": r.SupplierId, "selected": b.Selected}, Reason: b.Reason,
	}
	op, replayed, err := o.runRoutine(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, p, b.DryRun)
	if err != nil {
		return nil, err
	}
	return operationsapi.SupplierGroupSelect200JSONResponse{Body: operationBody(op), Headers: operationsapi.SupplierGroupSelect200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// SupplierModelMap maps a name one of a supplier's groups lists as waiting for a model to a catalog model for that
// group, at once. The agent maps only names that wait: any other alias moves traffic that already flows, which stays
// with the owner in the admin.
func (o operationsAPI) SupplierModelMap(ctx context.Context, r operationsapi.SupplierModelMapRequestObject) (operationsapi.SupplierModelMapResponseObject, error) {
	b := r.Body
	p := operations.Proposal{
		Tool: "supplier_model_map", Target: operations.Target{Kind: "supplier_group", ID: r.GroupId},
		Params: map[string]any{"supplier_id": r.SupplierId, "upstream_name": b.UpstreamName, "model_id": b.ModelId}, Reason: b.Reason,
	}
	op, replayed, err := o.runRoutine(ctx, r.Params.IdempotencyKey, r.Params.XAxiomRunID, p, b.DryRun)
	if err != nil {
		return nil, err
	}
	return operationsapi.SupplierModelMap200JSONResponse{Body: operationBody(op), Headers: operationsapi.SupplierModelMap200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// groupIDsOf reads the supplier and group of a path; a group that is no ID is a group the supplier lacks.
func groupIDsOf(supplierID, groupID string) (supplier, group uuid.UUID, err error) {
	if supplier, err = supplierIDOf(supplierID); err != nil {
		return uuid.UUID{}, uuid.UUID{}, err
	}
	if group, err = supplierIDOf(groupID); err != nil {
		return uuid.UUID{}, uuid.UUID{}, suppliers.ErrNoGroup
	}
	return supplier, group, nil
}

// runGroupSelect selects or unselects the group of op as the agent's, and answers the group as it now is.
func (a adminAPI) runGroupSelect(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	var params struct {
		Supplier string `json:"supplier_id"`
		Selected bool   `json:"selected"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	supplier, group, err := groupIDsOf(params.Supplier, op.Target.ID)
	if err != nil {
		return nil, err
	}
	g, err := a.suppliers.SelectGroup(ctx, tx, audit.OperationsAgent(op.Credential), supplier, group, params.Selected, op.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"supplier_id": params.Supplier, "selected": g.Selected, "codename": g.Codename}, nil
}

// runModelMap makes the alias of op for its group as the agent's, and answers it. The model is checked here, when the
// operation runs, so a retry finds the alias whatever happened to the model since.
func (a adminAPI) runModelMap(ctx context.Context, tx pg.Tx, op operations.Operation) (map[string]any, error) {
	var params struct {
		Supplier string `json:"supplier_id"`
		Name     string `json:"upstream_name"`
		Model    string `json:"model_id"`
	}
	if err := jsonx.Unmarshal(op.Params, &params); err != nil {
		return nil, fmt.Errorf("the parameters of operation %s: %w", op.ID, err)
	}
	supplier, group, err := groupIDsOf(params.Supplier, op.Target.ID)
	if err != nil {
		return nil, err
	}
	model, err := idIn("/model_id", params.Model)
	if err != nil {
		return nil, err
	}
	if err := mappableModel(ctx, tx, model); err != nil {
		return nil, err
	}
	m, err := a.suppliers.MapPending(ctx, tx, audit.OperationsAgent(op.Credential), supplier, group, params.Name, model, op.Reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"supplier_id": params.Supplier, "alias_id": m.ID.String(), "upstream_name": m.UpstreamName, "model_id": params.Model, "revision": m.Revision}, nil
}
