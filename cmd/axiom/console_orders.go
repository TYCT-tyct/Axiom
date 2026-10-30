package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"uuid"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/page"
	"axiom/internal/platform/webui"
)

// returnPath is the page the payment channel sends the browser back to: the console's balance page, where the order
// the user just paid shows up. The console is served under webui.ConsoleMount, the same constant the router mounts it
// with. The order does not exist yet when the address is built (the module makes its ID), so the address cannot name it.
const returnPath = webui.ConsoleMount + "/balance"

// orderRefusals are how payments' refusals are answered (tried after refusals). A message is English for logs and
// developers, says what to do next, and never repeats what the client sent or what the merchant's gateway answered.
var orderRefusals = []refusal{
	// A refused order wraps the cause the channel gave, which may be a merchant not found: the refusal comes first.
	{payments.ErrRefused, http.StatusUnprocessableEntity, codeUnprocessable, "the payment channel did not accept this top-up; start a new one, with another payment method if it happens again"},
	{payments.ErrNotFound, http.StatusNotFound, codeNotFound, "no such resource"},
	{payments.ErrClosed, http.StatusUnprocessableEntity, codeNotReady, "top-ups are not open on this site"},
	{payments.ErrCreditsRange, http.StatusUnprocessableEntity, codeUnprocessable, "the amount is outside this site's top-up limits"},
	{payments.ErrOutOfRange, http.StatusUnprocessableEntity, codeUnprocessable, "the amount is too large to price; try a smaller one"},
	{payments.ErrNoMerchant, http.StatusUnprocessableEntity, codeUnprocessable, "no payment method can take this amount now; try another amount or method"},
	{payments.ErrDailyLimit, http.StatusUnprocessableEntity, codeUnprocessable, "this top-up would pass the account's daily limit; try a smaller amount or wait until tomorrow"},
	{payments.ErrFirstLimit, http.StatusUnprocessableEntity, codeUnprocessable, "a first top-up is limited; try a smaller amount"},
	{payments.ErrTooManyOrders, http.StatusConflict, codeConflict, "too many orders wait for payment; pay or cancel one first"},
	{payments.ErrNotWaiting, http.StatusConflict, codeConflict, "the order is not waiting for payment"},
	{payments.ErrKeyReused, http.StatusConflict, codeIdempotencyReuse, "the Idempotency-Key was first used for another request"},
	{payments.ErrTooManyCancels, http.StatusTooManyRequests, codeRateLimited, "too many cancellations lately; try again later"},
	{payments.ErrTooManyIPOrders, http.StatusTooManyRequests, codeRateLimited, "too many top-ups were started from this network lately; try again later"},
	{payments.ErrTooManyIPFailures, http.StatusTooManyRequests, codeRateLimited, "too many top-ups from this network failed lately; try again later"},
	{payments.ErrVerifyLimited, http.StatusTooManyRequests, codeRateLimited, "this order was checked a moment ago; wait a minute before checking it again"},
	{payments.ErrQueryPaused, http.StatusServiceUnavailable, codeUnavailable, "the payment channel is busy; wait a moment before checking again"},
	{payments.ErrUnconfirmed, http.StatusServiceUnavailable, codeUnavailable, "the payment channel did not answer; try again shortly, a payment already made is not lost"},
	{payments.ErrRefundInFlight, http.StatusConflict, codeConflict, "a refund of this order is still in flight; wait for its outcome"},
	{payments.ErrSecondPerson, http.StatusForbidden, codeForbidden, "a different person must approve this refund"},
	{payments.ErrRefundForbidden, http.StatusForbidden, codeForbidden, "a refund of this order is not available: the channel or the merchant does not allow it"},
	{payments.ErrRefundAmount, http.StatusUnprocessableEntity, codeUnprocessable, "there is nothing refundable left on this order, or the amount is more than that"},
}

func payAmount(m payments.Money) consoleapi.PayAmount {
	return consoleapi.PayAmount{Amount: strconv.FormatInt(m.Minor, 10), Currency: m.Currency.String()}
}

