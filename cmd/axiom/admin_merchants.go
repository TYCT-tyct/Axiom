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
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// paymentsService is the payments module for one admin request. It is cheap: the plugins are compiled in and the
// per-process caches start empty, which a configuration change and a single probe do not need.
func (a adminAPI) paymentsService(ctx context.Context) (*payments.Payments, error) {
	return payments.Open(payments.Config{
		DB: a.db, Platform: callOf(ctx).scope, Keyring: a.keys, Client: a.exit, Logger: a.logger, Plugins: paymentPlugins,
	})
}

// merchantRequest is what the idempotency fingerprint covers: the request without the secrets, plus whether any were
// given. A persisted, unkeyed hash of a secret would let anyone with the table guess a weak gateway key offline, as
// SetMailSettings already avoids for the SMTP password.
func merchantRequest(m adminapi.MerchantInput) any {
	given := m.Secrets != nil
	m.Secrets = nil
	return struct {
		Merchant     adminapi.MerchantInput
		SecretsGiven bool
	}{m, given}
}

func merchantIDOf(s string) (payments.MerchantID, error) {
	id, err := payments.ParseMerchantID(s)
	if err != nil {
		return payments.MerchantID{}, pg.ErrNoRows
	}
	return id, nil
}

// unprocessableProblems answers every field a merchant request got wrong at once, by pointer.
func unprocessableProblems(p problems) error {
	if len(p) == 0 {
		return nil
	}
	return &apiError{status: 422, code: codeUnprocessable, message: "the request breaks a rule of the merchant account", fields: p}
}

// merchantProblem maps the module's field error to the JSON pointer the contract's errors use.
func merchantProblem(err error) error {
	if e, ok := errors.AsType[*payments.FieldError](err); ok {
		return unprocessableFields("/merchant"+e.Field, e.Reason)
	}
	return err
}

func merchantBody(m payments.Merchant) (adminapi.Merchant, error) {
	currency, err := platform.ParseCurrency(m.Currency)
	if err != nil {
		return adminapi.Merchant{}, err
	}
	methods := make([]adminapi.MerchantMethod, len(m.Methods))
	for i, method := range m.Methods {
		methods[i] = merchantMethodBody(currency, method)
	}
	id := payments.MerchantID(m.ID).TypeID()
	site := accounts.SiteID(m.SiteID).TypeID()
	kind, name, enabled := m.Kind, m.Name, m.Enabled
	refund, userRefund, sort, rev := m.RefundEnabled, m.UserRefund, int(m.Sort), m.Rev
	ttl := int(m.TtlSeconds)
	settings := jsonx.Raw(m.Settings)
	return adminapi.Merchant{
		Id: &id, SiteId: &site, Kind: &kind, Name: &name, Enabled: &enabled, Currency: &m.Currency,
		TtlSeconds: &ttl, RefundEnabled: &refund, UserRefund: &userRefund, Sort: &sort, Rev: &rev,
		Settings: &settings, Methods: &methods,
	}, nil
}

func merchantMethodBody(currency platform.Currency, method payments.PaymentsMerchantMethod) adminapi.MerchantMethod {
	fee := merchantMoney(currency, method.FeeFixed)
	name, upstream, enabled, ppm := method.Method, method.Upstream, method.Enabled, int(method.FeePpm)
	body := adminapi.MerchantMethod{
		Method: &name, Upstream: &upstream, FeePpm: &ppm, FeeFixed: &fee, Enabled: &enabled,
	}
	for _, limit := range []struct {
		value *int64
		field **adminapi.Money
	}{{method.SingleMin, &body.SingleMin}, {method.SingleMax, &body.SingleMax}, {method.DailyLimit, &body.DailyLimit}} {
		if limit.value == nil {
			continue
		}
		amount := merchantMoney(currency, *limit.value)
		*limit.field = &amount
	}
	return body
}

func merchantMoney(currency platform.Currency, minor int64) adminapi.Money {
	return adminapi.Money{Amount: payments.AmountFromMinor(currency, minor), Currency: currency.String()}
}

