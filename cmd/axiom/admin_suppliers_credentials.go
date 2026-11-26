package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"sync"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// The credentials of the platform's suppliers as a person manages them: listed with this node's load, checked, switched
// and erased (docs/domains/suppliers.md「管理操作」). A change that takes a credential out of routing, or puts it back,
// rebuilds this node's routing before it answers (leaveRouting).

// keysPerTestRequest is how many credentials a check of every key tests at once.
const keysPerTestRequest = 8

// ListSupplierCredentials lists the platform's suppliers' credentials, newest first, with this node's load on each.
func (a adminAPI) ListSupplierCredentials(ctx context.Context, request adminapi.ListSupplierCredentialsRequestObject) (adminapi.ListSupplierCredentialsResponseObject, error) {
	p := request.Params
	q := suppliers.CredentialQuery{PageSize: pageSize(p.Limit)}
	var err error
	if p.SupplierId != nil {
		if q.Supplier, err = idIn("supplier_id", *p.SupplierId); err != nil {
			return nil, err
		}
	}
	if p.Status != nil {
		q.Status = string(*p.Status)
	}
	if p.Mode != nil {
		q.Mode = string(*p.Mode)
	}
	if q.After, err = supplierCursor(p.Cursor); err != nil {
		return nil, err
	}
	var page []suppliers.Credential
	var more bool
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		page, more, err = a.suppliers.Credentials(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.SupplierCredentialList{Data: make([]adminapi.SupplierCredential, len(page))}
	for i, c := range page {
		list.Data[i] = a.credentialBody(c)
	}
	if more {
		list.NextCursor = new(page[len(page)-1].ID.String())
	}
	return adminapi.ListSupplierCredentials200JSONResponse(list), nil
}

// TestSupplierCredential checks one credential with its own key: 10 a minute per session, shared with
// testSupplierCredentials.
func (a adminAPI) TestSupplierCredential(ctx context.Context, request adminapi.TestSupplierCredentialRequestObject) (adminapi.TestSupplierCredentialResponseObject, error) {
	if err := a.credentialTests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	credential, err := supplierIDOf(request.CredentialId)
	if err != nil {
		return nil, suppliers.ErrNoCredential
	}
	claim, err := idempotencyClaim(ctx, "testSupplierCredential", request.Params.IdempotencyKey, []any{request.SupplierId, request.CredentialId, request.Body.Reason})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	var check suppliers.KeyCheck
	body, replayed, err := probed(ctx, a, claim, func(ctx context.Context) (err error) {
		check, err = a.suppliers.CheckCredential(ctx, a.reach(ctx), supplier, credential)
		return err
	}, func(tx pg.Tx) (adminapi.CredentialCheck, error) {
		if _, err := a.suppliers.RecordCheck(ctx, tx, actor(ctx), supplier, check, request.Body.Reason); err != nil {
			return adminapi.CredentialCheck{}, err
		}
		return keyCheckBody(check, false), nil
	})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.TestSupplierCredential201JSONResponse{Body: body, Headers: adminapi.TestSupplierCredential201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// TestSupplierCredentials checks every credential that is not disabled, of one supplier or of all, at most
// suppliers.MaxCredentialTests, a few at a time; it counts as one test of the session's ten a minute.
func (a adminAPI) TestSupplierCredentials(ctx context.Context, request adminapi.TestSupplierCredentialsRequestObject) (adminapi.TestSupplierCredentialsResponseObject, error) {
	if err := a.credentialTests.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	b := request.Body
	var supplier uuid.UUID
	if b.SupplierId != nil {
		var err error
		if supplier, err = idIn("/supplier_id", *b.SupplierId); err != nil {
			return nil, err
		}
	}
	claim, err := idempotencyClaim(ctx, "testSupplierCredentials", request.Params.IdempotencyKey, []any{b.SupplierId, b.Reason})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	var checks []supplierCheck
	skipped := 0
	body, replayed, err := probed(ctx, a, claim, func(ctx context.Context) (err error) {
		checks, skipped, err = a.checkAll(ctx, supplier)
		return err
	}, func(tx pg.Tx) (adminapi.CredentialCheckList, error) {
		list := adminapi.CredentialCheckList{Data: make([]adminapi.CredentialCheck, len(checks)), Skipped: &skipped}
		for i, c := range checks {
			if _, err := a.suppliers.RecordCheck(ctx, tx, actor(ctx), c.Supplier, c.KeyCheck, b.Reason); err != nil {
				return adminapi.CredentialCheckList{}, err
			}
			list.Data[i] = keyCheckBody(c.KeyCheck, false)
		}
		return list, nil
	})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.TestSupplierCredentials201JSONResponse{Body: body, Headers: adminapi.TestSupplierCredentials201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// supplierCheck is a check of a credential of a supplier.
type supplierCheck struct {
	Supplier uuid.UUID
	suppliers.KeyCheck
}

// checkAll checks the credentials CredentialsToTest gives, keysPerTestRequest at a time, and answers them by supplier.
// One that a person disabled or erased since it was listed is skipped, as the disabled are.
func (a adminAPI) checkAll(ctx context.Context, supplier uuid.UUID) ([]supplierCheck, int, error) {
	var refs []suppliers.CredentialRef
	var disabled int
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		refs, disabled, err = a.suppliers.CredentialsToTest(ctx, tx, supplier)
		return err
	}); err != nil {
		return nil, 0, err
	}
	checks := make([]supplierCheck, len(refs))
	errs := make([]error, len(refs))
	slots := make(chan struct{}, keysPerTestRequest)
	var wg sync.WaitGroup
	for i, r := range refs {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			check, err := a.suppliers.CheckCredential(ctx, a.reach(ctx), r.Supplier, r.ID)
			checks[i], errs[i] = supplierCheck{Supplier: r.Supplier, KeyCheck: check}, err
		})
	}
	wg.Wait()
	out := checks[:0]
	for i, err := range errs {
		switch {
		case errors.Is(err, suppliers.ErrStatusLocked):
			disabled++
		case err != nil:
			return nil, 0, err
		default:
			out = append(out, checks[i])
		}
	}
	// Recorded in the order of their suppliers, so that two of these requests take the suppliers' rows alike.
	slices.SortStableFunc(out, func(a, b supplierCheck) int { return a.Supplier.Compare(b.Supplier) })
	return out, disabled, nil
}

// SetSupplierCredentialEnabled is a person's switch of one credential; routing on this node follows it at once.
func (a adminAPI) SetSupplierCredentialEnabled(ctx context.Context, request adminapi.SetSupplierCredentialEnabledRequestObject) (adminapi.SetSupplierCredentialEnabledResponseObject, error) {
	supplier, credential, err := credentialIDsOf(request.SupplierId, request.CredentialId)
	if err != nil {
		return nil, err
	}
	b := request.Body
	changed, replayed, err := idempotently(ctx, a.db, "setSupplierCredentialEnabled", request.Params.IdempotencyKey, []any{request.SupplierId, request.CredentialId, b},
		func(tx pg.Tx) (adminapi.SupplierCredential, error) {
			c, err := a.suppliers.SetCredentialEnabled(ctx, tx, actor(ctx), supplier, credential, b.Enabled, b.Reason)
			return a.credentialBody(c), err
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.SetSupplierCredentialEnabled200JSONResponse{Body: changed, Headers: adminapi.SetSupplierCredentialEnabled200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RemoveSupplierCredential erases one credential; routing on this node stops picking it at once.
func (a adminAPI) RemoveSupplierCredential(ctx context.Context, request adminapi.RemoveSupplierCredentialRequestObject) (adminapi.RemoveSupplierCredentialResponseObject, error) {
	supplier, credential, err := credentialIDsOf(request.SupplierId, request.CredentialId)
	if err != nil {
		return nil, err
	}
	erased, replayed, err := idempotently(ctx, a.db, "removeSupplierCredential", request.Params.IdempotencyKey, []any{request.SupplierId, request.CredentialId, request.Body},
		func(tx pg.Tx) (adminapi.SupplierCredential, error) {
			c, err := a.suppliers.RemoveCredential(ctx, tx, actor(ctx), supplier, credential, request.Body.Reason)
			return a.credentialBody(c), err
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.RemoveSupplierCredential200JSONResponse{Body: erased, Headers: adminapi.RemoveSupplierCredential200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// RemoveInvalidSupplierCredentials erases the named credentials that are still refused, and keeps the rest.
func (a adminAPI) RemoveInvalidSupplierCredentials(ctx context.Context, request adminapi.RemoveInvalidSupplierCredentialsRequestObject) (adminapi.RemoveInvalidSupplierCredentialsResponseObject, error) {
	b := request.Body
	ids := make([]uuid.UUID, len(b.CredentialIds))
	for i, s := range b.CredentialIds {
		id, err := idIn("/credential_ids/"+strconv.Itoa(i), s)
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	result, replayed, err := idempotently(ctx, a.db, "removeInvalidSupplierCredentials", request.Params.IdempotencyKey, []any{b},
		func(tx pg.Tx) (adminapi.InvalidCredentialsRemoved, error) {
			removed, kept, err := a.suppliers.RemoveInvalid(ctx, tx, actor(ctx), ids, b.Reason)
			return adminapi.InvalidCredentialsRemoved{Removed: idTexts(removed), Kept: idTexts(kept)}, err
		})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed || len(result.Removed) == 0)
	return adminapi.RemoveInvalidSupplierCredentials200JSONResponse{Body: result, Headers: adminapi.RemoveInvalidSupplierCredentials200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// idTexts are IDs as the contract writes them; none is an empty list, not null.
func idTexts(ids []uuid.UUID) []adminapi.Uuid {
	out := make([]adminapi.Uuid, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// credentialIDsOf reads a credential's path: an ID that is none is no supplier or no credential.
func credentialIDsOf(supplierID, credentialID string) (uuid.UUID, uuid.UUID, error) {
	supplier, err := supplierIDOf(supplierID)
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, err
	}
	credential, err := supplierIDOf(credentialID)
	if err != nil {
		return uuid.UUID{}, uuid.UUID{}, suppliers.ErrNoCredential
	}
	return supplier, credential, nil
}

// checkBody is a check as the contract answers it; pasted is a check of a key in a request, which has its place there.
func keyCheckBody(c suppliers.KeyCheck, pasted bool) adminapi.CredentialCheck {
	body := adminapi.CredentialCheck{Outcome: adminapi.CredentialCheckOutcome(c.Outcome), Last4: text(c.Last4)}
	if pasted {
		body.Index = &c.Index
	}
	if c.Credential != (uuid.UUID{}) {
		body.CredentialId = new(c.Credential.String())
	}
	if c.Cause != "" {
		body.Cause = new(adminapi.CredentialCheckCause(c.Cause))
	}
	if c.Region != "" {
		body.Region = new(c.Region)
	}
	if !c.CheckedAt.IsZero() { // a request was sent
		body.UpstreamStatus, body.CheckedAt = &c.UpstreamStatus, new(c.CheckedAt.UTC())
	}
	return body
}

// credentialBody is a credential as the contract gives it, with what this node holds against its limits now: the counts
// are the answering node's own (docs/domains/suppliers.md「凭证」).
func (a adminAPI) credentialBody(c suppliers.Credential) adminapi.SupplierCredential {
	body := credentialBody(c)
	if a.calls != nil {
		load := a.calls.CredentialLoad(c.ID, c.Supplier)
		inFlight, lastMinute := int(load.InFlight), int(load.LastMinute)
		body.InFlight, body.RequestsLastMinute = &inFlight, &lastMinute
	}
	return body
}