// creditsIn reads a credits amount as the account's site writes money: positive, in the site's currency.
func creditsIn(m consoleapi.Money, currency platform.Currency) (int64, error) {
	if m.Currency != currency.String() {
		return 0, invalidField("/credits/currency", "must be the site's currency, "+currency.String())
	}
	return parseCredits(m.Amount, "/credits/amount")
}

// parseCredits reads a decimal amount of billionths, which the contract holds to positive digits and which may still
// not fit 64 bits.
func parseCredits(amount, pointer string) (int64, error) {
	credits, err := strconv.ParseInt(amount, 10, 64)
	if err != nil || credits <= 0 {
		return 0, invalidField(pointer, "must be a positive amount that fits 64 bits")
	}
	return credits, nil
}

// checkoutBody is what a waiting order sends the user to; a field the channel did not give is absent.
func checkoutBody(c payments.Checkout) *consoleapi.Checkout {
	return &consoleapi.Checkout{PayUrl: text(c.PayURL), QrCode: text(c.QRCode), ClientSecret: text(c.ClientSecret)}
}

// orderBody is an order as the contract shows it: never the merchant account or the channel's own order number. Only
// a waiting order has a checkout, the one given if there is one: a paid or cancelled order has nothing to pay on.
func orderBody(o payments.Order, site platform.Currency, checkout payments.Checkout) consoleapi.Order {
	body := consoleapi.Order{
		Id: o.ID.TypeID(), State: consoleapi.OrderState(o.State), Method: string(o.Method), Credits: moneyOf(o.Credits, site),
		Total: payAmount(o.Total), Fee: payAmount(payments.Money{Currency: o.Total.Currency, Minor: o.Fee}),
		CreatedAt: o.CreatedAt, ExpiresAt: o.ExpiresAt, RefundAllowed: &o.RefundAllowed,
	}
	if o.State == payments.Pending && hasCheckout(checkout) {
		body.Checkout = checkoutBody(checkout)
	}
	return body
}

// refundState is the refund as the console shows it: whether it still waits for an administrator, is with the channel,
// or is final. A frozen refund that an administrator has released is already processing.
func refundState(r payments.Refund) consoleapi.RefundState {
	switch r.State {
	case "frozen":
		if r.Approved {
			return consoleapi.RefundProcessing
		}
		return consoleapi.RefundPendingReview
	case "sending", "pending":
		return consoleapi.RefundProcessing
	case "succeeded":
		return consoleapi.RefundSucceeded
	default: // "failed"; the module has no other state, and a new one must be taught here.
		return consoleapi.RefundDeclined
	}
}

// refundBody is a refund as the contract shows it. Credits are the site's accounting currency; the cash quote is the
// currency the order was paid in, which is not always the same.
func refundBody(r payments.Refund, site platform.Currency) consoleapi.OrderRefund {
	return consoleapi.OrderRefund{
		Id: r.ID.TypeID(), State: refundState(r), Credits: moneyOf(r.Credits, site), Amount: payAmount(r.Amount),
		CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt,
	}
}

// withRefunds attaches what a page read knows about an order's refunds; an empty list means none, which is different
// from the field being absent on a node that did not read them.
func withRefunds(body consoleapi.Order, refunds []payments.Refund, site platform.Currency) consoleapi.Order {
	list := make([]consoleapi.OrderRefund, len(refunds))
	for i, r := range refunds {
		list[i] = refundBody(r, site)
	}
	body.Refunds = &list
	return body
}

// hasCheckout reports whether the channel's answer was saved: it always has the channel's order number or a link.
func hasCheckout(c payments.Checkout) bool {
	return c.TradeNo != "" || c.PayURL != "" || c.QRCode != "" || c.ClientSecret != ""
}

// returnURL is where the channel sends the browser back to, built from the console's registered origin and nothing
// the request wrote. A console served over plain http cannot take payments.
func returnURL(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", &apiError{
			status: http.StatusUnprocessableEntity, code: codeNotReady,
			message: "payments need a console served over https; register an https domain for this site",
		}
	}
	return u.JoinPath(returnPath).String(), nil
}