// merchantInput reads a request's merchant account. Money is in the merchant's own payment currency, in minor units on
// the module's side and the contract's exact decimal on the wire.
func merchantInput(body adminapi.MerchantInput, id payments.MerchantID) (payments.MerchantInput, error) {
	currency, err := platform.ParseCurrency(body.Currency)
	if err != nil {
		return payments.MerchantInput{}, unprocessableFields("/merchant/currency", "must be three upper-case letters")
	}
	in := payments.MerchantInput{
		ID: id, Site: uuid.UUID{}, Kind: payments.Kind(body.Kind), Name: body.Name, Enabled: body.Enabled,
		Currency: currency, TTLSeconds: int32(body.TtlSeconds), RefundEnabled: body.RefundEnabled, //nolint:gosec // the contract bounds it to 86400
		UserRefund: body.UserRefund, Sort: int32(body.Sort), Settings: []byte(body.Settings), //nolint:gosec // the contract bounds it to 2^31-1
	}
	if body.Rev != nil {
		in.Rev = *body.Rev
	}
	if body.Secrets != nil {
		in.Secrets = make(map[string]secret.Secret, len(*body.Secrets))
		for name, value := range *body.Secrets {
			in.Secrets[name] = secret.New(value)
		}
	}
	var p problems
	in.Methods = make([]payments.MethodInput, len(body.Methods))
	for i, method := range body.Methods {
		in.Methods[i] = merchantMethodInput(currency, method, i, &p)
	}
	if err := unprocessableProblems(p); err != nil {
		return payments.MerchantInput{}, err
	}
	return in, nil
}

func merchantMethodInput(currency platform.Currency, method adminapi.MerchantMethodInput, index int, p *problems) payments.MethodInput {
	prefix := "/merchant/methods/" + strconv.Itoa(index)
	in := payments.MethodInput{
		Method: payments.Method(method.Method), Upstream: method.Upstream,
		Fee: payments.Fee{PPM: int32(method.FeePpm)}, Enabled: method.Enabled, //nolint:gosec // the contract bounds it to 500000
	}
	if amount, ok := merchantMinor(method.FeeFixed, currency, prefix+"/fee_fixed", p); ok {
		in.Fee.Fixed = amount
	}
	in.SingleMin = merchantLimit(method.SingleMin, currency, prefix+"/single_min", p)
	in.SingleMax = merchantLimit(method.SingleMax, currency, prefix+"/single_max", p)
	in.DailyLimit = merchantLimit(method.DailyLimit, currency, prefix+"/daily_limit", p)
	return in
}

func merchantLimit(m *adminapi.Money, currency platform.Currency, pointer string, p *problems) *int64 {
	if m == nil {
		return nil
	}
	amount, ok := merchantMinor(*m, currency, pointer, p)
	if !ok {
		return nil
	}
	if amount <= 0 {
		p.add(pointer+"/amount", "must be positive")
		return nil
	}
	return &amount
}

func merchantMinor(m adminapi.Money, currency platform.Currency, pointer string, p *problems) (int64, bool) {
	if m.Currency != currency.String() {
		p.add(pointer+"/currency", "must be the merchant account's currency, "+currency.String())
		return 0, false
	}
	amount, ok := payments.MinorFromAmount(currency, m.Amount)
	if !ok {
		p.add(pointer+"/amount", "must be a whole minor unit of the currency")
		return 0, false
	}
	return amount, true
}

func (a adminAPI) ListMerchants(ctx context.Context, r adminapi.ListMerchantsRequestObject) (adminapi.ListMerchantsResponseObject, error) {
	site, _, err := a.siteOf(r.Params.SiteId)
	if err != nil {
		return nil, err
	}
	var rows []payments.Merchant
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		rows, err = payments.ListMerchants(ctx, tx, uuid.UUID(site))
		return err
	})
	if err != nil {
		return nil, err
	}
	data := make([]adminapi.Merchant, len(rows))
	for i, row := range rows {
		if data[i], err = merchantBody(row); err != nil {
			return nil, err
		}
	}
	return adminapi.ListMerchants200JSONResponse{Data: &data}, nil
}

