package main

import (
	"context"
	"strconv"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"
)

// actor is who the session in ctx is, for the audit log and the idempotency claim.
func actor(ctx context.Context) audit.Actor {
	return audit.User(uuid.UUID(callOf(ctx).principal.UserID()))
}

// keyOf reads an Idempotency-Key. The guard has held the header to the contract's pattern, which the all-zero UUID
// matches.
func keyOf(key string) (uuid.UUID, error) {
	id, err := uuid.Parse(key)
	if err != nil || id == (uuid.UUID{}) {
		return uuid.UUID{}, invalidField("Idempotency-Key", "must be a UUID that is not all zeros")
	}
	return id, nil
}

// idempotently runs a change of an operation that takes an Idempotency-Key as one transaction in the platform scope,
// the scope the guard authorized, and answers with its response. The first request with a key runs do and stores the
// response with the claim; the same request again, after a lost response, gets that response without do (replayed),
// and another request with the key is idempotency_key_reused. request is what the response depends on, the path
// parameters and the body: it is compared, not stored. do's error rolls back the transaction, the claim included.
func idempotently[T any](ctx context.Context, db *pg.DB, operation, key string, request any, do func(tx pg.Tx) (T, error)) (response T, replayed bool, err error) {
	id, err := keyOf(key)
	if err != nil {
		return response, false, err
	}
	fingerprint, err := idem.Fingerprint(request)
	if err != nil {
		return response, false, err
	}
	claim := idem.Claim{Key: id, Actor: actor(ctx), Operation: operation, Fingerprint: fingerprint}
	err = db.Tx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		response, replayed, err = idem.Once(ctx, tx, claim, func() (T, error) { return do(tx) })
		return err
	})
	return response, replayed, err
}

// replayedHeader is the value of the Idempotency-Replayed header: sent as true when the response is a replay, and
// not sent otherwise.
func replayedHeader(replayed bool) *bool {
	if !replayed {
		return nil
	}
	return &replayed
}

// adminMoney is an amount in billionths of a unit of the site's currency, as the contract writes money.
func adminMoney(amount int64, currency platform.Currency) adminapi.Money {
	return adminapi.Money{Amount: strconv.FormatInt(amount, 10), Currency: currency.String()}
}

// moneyIn reads m as an amount of currency; pointer is where m is in the request, for the answer to a bad one.
func moneyIn(m adminapi.Money, currency platform.Currency, pointer string, p *problems) (int64, bool) {
	amount, err := strconv.ParseInt(m.Amount, 10, 64)
	switch {
	case err != nil:
		p.add(pointer+"/amount", "does not fit 64 bits")
	case m.Currency != currency.String():
		p.add(pointer+"/currency", "must be the site's currency, "+currency.String())
	default:
		return amount, true
	}
	return 0, false
}