// ListPaymentMethods quotes the credits through each method the site takes.
func (c consoleAPI) ListPaymentMethods(ctx context.Context, request consoleapi.ListPaymentMethodsRequestObject) (consoleapi.ListPaymentMethodsResponseObject, error) {
	cl := callOf(ctx)
	credits, err := parseCredits(request.Params.Credits, "credits")
	if err != nil {
		return nil, err
	}
	options, err := c.payments.Options(ctx, cl.scope, cl.site, credits)
	if err != nil {
		return nil, err
	}
	list := consoleapi.PaymentMethodList{Data: make([]consoleapi.PaymentMethod, len(options))}
	for i, o := range options {
		list.Data[i] = consoleapi.PaymentMethod{
			Method: string(o.Method), Total: payAmount(o.Total), Fee: payAmount(payments.Money{Currency: o.Total.Currency, Minor: o.Fee}),
		}
	}
	return consoleapi.ListPaymentMethods200JSONResponse(list), nil
}

// CreateOrder places the order and answers by what the order is now (placed), the same for the first request and a
// replay of its key.
func (c consoleAPI) CreateOrder(ctx context.Context, request consoleapi.CreateOrderRequestObject) (consoleapi.CreateOrderResponseObject, error) {
	cl := callOf(ctx)
	credits, err := creditsIn(request.Body.Credits, cl.site.Currency)
	if err != nil {
		return nil, err
	}
	origin, err := c.accounts.Origin(cl.r.Host)
	if err != nil {
		return nil, err
	}
	back, err := returnURL(origin)
	if err != nil {
		return nil, err
	}
	order, checkout, err := c.payments.CreateOrder(ctx, cl.scope, payments.CreateOrder{
		Site: cl.site, Account: uuid.UUID(cl.principal.AccountID()), User: uuid.UUID(cl.principal.UserID()),
		Method: payments.Method(request.Body.Method), Credits: credits, Key: request.Params.IdempotencyKey,
		ReturnURL: back, ClientIP: cl.ip,
	})
	if errors.Is(err, payments.ErrUnconfirmed) && order.ID != (payments.OrderID{}) {
		// The order the module returns is the one it placed, which a cancel, a callback or an expiry may have moved while
		// the channel was called: answer by what is stored now. A read that fails is a fault, not a waiting order.
		if order, err = c.payments.GetOrder(ctx, cl.scope, order.ID); err != nil {
			return nil, err
		}
		checkout = order.Checkout
	}
	return placed(order, cl.site.Currency, checkout, err)
}

// placed answers a request for an order by the order's state and checkout, never by whether the module returned an
// error: a replay of the key returns the stored order and no error whatever became of it, and a first request that
// ended unknown is read again by the caller (CreateOrder) so that this sees the stored order.
//
//   - failed: the channel refused it, a 422, for the first request and every replay;
//   - waiting with a saved checkout: 201 with the checkout to pay on;
//   - waiting without one: 202, the channel has not confirmed it, so nothing to pay on;
//   - anything else (paid, cancelled, expired, ...): 200 as the state says, nothing to pay on.
func placed(o payments.Order, site platform.Currency, checkout payments.Checkout, err error) (consoleapi.CreateOrderResponseObject, error) {
	switch {
	case err != nil:
		return nil, err
	case o.State == payments.Failed:
		return nil, payments.ErrRefused
	case o.State != payments.Pending:
		return consoleapi.CreateOrder200JSONResponse(orderBody(o, site, payments.Checkout{})), nil
	case !hasCheckout(checkout):
		return consoleapi.CreateOrder202JSONResponse(orderBody(o, site, payments.Checkout{})), nil
	}
	return consoleapi.CreateOrder201JSONResponse(orderBody(o, site, checkout)), nil
}

