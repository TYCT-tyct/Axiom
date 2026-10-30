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

// refundIDOf reads a refund's ID from a path. A malformed ID and an unknown one are the same 404.
func refundIDOf(s string) (payments.RefundID, error) {
	id, err := payments.ParseRefundID(s)
	if err != nil {
		return payments.RefundID{}, pg.ErrNoRows
	}
	return id, nil
}

// adminPayAmount is an amount in the currency a method is paid in, in its minor units, as the contract writes it.
func adminPayAmount(m payments.Money) adminapi.PayAmount {
	return adminapi.PayAmount{Amount: strconv.FormatInt(m.Minor, 10), Currency: m.Currency.String()}
}

// paymentRefundBody is a refund as the admin contract shows it. Credits are the site's accounting currency; the cash
// quote is in the currency the order was paid in, which is not always the same.
func paymentRefundBody(r payments.Refund, site platform.Currency) adminapi.PaymentRefund {
	id, order := r.ID.TypeID(), r.Order.TypeID()
	account := accounts.AccountID(r.AccountID).TypeID()
	state, via := adminapi.PaymentRefundState(r.State), adminapi.PaymentRefundVia(r.Via)
	credits, amount := adminMoney(r.Credits, site), adminPayAmount(r.Amount)
	reason, requested := r.Reason, accounts.UserID(r.RequestedBy).TypeID()
	body := adminapi.PaymentRefund{
		Id: &id, OrderId: &order, AccountId: &account, State: &state, Via: &via, Credits: &credits, Amount: &amount,
		Reason: &reason, RequestedBy: &requested, CreatedAt: &r.CreatedAt, FinishedAt: r.FinishedAt,
	}
	if r.ApprovedBy != nil {
		approved := accounts.UserID(*r.ApprovedBy).TypeID()
		body.ApprovedBy = &approved
	}
	if r.ProviderRef != "" {
		body.ProviderRef = &r.ProviderRef
	}
	return body
}

// paymentOrderBody is an order as the admin contract shows it, with its refunds. The checkout is left out: it may
// carry a client secret the operator never needs.
func paymentOrderBody(o payments.SiteOrder, site platform.Currency, refunds []payments.Refund) adminapi.PaymentOrder {
	id, account := o.ID.TypeID(), accounts.AccountID(o.AccountID).TypeID()
	user := accounts.UserID(o.UserID).TypeID()
	merchant := o.MerchantID.TypeID()
	method, state := string(o.Method), adminapi.PaymentOrderState(o.State)
	name, credits := o.MerchantName, adminMoney(o.Credits, site)
	total, fee := adminPayAmount(o.Total), adminapi.PayAmount{Amount: strconv.FormatInt(o.Fee, 10), Currency: o.Total.Currency.String()}
	allowed := o.RefundAllowed
	body := adminapi.PaymentOrder{
		Id: &id, AccountId: &account, UserId: &user, MerchantId: &merchant, MerchantName: &name, Method: &method,
		State: &state, Credits: &credits, Total: &total, Fee: &fee, CreatedAt: &o.CreatedAt, ExpiresAt: &o.ExpiresAt,
		PaidAt: o.PaidAt, RefundAllowed: &allowed,
	}
	if o.TradeNo != "" {
		body.TradeNo = &o.TradeNo
	}
	// Credited is in the site's accounting currency like Credits; the paid amounts are what the channel took.
	if o.Credited != nil {
		credited := adminMoney(*o.Credited, site)
		body.Credited = &credited
	}
	if o.PaidTotal != nil {
		paid := adminPayAmount(payments.Money{Currency: o.Total.Currency, Minor: *o.PaidTotal})
		body.PaidTotal = &paid
	}
	if o.PaidFee != nil {
		fee := adminPayAmount(payments.Money{Currency: o.Total.Currency, Minor: *o.PaidFee})
		body.PaidFee = &fee
	}
	list := make([]adminapi.PaymentRefund, len(refunds))
	for i, r := range refunds {
		list[i] = paymentRefundBody(r, site)
	}
	body.Refunds = &list
	return body
}

