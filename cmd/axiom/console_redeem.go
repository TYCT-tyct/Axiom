package main

import (
	"context"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
)

// redemptionBody is a redemption as the console shows it: what the code was worth, when it was redeemed and when the
// credit expires, never the code.
func redemptionBody(r payments.Redemption, site platform.Currency) consoleapi.RedeemCodeRedemption {
	return consoleapi.RedeemCodeRedemption{
		Id: r.ID.TypeID(), Amount: moneyOf(r.Amount, site), CodeHint: r.Hint, ExpiresAt: r.ExpiresAt,
		RedeemedAt: r.CreatedAt,
	}
}

// RedeemCode turns a code into credit, or answers the credit an earlier request with the same key made. The rate limit
// and the code's state are the module's; the refusal map turns each into the contract's answer.
func (c consoleAPI) RedeemCode(ctx context.Context, request consoleapi.RedeemCodeRequestObject) (consoleapi.RedeemCodeResponseObject, error) {
	cl := callOf(ctx)
	redemption, err := c.payments.Redeem(ctx, payments.Redeem{
		Site: cl.site, Account: uuid.UUID(cl.principal.AccountID()), User: uuid.UUID(cl.principal.UserID()),
		Code: request.Body.Code, Key: request.Params.IdempotencyKey, ClientIP: cl.ip,
	})
	if err != nil {
		return nil, err
	}
	body := redemptionBody(redemption, cl.site.Currency)
	if redemption.Replayed {
		return consoleapi.RedeemCode200JSONResponse(body), nil
	}
	return consoleapi.RedeemCode201JSONResponse(body), nil
}

// ListRedemptions lists the account's redeemed codes, newest first, a page at a time.
func (c consoleAPI) ListRedemptions(ctx context.Context, request consoleapi.ListRedemptionsRequestObject) (consoleapi.ListRedemptionsResponseObject, error) {
	cl := callOf(ctx)
	after, err := pageCursorOf(request.Params.Cursor)
	if err != nil {
		return nil, err
	}
	history, next, err := c.payments.ListRedemptions(ctx, cl.scope, uuid.UUID(cl.principal.AccountID()), after, pageSize(request.Params.Limit))
	if err != nil {
		return nil, err
	}
	list := consoleapi.RedeemCodeHistory{Data: make([]consoleapi.RedeemCodeRedemption, len(history))}
	for i, r := range history {
		list.Data[i] = redemptionBody(r, cl.site.Currency)
	}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return consoleapi.ListRedemptions200JSONResponse(list), nil
}
