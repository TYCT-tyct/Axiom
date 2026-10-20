package main

import (
	"context"

	"axiom/internal/accounts"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// SetAccountStatus suspends an account or resumes it: its keys are refused at admission from the next call on, its
// console session and its users are left alone (docs/domains/accounts.md「停用与注销」).
func (a adminAPI) SetAccountStatus(ctx context.Context, request adminapi.SetAccountStatusRequestObject) (adminapi.SetAccountStatusResponseObject, error) {
	id, err := accountID(request.AccountId)
	if err != nil {
		return nil, err
	}
	body := request.Body
	detail, replayed, err := idempotently(ctx, a.db, "setAccountStatus", request.Params.IdempotencyKey, []any{request.AccountId, body},
		func(tx pg.Tx) (adminapi.AccountDetail, error) {
			account, err := a.accounts.SetAccountStatus(ctx, tx, actor(ctx), id, accounts.Status(body.Status), body.Reason)
			if err != nil {
				return adminapi.AccountDetail{}, err
			}
			currency, err := a.currencyOf(account.Site)
			if err != nil {
				return adminapi.AccountDetail{}, err
			}
			balance, err := a.readBalance(ctx, tx, id)
			if err != nil {
				return adminapi.AccountDetail{}, err
			}
			return accountBody(account, balance, currency), nil
		})
	if err != nil {
		return nil, err
	}
	headers := adminapi.SetAccountStatus200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	return adminapi.SetAccountStatus200JSONResponse{Body: detail, Headers: headers}, nil
}

// SetUserStatus disables a user's login or re-enables it; disabling ends every session the user has, of both surfaces.
func (a adminAPI) SetUserStatus(ctx context.Context, request adminapi.SetUserStatusRequestObject) (adminapi.SetUserStatusResponseObject, error) {
	id, err := accounts.ParseUserID(request.UserId)
	if err != nil {
		return nil, accounts.ErrNotFound
	}
	body := request.Body
	summary, replayed, err := idempotently(ctx, a.db, "setUserStatus", request.Params.IdempotencyKey, []any{request.UserId, body},
		func(tx pg.Tx) (adminapi.UserSummary, error) {
			user, err := a.accounts.SetUserStatus(ctx, tx, actor(ctx), id, accounts.Status(body.Status), body.Reason)
			return userBody(user), err
		})
	if err != nil {
		return nil, err
	}
	headers := adminapi.SetUserStatus200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	return adminapi.SetUserStatus200JSONResponse{Body: summary, Headers: headers}, nil
}
