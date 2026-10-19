package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"axiom/internal/billing"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

func exchangeProblem(err error) error {
	if e, ok := errors.AsType[*billing.ExchangeError](err); ok {
		return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "invalid exchange rate", fields: []errorField{{Pointer: "/" + e.Field, Message: e.Reason}}}
	}
	return err
}

func exchangeBody(r billing.BillingExchangeRate) adminapi.ExchangeRate {
	return adminapi.ExchangeRate{Id: r.ID.String(), Base: r.Base, Quote: r.Quote, Numerator: strconv.FormatInt(r.Numerator, 10), Denominator: strconv.FormatInt(r.Denominator, 10), Version: r.Version, Source: r.Source, Kind: r.Kind, Reason: r.Reason, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt}
}

func exchangeList(rows []billing.BillingExchangeRate) adminapi.ExchangeRateList {
	list := adminapi.ExchangeRateList{Data: make([]adminapi.ExchangeRate, len(rows))}
	for i, r := range rows {
		list.Data[i] = exchangeBody(r)
	}
	return list
}

// ListExchangeRates reads platform rates under the pricing.view permission of the generated guard.
func (a adminAPI) ListExchangeRates(ctx context.Context, _ adminapi.ListExchangeRatesRequestObject) (adminapi.ListExchangeRatesResponseObject, error) {
	var rows []billing.BillingExchangeRate
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) { rows, err = billing.ExchangeRates(ctx, tx); return err })
	if err != nil {
		return nil, err
	}
	return adminapi.ListExchangeRates200JSONResponse(exchangeList(rows)), nil
}

// ExchangeRateHistory accepts either direction; its response always names the stored direction.
func (a adminAPI) ExchangeRateHistory(ctx context.Context, r adminapi.ExchangeRateHistoryRequestObject) (adminapi.ExchangeRateHistoryResponseObject, error) {
	var rows []billing.BillingExchangeRate
	var before int64
	if r.Params.BeforeVersion != nil {
		before = *r.Params.BeforeVersion
	}
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		rows, err = billing.ExchangeRateHistory(ctx, tx, r.Base, r.Quote, before)
		return err
	})
	if err != nil {
		return nil, exchangeProblem(err)
	}
	return adminapi.ExchangeRateHistory200JSONResponse(exchangeList(rows)), nil
}

// SetExchangeRate claims the key, appends the version and records the audit atomically.
func (a adminAPI) SetExchangeRate(ctx context.Context, r adminapi.SetExchangeRateRequestObject) (adminapi.SetExchangeRateResponseObject, error) {
	b := r.Body
	in := billing.ExchangeInput{Base: b.Base, Quote: b.Quote, Numerator: b.Numerator, Denominator: b.Denominator, ExpectedVersion: b.ExpectedVersion, Kind: b.Kind, Reason: b.Reason}
	body, replayed, err := idempotently(ctx, a.db, "setExchangeRate", r.Params.IdempotencyKey, b, func(tx pg.Tx) (adminapi.ExchangeRate, error) {
		row, err := billing.SetExchangeRate(ctx, tx, actor(ctx), in)
		return exchangeBody(row), exchangeProblem(err)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.SetExchangeRate201JSONResponse{Body: body, Headers: adminapi.SetExchangeRate201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
