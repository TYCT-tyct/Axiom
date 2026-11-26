package main

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// ListPlatformSubscriptions lists every account's subscriptions, newest first or soonest to end first, a page at a time.
func (a adminAPI) ListPlatformSubscriptions(ctx context.Context, r adminapi.ListPlatformSubscriptionsRequestObject) (adminapi.ListPlatformSubscriptionsResponseObject, error) {
	p := r.Params
	q := billing.SubscriptionQuery{EndsBefore: p.EndsBefore, ByEnd: p.Sort != nil && *p.Sort == adminapi.EndsAt, Limit: pageSize(p.Limit)}
	if p.State != nil {
		q.State = string(*p.State)
	}
	if p.PlanId != nil {
		id, err := uuid.Parse(*p.PlanId)
		if err != nil {
			return nil, invalidField("plan_id", "not a plan ID")
		}
		q.Plan = &id
	}
	var err error
	if q.Account, err = parseFilterID(p.AccountId, accounts.ParseAccountID, "account_id", "not an account ID"); err != nil {
		return nil, err
	}
	if q.After, err = pageCursorOf(p.Cursor); err != nil {
		return nil, err
	}
	var subs []billing.Subscription
	var next *string
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		rows, cursor, err := billing.ListAllSubscriptions(ctx, tx, q)
		if cursor != nil {
			v := cursor.String()
			next = &v
		}
		subs = rows
		return err
	})
	if err != nil {
		return nil, err
	}
	body := adminapi.SubscriptionPage{Data: make([]adminapi.Subscription, len(subs)), NextCursor: next}
	for i, s := range subs {
		if body.Data[i], err = subscriptionBody(s); err != nil {
			return nil, err
		}
	}
	return adminapi.ListPlatformSubscriptions200JSONResponse(body), nil
}

// GetSubscriptionSummary counts the request's site's subscriptions and what they sold for in a UTC month.
func (a adminAPI) GetSubscriptionSummary(ctx context.Context, r adminapi.GetSubscriptionSummaryRequestObject) (adminapi.GetSubscriptionSummaryResponseObject, error) {
	var month time.Time
	if r.Params.Month != nil {
		var err error
		if month, err = time.Parse("2006-01", *r.Params.Month); err != nil {
			return nil, invalidField("month", "not a month, YYYY-MM")
		}
	}
	site := callOf(ctx).site
	var s billing.SubscriptionSummary
	err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		s, err = billing.ReadSubscriptionSummary(ctx, tx, uuid.UUID(site.ID), site.Currency, month)
		return err
	})
	if err != nil {
		return nil, err
	}
	plans := make([]adminapi.SubscriptionPlanShare, len(s.Plans))
	for i, p := range s.Plans {
		plans[i] = adminapi.SubscriptionPlanShare{PlanId: p.Plan.String(), Active: p.Active, ShareBp: int(p.ShareBP)}
	}
	currency := site.Currency.String()
	return adminapi.GetSubscriptionSummary200JSONResponse{
		Month: s.Month.Format("2006-01"), Active: s.Active, EndingWithin7Days: s.EndingSoon, Plans: plans,
		Revenue:         adminapi.Money{Amount: s.Revenue.String(), Currency: currency},
		PreviousRevenue: adminapi.Money{Amount: s.PreviousRevenue.String(), Currency: currency},
	}, nil
}
