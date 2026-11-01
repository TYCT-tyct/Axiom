package main

import (
	"context"
	"errors"
	"strconv"

	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
)

// subscriptionPlansAdmin is the catalog and the grants of subscription plans (docs/domains/billing.md「订阅套餐」): the
// administrator sets what a plan holds and sells, and grants or renews it on an account.

// planCurrency is the site's currency, which a plan's price and quota are in.
func (a adminAPI) planCurrency() (accounts.SiteID, platform.Currency, error) {
	site, ok := a.accounts.MainSite()
	if !ok {
		return accounts.SiteID{}, platform.Currency{}, accounts.ErrNotFound
	}
	return site.ID, site.Currency, nil
}

// planInput reads a plan from a request, checking what the API can check: whole minor units of the site's currency for
// the price, and a quota that is text because a JSON number would round it.
func planInput(in adminapi.SubscriptionPlanInput, currency platform.Currency) (billing.PlanInput, error) {
	amount, err := strconv.ParseInt(in.Price.Amount, 10, 64)
	if err != nil {
		return billing.PlanInput{}, unprocessableFields("/price/amount", "does not fit 64 bits")
	}
	if in.Price.Currency != currency.String() {
		return billing.PlanInput{}, unprocessableFields("/price/currency", "a plan is priced in the site's currency")
	}
	price, err := billing.MinorUnits(currency, amount)
	if err != nil {
		return billing.PlanInput{}, unprocessableFields("/price", err.Error())
	}
	quota, err := strconv.ParseInt(in.Quota, 10, 64)
	if err != nil || quota <= 0 {
		return billing.PlanInput{}, unprocessableFields("/quota", "a window's credit is a positive amount that fits 64 bits")
	}
	active := in.Active == nil || *in.Active
	return billing.PlanInput{Name: in.Name, Reason: in.Reason, Currency: currency, Price: price, Quota: quota,
		WindowSeconds: int64(in.WindowSeconds), TermSeconds: int64(in.TermSeconds), Active: active}, nil
}

// planBody is a plan as the contract shows it: the price in the site's currency, the quota in billionths.
func planBody(p billing.Plan, currency platform.Currency) (adminapi.SubscriptionPlan, error) {
	price, err := billing.FromMinor(currency, p.Price)
	if err != nil {
		return adminapi.SubscriptionPlan{}, err
	}
	return adminapi.SubscriptionPlan{
		Id: adminapi.Uuid(p.ID.String()), Name: p.Name, Price: adminMoney(price, currency),
		Quota: adminapi.CreditAmount(strconv.FormatInt(p.Quota, 10)), WindowSeconds: int(p.WindowSeconds),
		TermSeconds: int(p.TermSeconds), Active: p.Active, Rev: int(p.Rev),
	}, nil
}

// ListSubscriptionPlans lists the site's catalog.
func (a adminAPI) ListSubscriptionPlans(ctx context.Context, r adminapi.ListSubscriptionPlansRequestObject) (adminapi.ListSubscriptionPlansResponseObject, error) {
	site, currency, err := a.planCurrency()
	if err != nil {
		return nil, err
	}
	include := r.Params.IncludeInactive != nil && *r.Params.IncludeInactive
	var plans []billing.Plan
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		plans, err = billing.Plans(ctx, tx, uuid.UUID(site), include)
		return err
	}); err != nil {
		return nil, err
	}
	list := adminapi.SubscriptionPlanList{Data: make([]adminapi.SubscriptionPlan, len(plans))}
	for i, p := range plans {
		if list.Data[i], err = planBody(p, currency); err != nil {
			return nil, err
		}
	}
	return adminapi.ListSubscriptionPlans200JSONResponse(list), nil
}

