package main

import (
	"context"
	"errors"
	"strconv"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// redeemCodeIDOf reads a code's ID from a path. A malformed ID and an unknown one are the same 404.
func redeemCodeIDOf(s string) (payments.CodeID, error) {
	id, err := payments.ParseCodeID(s)
	if err != nil {
		return payments.CodeID{}, pg.ErrNoRows
	}
	return id, nil
}

// redeemProblem maps the module's field error to the JSON pointer the contract's errors use.
func redeemProblem(err error) error {
	if e, ok := errors.AsType[*payments.FieldError](err); ok {
		return unprocessableFields(e.Field, e.Reason)
	}
	return err
}

// redeemCodeBody is a code as the contract shows it; only a generation carries Code.
func redeemCodeBody(c payments.RedeemCode, currency platform.Currency) adminapi.RedeemCode {
	body := adminapi.RedeemCode{
		Id: adminapi.RedeemCodeId(c.ID.TypeID()), State: adminapi.RedeemCodeState(c.State),
		Amount: adminMoney(c.Amount, currency), Note: c.Note, Hint: c.Hint, CreatedAt: c.CreatedAt,
		CreatedBy: adminapi.UserId(accounts.UserID(c.CreatedBy).TypeID()),
		ExpiresAt: c.ExpiresAt, RedeemedAt: c.RedeemedAt,
	}
	if c.Code != "" {
		code := c.Code
		body.Code = &code
	}
	return body
}

// redeemListBody is a page of codes, with the cursor of the page after it when there is one.
func redeemListBody(codes []payments.RedeemCode, next *page.Cursor, currency platform.Currency) adminapi.RedeemCodeList {
	list := adminapi.RedeemCodeList{Data: make([]adminapi.RedeemCode, len(codes))}
	for i, c := range codes {
		list.Data[i] = redeemCodeBody(c, currency)
	}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return list
}

// ListRedeemCodes lists one site's codes, newest first, a page at a time. The rows never carry a code.
func (a adminAPI) ListRedeemCodes(ctx context.Context, request adminapi.ListRedeemCodesRequestObject) (adminapi.ListRedeemCodesResponseObject, error) {
	site, snapshot, err := a.siteOf(request.Params.SiteId)
	if err != nil {
		return nil, err
	}
	after, err := pageCursorOf(request.Params.Cursor)
	if err != nil {
		return nil, err
	}
	var state payments.RedeemState
	if request.Params.State != nil {
		state = payments.RedeemState(*request.Params.State)
	}
	var note string
	if request.Params.Note != nil {
		note = *request.Params.Note
	}
	size := defaultPage
	if request.Params.Limit != nil {
		size = *request.Params.Limit
	}
	p, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	codes, next, err := p.ListCodes(ctx, callOf(ctx).scope, uuid.UUID(site), state, note, after, size)
	if err != nil {
		return nil, redeemProblem(err)
	}
	return adminapi.ListRedeemCodes200JSONResponse(redeemListBody(codes, next, snapshot.Currency)), nil
}

// CreateRedeemCodes makes a batch and answers with the codes themselves, the only time they leave the server. The
// batch, its audit entry and the idempotency claim commit together, so a replay answers the same codes.
func (a adminAPI) CreateRedeemCodes(ctx context.Context, request adminapi.CreateRedeemCodesRequestObject) (adminapi.CreateRedeemCodesResponseObject, error) {
	site, snapshot, err := a.siteOf(request.Body.SiteId)
	if err != nil {
		return nil, err
	}
	amount, err := strconv.ParseInt(request.Body.Amount.Amount, 10, 64)
	if err != nil {
		return nil, invalidField("/amount/amount", "does not fit 64 bits")
	}
	if request.Body.Amount.Currency != snapshot.Currency.String() {
		return nil, invalidField("/amount/currency", "must be the site's currency, "+snapshot.Currency.String())
	}
	var note string
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	claim, err := idempotencyClaim(ctx, "createRedeemCodes", request.Params.IdempotencyKey, []any{request.Body})
	if err != nil {
		return nil, err
	}
	// The claim stores the list without the codes: they are a secret at rest, and a response kept for a replay would keep
	// them in the clear. A replay answers the same batch, its hints and no codes; the page says they cannot be shown
	// again, and the batch can be disabled and another made.
	var issued []payments.RedeemCode
	body, replayed, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.RedeemCodeList, error) {
		codes, err := payments.IssueCodes(ctx, tx, payments.Issue{
			Site: uuid.UUID(site), Amount: amount, Count: request.Body.Count, Note: note,
			ExpiresAt: request.Body.ExpiresAt, Actor: actor(ctx),
			CreatedBy: uuid.UUID(callOf(ctx).principal.UserID()), Reason: string(request.Body.Reason),
		})
		if err != nil {
			return adminapi.RedeemCodeList{}, redeemProblem(err)
		}
		issued = codes
		list := redeemListBody(codes, nil, snapshot.Currency)
		for i := range list.Data {
			list.Data[i].Code = nil
		}
		return list, nil
	})
	if err != nil {
		return nil, err
	}
	if !replayed {
		for i := range body.Data {
			if i < len(issued) {
				code := issued[i].Code
				body.Data[i].Code = &code
			}
		}
	} else {
		for i := range body.Data {
			body.Data[i].Code = nil
		}
	}
	return adminapi.CreateRedeemCodes201JSONResponse{
		Body: body, Headers: adminapi.CreateRedeemCodes201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// DisableRedeemCode stops an unused code from being redeemed, with the audit entry's reason.
func (a adminAPI) DisableRedeemCode(ctx context.Context, request adminapi.DisableRedeemCodeRequestObject) (adminapi.DisableRedeemCodeResponseObject, error) {
	id, err := redeemCodeIDOf(request.CodeId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "disableRedeemCode", request.Params.IdempotencyKey, []any{request.CodeId, request.Body}, func(tx pg.Tx) (adminapi.RedeemCode, error) {
		code, err := payments.DisableCode(ctx, tx, actor(ctx), uuid.UUID(callOf(ctx).principal.UserID()), id, request.Body.Reason)
		if err != nil {
			return adminapi.RedeemCode{}, err
		}
		snapshot, ok := a.accounts.Site(accounts.SiteID(code.Site))
		if !ok {
			return adminapi.RedeemCode{}, accounts.ErrNotFound
		}
		return redeemCodeBody(code, snapshot.Currency), nil
	})
	if err != nil {
		return nil, err
	}
	return adminapi.DisableRedeemCode200JSONResponse{
		Body: body, Headers: adminapi.DisableRedeemCode200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}
