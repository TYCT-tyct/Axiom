package main

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

func (c consoleAPI) AggregateCalls(ctx context.Context, request consoleapi.AggregateCallsRequestObject) (consoleapi.AggregateCallsResponseObject, error) {
	ctx, cancel := context.WithTimeout(ctx, callReportTimeout)
	defer cancel()
	cl := callOf(ctx)
	q, err := ownCallQuery(cl)
	if err != nil {
		return nil, err
	}
	q, err = boundedCallWindow(q, request.Params.Since, request.Params.Until, ownCallReportWindow)
	if err != nil {
		return nil, err
	}
	group := "day"
	if request.Params.GroupBy != nil {
		group = string(*request.Params.GroupBy)
	}
	var rows []observability.OwnAggregate
	err = c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		rows, err = observability.AggregateOwn(ctx, tx, q, group)
		return err
	})
	if errors.Is(err, observability.ErrAggregateSize) {
		return nil, invalidField("group_by", "too many groups; narrow the window or filters")
	}
	if err != nil {
		return nil, err
	}
	data := make([]consoleapi.CallAggregate, len(rows))
	for i, row := range rows {
		bucket := row.Bucket
		if group == "key" {
			bucket = accounts.KeyID(uuid.MustParse(bucket)).TypeID()
		}
		data[i] = consoleapi.CallAggregate{
			Bucket: &bucket, Calls: &row.Calls,
			Price: &consoleapi.CallAggregateMoney{Amount: &row.PriceNano, Currency: &row.Currency},
		}
	}
	by := consoleapi.CallAggregatesGroupBy(group)
	return consoleapi.AggregateCalls200JSONResponse{Data: &data, Since: &q.Since, Until: &q.Until, GroupBy: &by}, nil
}