func (a adminAPI) GetMerchant(ctx context.Context, r adminapi.GetMerchantRequestObject) (adminapi.GetMerchantResponseObject, error) {
	id, err := merchantIDOf(r.MerchantId)
	if err != nil {
		return nil, err
	}
	var m payments.Merchant
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		m, err = payments.ReadMerchant(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	body, err := merchantBody(m)
	return adminapi.GetMerchant200JSONResponse(body), err
}

func (a adminAPI) CreateMerchant(ctx context.Context, r adminapi.CreateMerchantRequestObject) (adminapi.CreateMerchantResponseObject, error) {
	site, _, err := a.siteOf(r.Body.SiteId)
	if err != nil {
		return nil, err
	}
	in, err := merchantInput(r.Body.Merchant, payments.MerchantID{})
	if err != nil {
		return nil, err
	}
	in.Site = uuid.UUID(site)
	claim, err := idempotencyClaim(ctx, "createMerchant", r.Params.IdempotencyKey, []any{r.Body.SiteId, merchantRequest(r.Body.Merchant), r.Body.Reason})
	if err != nil {
		return nil, err
	}
	// A replay answers from its claim before any preparation: the merchant may have changed since, and the probe must
	// not run for a change that is not made again.
	if body, found, err := replayed[adminapi.Merchant](ctx, a.db, claim); err != nil {
		return nil, err
	} else if found {
		return adminapi.CreateMerchant201JSONResponse{Body: body, Headers: adminapi.CreateMerchant201ResponseHeaders{IdempotencyReplayed: replayedHeader(true)}}, nil
	}
	p, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	// The probe runs before the idempotency transaction: it must not hold a connection while waiting on a gateway,
	// and an update's preflight read needs a connection of its own.
	change, err := p.PrepareMerchant(ctx, callOf(ctx).scope, in)
	if err != nil {
		// The first request under this key may have committed between the replay check and preparation.
		if body, found, replayErr := replayed[adminapi.Merchant](ctx, a.db, claim); replayErr == nil && found {
			return adminapi.CreateMerchant201JSONResponse{Body: body, Headers: adminapi.CreateMerchant201ResponseHeaders{IdempotencyReplayed: replayedHeader(true)}}, nil
		}
		return nil, merchantProblem(err)
	}
	body, replayed, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.Merchant, error) {
		saved, err := p.WriteMerchant(ctx, tx, actor(ctx), change, r.Body.Reason)
		if err != nil {
			return adminapi.Merchant{}, merchantProblem(err)
		}
		return merchantBody(saved)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateMerchant201JSONResponse{Body: body, Headers: adminapi.CreateMerchant201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) UpdateMerchant(ctx context.Context, r adminapi.UpdateMerchantRequestObject) (adminapi.UpdateMerchantResponseObject, error) {
	id, err := merchantIDOf(r.MerchantId)
	if err != nil {
		return nil, err
	}
	in, err := merchantInput(r.Body.Merchant, id)
	if err != nil {
		return nil, err
	}
	claim, err := idempotencyClaim(ctx, "updateMerchant", r.Params.IdempotencyKey, []any{r.MerchantId, merchantRequest(r.Body.Merchant), r.Body.Reason})
	if err != nil {
		return nil, err
	}
	// A replay answers from its claim before any preparation: the merchant may have changed since, and the probe must
	// not run for a change that is not made again.
	if body, found, err := replayed[adminapi.Merchant](ctx, a.db, claim); err != nil {
		return nil, err
	} else if found {
		return adminapi.UpdateMerchant200JSONResponse{Body: body, Headers: adminapi.UpdateMerchant200ResponseHeaders{IdempotencyReplayed: replayedHeader(true)}}, nil
	}
	p, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	// The probe runs before the idempotency transaction: it must not hold a connection while waiting on a gateway,
	// and an update's preflight read needs a connection of its own.
	change, err := p.PrepareMerchant(ctx, callOf(ctx).scope, in)
	if err != nil {
		// The first request under this key may have committed between the replay check and preparation.
		if body, found, replayErr := replayed[adminapi.Merchant](ctx, a.db, claim); replayErr == nil && found {
			return adminapi.UpdateMerchant200JSONResponse{Body: body, Headers: adminapi.UpdateMerchant200ResponseHeaders{IdempotencyReplayed: replayedHeader(true)}}, nil
		}
		return nil, merchantProblem(err)
	}
	body, replayed, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.Merchant, error) {
		saved, err := p.WriteMerchant(ctx, tx, actor(ctx), change, r.Body.Reason)
		if err != nil {
			return adminapi.Merchant{}, merchantProblem(err)
		}
		return merchantBody(saved)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateMerchant200JSONResponse{Body: body, Headers: adminapi.UpdateMerchant200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

func (a adminAPI) DisableMerchant(ctx context.Context, r adminapi.DisableMerchantRequestObject) (adminapi.DisableMerchantResponseObject, error) {
	id, err := merchantIDOf(r.MerchantId)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "disableMerchant", r.Params.IdempotencyKey, []any{r.MerchantId, r.Body}, func(tx pg.Tx) (adminapi.Merchant, error) {
		saved, err := payments.DisableMerchant(ctx, tx, actor(ctx), id, r.Body.Reason)
		if err != nil {
			return adminapi.Merchant{}, err
		}
		return merchantBody(saved)
	})
	if err != nil {
		return nil, err
	}
	return adminapi.DisableMerchant200JSONResponse{Body: body, Headers: adminapi.DisableMerchant200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}