// CreateSubscriptionPlan adds a plan to the catalog.
func (a adminAPI) CreateSubscriptionPlan(ctx context.Context, r adminapi.CreateSubscriptionPlanRequestObject) (adminapi.CreateSubscriptionPlanResponseObject, error) {
	site, currency, err := a.planCurrency()
	if err != nil {
		return nil, err
	}
	in, err := planInput(*r.Body, currency)
	if err != nil {
		return nil, err
	}
	body, replayed, err := idempotently(ctx, a.db, "createSubscriptionPlan", r.Params.IdempotencyKey, []any{r.Body},
		func(tx pg.Tx) (adminapi.SubscriptionPlan, error) {
			p, err := billing.CreatePlan(ctx, tx, uuid.UUID(site), in, actor(ctx))
			if err != nil {
				return adminapi.SubscriptionPlan{}, planProblem(err)
			}
			return planBody(p, currency)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.CreateSubscriptionPlan201JSONResponse{Body: body,
		Headers: adminapi.CreateSubscriptionPlan201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// UpdateSubscriptionPlan saves a plan at the revision it was read.
func (a adminAPI) UpdateSubscriptionPlan(ctx context.Context, r adminapi.UpdateSubscriptionPlanRequestObject) (adminapi.UpdateSubscriptionPlanResponseObject, error) {
	_, currency, err := a.planCurrency()
	if err != nil {
		return nil, err
	}
	id, err := planID(r.PlanId)
	if err != nil {
		return nil, err
	}
	update := r.Body
	body, replayed, err := idempotently(ctx, a.db, "updateSubscriptionPlan", r.Params.IdempotencyKey,
		[]any{r.PlanId, r.Body}, func(tx pg.Tx) (adminapi.SubscriptionPlan, error) {
			in, err := planInput(adminapi.SubscriptionPlanInput{Name: update.Name, Price: update.Price, Quota: update.Quota,
				WindowSeconds: update.WindowSeconds, TermSeconds: update.TermSeconds, Active: update.Active,
				Reason: update.Reason}, currency)
			if err != nil {
				return adminapi.SubscriptionPlan{}, err
			}
			p, err := billing.UpdatePlan(ctx, tx, id, int32(update.Rev), in, actor(ctx)) //nolint:gosec // G115: a revision the contract bounds at 1
			if err != nil {
				return adminapi.SubscriptionPlan{}, planProblem(err)
			}
			return planBody(p, currency)
		})
	if err != nil {
		return nil, err
	}
	return adminapi.UpdateSubscriptionPlan200JSONResponse{Body: body,
		Headers: adminapi.UpdateSubscriptionPlan200ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// GrantSubscription sells or gives the plan, or renews the one in force, and answers the subscription it made.
func (a adminAPI) GrantSubscription(ctx context.Context, r adminapi.GrantSubscriptionRequestObject) (adminapi.GrantSubscriptionResponseObject, error) {
	id, err := accountID(r.AccountId)
	if err != nil {
		return nil, err
	}
	plan, err := planID(r.Body.PlanId)
	if err != nil {
		return nil, err
	}
	source := "gift"
	if r.Body.Source != nil && *r.Body.Source == adminapi.SubscriptionSourceOfflinePayment {
		source = "paid"
	}
	body, replayed, err := idempotently(ctx, a.db, "grantSubscription", r.Params.IdempotencyKey, []any{r.AccountId, r.Body},
		func(tx pg.Tx) (adminapi.Subscription, error) {
			account, err := a.accounts.Account(ctx, tx, id)
			if err != nil {
				return adminapi.Subscription{}, err
			}
			currency, err := a.currencyOf(account.Site)
			if err != nil {
				return adminapi.Subscription{}, err
			}
			g := billing.SubscriptionGrant{ID: uuid.NewV7(), Site: uuid.UUID(account.Site), Account: uuid.UUID(id),
				Plan: plan, SiteCurrency: currency, Source: source, Actor: actor(ctx), Reason: r.Body.Reason}
			if source == "offline_payment" {
				if r.Body.Payment == nil {
					return adminapi.Subscription{}, unprocessableFields("/payment", "an offline payment says what was paid and where")
				}
				payment, channel, err := offlinePayment(r.Body.Payment)
				if err != nil {
					return adminapi.Subscription{}, err
				}
				g.Payment, g.Channel = *payment, billing.Channel{Offline: channel}
				g.RateID, err = rateFor(ctx, tx, currency, payment.Currency)
				if err != nil {
					return adminapi.Subscription{}, err
				}
			}
			if err := billing.CreditSubscription(ctx, tx, g); err != nil {
				return adminapi.Subscription{}, planProblem(err)
			}
			subs, err := billing.Subscriptions(ctx, tx, uuid.UUID(id), maximumSubscriptions)
			if err != nil {
				return adminapi.Subscription{}, err
			}
			if len(subs) == 0 {
				return adminapi.Subscription{}, errNoSubscription
			}
			return subscriptionBody(subs[0], currency) // the grant's subscription: a new one is newest, a renewal is the only active
		})
	if err != nil {
		return nil, err
	}
	return adminapi.GrantSubscription201JSONResponse{Body: body,
		Headers: adminapi.GrantSubscription201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// rateFor is the exchange rate a payment in its own currency is converted at: none in the site's currency, else the
// current reference rate.
func rateFor(ctx context.Context, tx pg.Tx, site, from platform.Currency) (uuid.UUID, error) {
	if from == site {
		return uuid.UUID{}, nil
	}
	rates, err := billing.ExchangeRateHistory(ctx, tx, from.String(), site.String(), 0)
	if err != nil {
		return uuid.UUID{}, err
	}
	if len(rates) == 0 {
		return uuid.UUID{}, unprocessableFields("/payment/amount/currency", "no reference rate to the site's currency")
	}
	return rates[0].ID, nil
}

// errNoSubscription is a grant whose subscription a reader could not see: the grant is in, so this is a stale read.
var errNoSubscription = errors.New("the subscription was not readable after the grant")

// ListSubscriptions lists an account's subscriptions.
func (a adminAPI) ListSubscriptions(ctx context.Context, r adminapi.ListSubscriptionsRequestObject) (adminapi.ListSubscriptionsResponseObject, error) {
	id, err := accountID(r.AccountId)
	if err != nil {
		return nil, err
	}
	var subs []billing.Subscription
	var currency platform.Currency
	err = a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		account, err := a.accounts.Account(ctx, tx, id)
		if err != nil {
			return err
		}
		if currency, err = a.currencyOf(account.Site); err != nil {
			return err
		}
		subs, err = billing.Subscriptions(ctx, tx, uuid.UUID(id), maximumSubscriptions)
		return err
	})
	if err != nil {
		return nil, err
	}
	list := adminapi.SubscriptionList{Data: make([]adminapi.Subscription, len(subs))}
	for i, s := range subs {
		if list.Data[i], err = subscriptionBody(s, currency); err != nil {
			return nil, err
		}
	}
	return adminapi.ListSubscriptions200JSONResponse(list), nil
}

// maximumSubscriptions bounds a listing: an account renews at most a few times a year.
const maximumSubscriptions = 100

// subscriptionBody is a subscription as the contract shows it.
func subscriptionBody(s billing.Subscription, currency platform.Currency) (adminapi.Subscription, error) {
	return adminapi.Subscription{
		Id: adminapi.Uuid(s.ID.String()), AccountId: accounts.AccountID(s.Account).TypeID(), PlanId: adminapi.Uuid(s.PlanID.String()),
		PlanName: s.PlanName, State: adminapi.SubscriptionStanding(s.State), StartsAt: s.StartsAt, EndsAt: s.EndsAt,
		ClosedAt: s.ClosedAt, CreatedAt: s.CreatedAt,
		CurrentWindow: adminapi.SubscriptionWindow{
			Seq: int(s.Window.Seq), StartsAt: s.Window.StartsAt, EndsAt: s.Window.EndsAt,
			Quota:     adminapi.CreditAmount(strconv.FormatInt(s.Window.Quota, 10)),
			Used:      adminapi.CreditAmount(strconv.FormatInt(s.Window.Used, 10)),
			Reserved:  adminapi.CreditAmount(strconv.FormatInt(s.Window.Reserved, 10)),
			Remaining: adminapi.CreditAmount(strconv.FormatInt(s.Window.Remaining(), 10)),
		},
	}, nil
}

// planID reads a plan's UUID from a path; a malformed one is an unknown plan.
func planID(s adminapi.Uuid) (uuid.UUID, error) {
	id, err := uuid.Parse(string(s))
	if err != nil {
		return uuid.UUID{}, pg.ErrNoRows
	}
	return id, nil
}

// planProblem maps the module's plan errors to the contract's refusals.
func planProblem(err error) error {
	if e, ok := errors.AsType[*billing.PlanError](err); ok {
		return unprocessable(e.Reason)
	}
	return err
}
