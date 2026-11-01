package payments

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stripe/stripe-go/v85"
)

// notice turns a verified Stripe event into the core's payment or dispute notice. An event the channel sends but this
// module does not act on is Relevant false, so Stripe gets a 2xx and stops retrying it.
func (c *stripeChannel) notice(event stripe.Event) (Notice, error) {
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted, stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded,
		stripe.EventTypeCheckoutSessionAsyncPaymentFailed, stripe.EventTypeCheckoutSessionExpired:
		var session stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
			return Notice{}, fmt.Errorf("stripe: decode the checkout session: %w", err)
		}
		order := session.ClientReferenceID
		if order == "" {
			order = session.Metadata["order"]
		}
		tradeNo := stripePaymentIntent(&session)
		raw := stripeRaw(event, session.ID)
		switch event.Type {
		case stripe.EventTypeCheckoutSessionAsyncPaymentFailed, stripe.EventTypeCheckoutSessionExpired:
			// A failed or expired session changes no state here: the order's own expiry and the query job decide, and
			// a later successful payment must still be able to credit it.
			return Notice{Relevant: true, Order: order, TradeNo: tradeNo, Raw: raw}, nil
		case stripe.EventTypeCheckoutSessionCompleted:
			if session.Status != stripe.CheckoutSessionStatusComplete || session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
				// An asynchronous method is still unpaid; its async_payment_succeeded follows.
				return Notice{}, nil
			}
		}
		return Notice{
			Relevant: true, Success: true, Order: order, TradeNo: tradeNo,
			Paid: stripeMoney(c.currency, session.AmountTotal, session.Currency), Raw: raw,
		}, nil

	case stripe.EventTypeChargeDisputeCreated, stripe.EventTypeChargeDisputeClosed,
		stripe.EventTypeChargeDisputeFundsWithdrawn, stripe.EventTypeChargeDisputeFundsReinstated:
		var dispute stripe.Dispute
		if err := json.Unmarshal(event.Data.Raw, &dispute); err != nil {
			return Notice{}, fmt.Errorf("stripe: decode the dispute: %w", err)
		}
		paymentIntent := ""
		if dispute.PaymentIntent != nil {
			paymentIntent = dispute.PaymentIntent.ID
		}
		order := ""
		if dispute.Charge != nil {
			order = dispute.Charge.Metadata["order"]
		}
		return Notice{
			Relevant: true, Raw: stripeRaw(event, dispute.ID),
			Dispute: &DisputeNotice{
				Provider: "stripe", ID: dispute.ID, PaymentIntent: paymentIntent, Order: order,
				Status: string(dispute.Status), Reason: string(dispute.Reason),
				Amount: stripeMoney(c.currency, dispute.Amount, dispute.Currency),
				Action: stripeDisputeAction(event.Type, dispute.Status),
			},
		}, nil
	}
	return Notice{}, nil
}

func stripeDisputeAction(kind stripe.EventType, status stripe.DisputeStatus) DisputeAction {
	switch kind {
	case stripe.EventTypeChargeDisputeFundsWithdrawn:
		return DisputeOpen
	case stripe.EventTypeChargeDisputeFundsReinstated:
		return DisputeWon
	}
	switch status {
	case stripe.DisputeStatusWon:
		return DisputeWon
	case stripe.DisputeStatusLost:
		return DisputeLost
	case stripe.DisputeStatusNeedsResponse, stripe.DisputeStatusUnderReview:
		return DisputeOpen
	default:
		// warning_needs_response, warning_under_review, warning_closed, prevented and any status this build does
		// not know are recorded without moving money; a person reads the discrepancy or the row.
		return DisputeWarning
	}
}

func stripeRaw(event stripe.Event, object string) string {
	parts := []string{event.ID, string(event.Type)}
	if object != "" {
		parts = append(parts, object)
	}
	return strings.Join(parts, " ")
}
