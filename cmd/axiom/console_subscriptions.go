package main

import (
	"context"
	"strconv"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// ListSubscriptions is the account's own subscriptions with their windows: where a call's credit comes from when it is
// not the balance (docs/domains/billing.md「订阅套餐」).
func (c consoleAPI) ListAccountSubscriptions(ctx context.Context, _ consoleapi.ListAccountSubscriptionsRequestObject) (consoleapi.ListAccountSubscriptionsResponseObject, error) {
	cl := callOf(ctx)
	account := uuid.UUID(cl.principal.AccountID())
	var subs []billing.Subscription
	err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
		var err error
		subs, err = billing.Subscriptions(ctx, tx, account, maximumSubscriptions)
		return err
	})
	if err != nil {
		return nil, err
	}
	list := consoleapi.AccountSubscriptionList{Data: make([]consoleapi.AccountSubscription, len(subs))}
	for i, s := range subs {
		list.Data[i] = consoleapi.AccountSubscription{
			Id: openapi_types.UUID(s.ID), PlanName: s.PlanName, State: consoleapi.AccountSubscriptionStanding(s.State),
			StartsAt: s.StartsAt, EndsAt: s.EndsAt, ClosedAt: s.ClosedAt,
			CurrentWindow: consoleapi.AccountSubscriptionWindow{
				Seq: int(s.Window.Seq), StartsAt: s.Window.StartsAt, EndsAt: s.Window.EndsAt,
				Quota:     strconv.FormatInt(s.Window.Quota, 10),
				Used:      strconv.FormatInt(s.Window.Used, 10),
				Reserved:  strconv.FormatInt(s.Window.Reserved, 10),
				Remaining: strconv.FormatInt(s.Window.Remaining(), 10),
			},
		}
	}
	return consoleapi.ListAccountSubscriptions200JSONResponse(list), nil
}
