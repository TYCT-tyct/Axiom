package main

import (
	"context"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/payments"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func (a adminAPI) ListPaymentDiscrepancies(ctx context.Context, r adminapi.ListPaymentDiscrepanciesRequestObject) (adminapi.ListPaymentDiscrepanciesResponseObject, error) {
	var before uuid.UUID
	var err error
	if r.Params.Before != nil {
		before, err = uuid.Parse(*r.Params.Before)
		if err != nil {
			return nil, invalidField("before", "must be a UUID")
		}
	}
	state := ""
	if r.Params.State != nil {
		state = string(*r.Params.State)
	}
	var rows []payments.Discrepancy
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { rows, err = payments.ListDiscrepancies(ctx, tx, before, state); return err })
	if err != nil {
		return nil, err
	}
	body := make([]adminapi.PaymentDiscrepancy, len(rows))
	for i, d := range rows {
		body[i], err = paymentDiscrepancyBody(d)
		if err != nil {
			return nil, err
		}
	}
	return adminapi.ListPaymentDiscrepancies200JSONResponse{Data: &body}, nil
}

func (a adminAPI) GetPaymentDiscrepancy(ctx context.Context, r adminapi.GetPaymentDiscrepancyRequestObject) (adminapi.GetPaymentDiscrepancyResponseObject, error) {
	id, err := uuid.Parse(r.DiscrepancyId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	var row payments.Discrepancy
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { row, err = payments.GetDiscrepancy(ctx, tx, id); return err })
	if err != nil {
		return nil, err
	}
	body, err := paymentDiscrepancyBody(row)
	return adminapi.GetPaymentDiscrepancy200JSONResponse(body), err
}

func (a adminAPI) InvestigatePaymentDiscrepancy(ctx context.Context, r adminapi.InvestigatePaymentDiscrepancyRequestObject) (adminapi.InvestigatePaymentDiscrepancyResponseObject, error) {
	id, err := uuid.Parse(r.DiscrepancyId)
	if err != nil {
		return nil, pg.ErrNoRows
	}
	input := struct {
		ID   string
		Body *adminapi.ReasonRequest
	}{r.DiscrepancyId, r.Body}
	body, replayed, err := idempotently(ctx, a.db, "investigatePaymentDiscrepancy", r.Params.IdempotencyKey, input, func(tx pg.Tx) (adminapi.PaymentDiscrepancy, error) {
		d, err := payments.InvestigateDiscrepancy(ctx, tx, id, r.Body.Reason, actor(ctx))
		if err != nil {
			return adminapi.PaymentDiscrepancy{}, err
		}
		return paymentDiscrepancyBody(d)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.InvestigatePaymentDiscrepancy201JSONResponse{Body: body, Headers: adminapi.InvestigatePaymentDiscrepancy201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func paymentDiscrepancyBody(d payments.Discrepancy) (adminapi.PaymentDiscrepancy, error) {
	body, err := adminJSON[adminapi.PaymentDiscrepancy](d)
	if err != nil {
		return body, err
	}
	site := accounts.SiteID(d.Site).TypeID()
	merchant := payments.MerchantID(d.Merchant).TypeID()
	body.SiteId, body.MerchantId = &site, &merchant
	if d.Account != nil {
		account := accounts.AccountID(*d.Account).TypeID()
		body.AccountId = &account
	}
	if d.Order != nil {
		order := payments.OrderID(*d.Order).TypeID()
		body.OrderId = &order
	}
	return body, nil
}