// orderQueryOf validates the order list's filters into the module's query. A filter the address leaves out does not
// narrow.
func orderQueryOf(site accounts.SiteID, p adminapi.ListPaymentOrdersParams) (payments.OrderQuery, error) {
	q := payments.OrderQuery{Site: uuid.UUID(site)}
	var err error
	if q.Account, err = parseFilterID(p.AccountId, accounts.ParseAccountID, "account_id", "not an account ID"); err != nil {
		return payments.OrderQuery{}, err
	}
	if q.User, err = parseFilterID(p.UserId, accounts.ParseUserID, "user_id", "not a user ID"); err != nil {
		return payments.OrderQuery{}, err
	}
	if q.Merchant, err = parseFilterID(p.MerchantId, payments.ParseMerchantID, "merchant_id", "not a merchant account ID"); err != nil {
		return payments.OrderQuery{}, err
	}
	if p.State != nil {
		q.State = payments.State(*p.State)
	}
	if p.Since != nil {
		q.Since = *p.Since
	}
	if p.Until != nil {
		q.Until = *p.Until
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && !q.Until.After(q.Since) {
		return payments.OrderQuery{}, invalidField("until", "must be after since")
	}
	return q, nil
}

// parseFilterID turns one optional ID filter from the address, refusing a malformed one as the query field that named
// it. An absent filter returns nil, which narrows nothing.
func parseFilterID[T ~[16]byte](raw *string, parse func(string) (T, error), field, message string) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // an absent filter is not an error
	}
	id, err := parse(*raw)
	if err != nil {
		return nil, invalidField(field, message)
	}
	value := uuid.UUID(id)
	return &value, nil
}

// pageCursorOf parses a keyset page cursor; an absent one starts at the newest row.
func pageCursorOf(raw *string) (*page.Cursor, error) {
	if raw == nil {
		return nil, nil //nolint:nilnil // an absent cursor starts at the newest row
	}
	cursor, err := page.ParseCursor(*raw)
	if err != nil {
		return nil, invalidField("cursor", "not a cursor this API gave out")
	}
	return &cursor, nil
}

// ListPaymentOrders lists one site's orders, newest first, with the refunds of each. The admin's site is the one named
// in the query, and the scope is the guard's: a site outside it reads as absent.
func (a adminAPI) ListPaymentOrders(ctx context.Context, r adminapi.ListPaymentOrdersRequestObject) (adminapi.ListPaymentOrdersResponseObject, error) {
	p := r.Params
	siteID, site, err := a.siteOf(p.SiteId)
	if err != nil {
		return nil, err
	}
	q, err := orderQueryOf(siteID, p)
	if err != nil {
		return nil, err
	}
	after, err := pageCursorOf(p.Cursor)
	if err != nil {
		return nil, err
	}
	service, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	orders, next, err := service.ListSiteOrders(ctx, callOf(ctx).scope, q, after, pageSize(p.Limit))
	if err != nil {
		return nil, err
	}
	ids := make([]payments.OrderID, len(orders))
	for i, o := range orders {
		ids[i] = o.ID
	}
	refunds, err := service.ListRefunds(ctx, callOf(ctx).scope, ids)
	if err != nil {
		return nil, err
	}
	data := make([]adminapi.PaymentOrder, len(orders))
	for i, o := range orders {
		data[i] = paymentOrderBody(o, site.Currency, refunds[o.ID])
	}
	list := adminapi.PaymentOrderList{Data: data}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return adminapi.ListPaymentOrders200JSONResponse(list), nil
}

// ListPaymentRefunds lists one site's refunds, newest first, for the review queue and follow-up.
func (a adminAPI) ListPaymentRefunds(ctx context.Context, r adminapi.ListPaymentRefundsRequestObject) (adminapi.ListPaymentRefundsResponseObject, error) {
	p := r.Params
	siteID, site, err := a.siteOf(p.SiteId)
	if err != nil {
		return nil, err
	}
	q := payments.RefundQuery{Site: uuid.UUID(siteID)}
	if p.State != nil {
		q.State = string(*p.State)
	}
	after, err := pageCursorOf(p.Cursor)
	if err != nil {
		return nil, err
	}
	service, err := a.paymentsService(ctx)
	if err != nil {
		return nil, err
	}
	refunds, next, err := service.ListSiteRefunds(ctx, callOf(ctx).scope, q, after, pageSize(p.Limit))
	if err != nil {
		return nil, err
	}
	data := make([]adminapi.PaymentRefund, len(refunds))
	for i, refund := range refunds {
		data[i] = paymentRefundBody(refund, site.Currency)
	}
	list := adminapi.PaymentRefundList{Data: data}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return adminapi.ListPaymentRefunds200JSONResponse(list), nil
}

// refundDecisionProblem answers a refund decision's refusal. A forbidden one is the state, not the person: the guard
// has already checked the permission, and a refund that is terminal is past review.
func refundDecisionProblem(err error) error {
	if errors.Is(err, payments.ErrRefundForbidden) {
		return &apiError{status: 409, code: codeConflict, message: "the refund is past review; a refund the channel saw is settled by its outcome"}
	}
	if e, ok := errors.AsType[*payments.FieldError](err); ok {
		return unprocessableFields("/reason", e.Reason)
	}
	return err
}