// ListOrders lists the account's orders, newest first, a page at a time, with each order's refunds.
func (c consoleAPI) ListOrders(ctx context.Context, request consoleapi.ListOrdersRequestObject) (consoleapi.ListOrdersResponseObject, error) {
	cl := callOf(ctx)
	var after *page.Cursor
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		cursor, err := page.ParseCursor(*request.Params.Cursor)
		if err != nil {
			return nil, err
		}
		after = &cursor
	}
	orders, next, err := c.payments.ListOrders(ctx, cl.scope, uuid.UUID(cl.principal.AccountID()), after, pageSize(request.Params.Limit))
	if err != nil {
		return nil, err
	}
	ids := make([]payments.OrderID, len(orders))
	for i, o := range orders {
		ids[i] = o.ID
	}
	refunds, err := c.payments.ListRefunds(ctx, cl.scope, ids)
	if err != nil {
		return nil, err
	}
	list := consoleapi.OrderList{Data: make([]consoleapi.Order, len(orders))}
	for i, o := range orders {
		// The channel's client secret is shown once, in the answer that made the order; a page read leaves it out and
		// carries only the link or code that is still safe to show.
		checkout := o.Checkout
		checkout.ClientSecret = ""
		list.Data[i] = withRefunds(orderBody(o, cl.site.Currency, checkout), refunds[o.ID], cl.site.Currency)
	}
	if next != nil {
		cursor := next.String()
		list.NextCursor = &cursor
	}
	return consoleapi.ListOrders200JSONResponse(list), nil
}

// GetOrder reads one of the account's orders, with its refunds; another account's is not found.
func (c consoleAPI) GetOrder(ctx context.Context, request consoleapi.GetOrderRequestObject) (consoleapi.GetOrderResponseObject, error) {
	cl := callOf(ctx)
	id, err := payments.ParseOrderID(request.OrderId)
	if err != nil {
		return nil, err
	}
	o, err := c.payments.GetOrder(ctx, cl.scope, id)
	if err != nil {
		return nil, err
	}
	refunds, err := c.payments.ListRefunds(ctx, cl.scope, []payments.OrderID{id})
	if err != nil {
		return nil, err
	}
	return consoleapi.GetOrder200JSONResponse(withRefunds(orderBody(o, cl.site.Currency, o.Checkout), refunds[id], cl.site.Currency)), nil
}

// CancelOrder cancels a waiting order of the account.
func (c consoleAPI) CancelOrder(ctx context.Context, request consoleapi.CancelOrderRequestObject) (consoleapi.CancelOrderResponseObject, error) {
	cl := callOf(ctx)
	id, err := payments.ParseOrderID(request.OrderId)
	if err != nil {
		return nil, err
	}
	o, err := c.payments.Cancel(ctx, cl.scope, id)
	if err != nil {
		return nil, err
	}
	return consoleapi.CancelOrder200JSONResponse(orderBody(o, cl.site.Currency, o.Checkout)), nil
}

// VerifyOrder asks the channel about one of the account's orders and answers with the order as it is afterwards.
func (c consoleAPI) VerifyOrder(ctx context.Context, request consoleapi.VerifyOrderRequestObject) (consoleapi.VerifyOrderResponseObject, error) {
	cl := callOf(ctx)
	id, err := payments.ParseOrderID(request.OrderId)
	if err != nil {
		return nil, err
	}
	o, err := c.payments.Verify(ctx, cl.scope, id)
	if err != nil {
		return nil, err
	}
	return consoleapi.VerifyOrder200JSONResponse(orderBody(o, cl.site.Currency, o.Checkout)), nil
}

// RequestRefund freezes the unused part of an order's paid credits and asks for an administrator's approval.
func (c consoleAPI) RequestRefund(ctx context.Context, request consoleapi.RequestRefundRequestObject) (consoleapi.RequestRefundResponseObject, error) {
	cl := callOf(ctx)
	id, err := payments.ParseOrderID(request.OrderId)
	if err != nil {
		return nil, err
	}
	credits, err := creditsIn(request.Body.Credits, cl.site.Currency)
	if err != nil {
		return nil, err
	}
	r, err := c.payments.Refund(ctx, cl.scope, payments.RefundRequest{
		Order: id, Credits: credits, Key: request.Params.IdempotencyKey, Reason: request.Body.Reason,
		Via: "channel", User: uuid.UUID(cl.principal.UserID()),
	})
	if err != nil {
		return nil, err
	}
	return consoleapi.RequestRefund201JSONResponse(refundBody(r, cl.site.Currency)), nil
}