// decideRefund runs one refund decision and records the local decision and its answer under the request's
// Idempotency-Key in one transaction. The channel call, when there is one, happens after that commit: the decision is
// then durable and the refund poller can finish a send this node did not. A key used by another refund or request is
// refused before the decision runs, so two funds changes cannot share a key.
func (a adminAPI) decideRefund(ctx context.Context, operation, refundID, key string, body adminapi.ReasonRequest, send bool, prepare func(*payments.Payments, pg.Scope, payments.RefundID) error, decide func(pg.Tx, payments.RefundID, uuid.UUID, string) (payments.Refund, error)) (adminapi.PaymentRefund, bool, error) {
	id, err := refundIDOf(refundID)
	if err != nil {
		return adminapi.PaymentRefund{}, false, err
	}
	claim, err := idempotencyClaim(ctx, operation, key, []any{refundID, body})
	if err != nil {
		return adminapi.PaymentRefund{}, false, err
	}
	if stored, found, err := replayed[adminapi.PaymentRefund](ctx, a.db, claim); err != nil || found {
		return stored, found, err
	}
	service, err := a.paymentsService(ctx)
	if err != nil {
		return adminapi.PaymentRefund{}, false, err
	}
	if prepare != nil {
		if err = prepare(service, callOf(ctx).scope, id); err != nil {
			return adminapi.PaymentRefund{}, false, refundDecisionProblem(err)
		}
	}
	var decided payments.Refund
	stored, replayed, err := idempotentlyClaimed(ctx, a.db, claim, func(tx pg.Tx) (adminapi.PaymentRefund, error) {
		return a.storeRefundDecision(ctx, tx, id, body.Reason, &decided, decide)
	})
	if err != nil {
		return adminapi.PaymentRefund{}, false, err
	}
	a.startRefundSend(ctx, service, replayed, send, decided)
	return stored, replayed, nil
}

// startRefundSend begins the channel flow after the decision committed, unless the answer is a replay or the refund is
// offline. A failure here is left to the refund poller.
func (a adminAPI) startRefundSend(ctx context.Context, service *payments.Payments, replayed, send bool, refund payments.Refund) {
	if replayed || !send || refund.Via != "channel" {
		return
	}
	if err := service.SendRefund(ctx, callOf(ctx).scope, refund.ID, true); err != nil {
		a.logger.WarnContext(ctx, "refund decided but this node did not start its channel flow", "refund", refund.ID.TypeID(), "err", err)
	}
}

// storeRefundDecision runs one decision in the caller's transaction and renders the refund it decided for the stored
// idempotency answer.
func (a adminAPI) storeRefundDecision(ctx context.Context, tx pg.Tx, id payments.RefundID, reason string, decided *payments.Refund, decide func(pg.Tx, payments.RefundID, uuid.UUID, string) (payments.Refund, error)) (adminapi.PaymentRefund, error) {
	refund, err := decide(tx, id, uuid.UUID(callOf(ctx).principal.UserID()), reason)
	if err != nil {
		return adminapi.PaymentRefund{}, refundDecisionProblem(err)
	}
	*decided = refund
	_, site, err := a.siteOf(accounts.SiteID(refund.SiteID).TypeID())
	if err != nil {
		return adminapi.PaymentRefund{}, err
	}
	return paymentRefundBody(refund, site.Currency), nil
}

// ApprovePaymentRefund releases a refund as a second person and lets the channel flow start.
func (a adminAPI) ApprovePaymentRefund(ctx context.Context, r adminapi.ApprovePaymentRefundRequestObject) (adminapi.ApprovePaymentRefundResponseObject, error) {
	f := func(tx pg.Tx, id payments.RefundID, user uuid.UUID, reason string) (payments.Refund, error) {
		return payments.ApproveRefundTx(ctx, tx, id, user, reason)
	}
	body, replayed, err := a.decideRefund(ctx, "approvePaymentRefund", r.RefundId, r.Params.IdempotencyKey, *r.Body, true, nil, f)
	if err != nil {
		return nil, err
	}
	return adminapi.ApprovePaymentRefund200JSONResponse{
		Body: body, Headers: adminapi.ApprovePaymentRefund200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}

// RejectPaymentRefund declines a refund that nobody approved and no channel saw, returning its frozen credits.
func (a adminAPI) RejectPaymentRefund(ctx context.Context, r adminapi.RejectPaymentRefundRequestObject) (adminapi.RejectPaymentRefundResponseObject, error) {
	f := func(tx pg.Tx, id payments.RefundID, user uuid.UUID, reason string) (payments.Refund, error) {
		return payments.RejectRefundTx(ctx, tx, id, user, reason)
	}
	prepare := func(p *payments.Payments, scope pg.Scope, id payments.RefundID) error {
		return p.PrepareRejectRefund(ctx, scope, id)
	}
	body, replayed, err := a.decideRefund(ctx, "rejectPaymentRefund", r.RefundId, r.Params.IdempotencyKey, *r.Body, false, prepare, f)
	if err != nil {
		return nil, err
	}
	return adminapi.RejectPaymentRefund200JSONResponse{
		Body: body, Headers: adminapi.RejectPaymentRefund200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)},
	}, nil
}
