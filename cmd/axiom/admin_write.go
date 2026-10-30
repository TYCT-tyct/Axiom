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
	claim, err := idempotencyClaim(ctx, operation, key, request)
	if err != nil {
		return response, false, err
	}
	return idempotentlyClaimed(ctx, db, claim, do)
}

// idempotencyClaim is the claim of a request under an Idempotency-Key before its change runs: the same actor, operation
// and request content that Once compares when the key is claimed.
func idempotencyClaim(ctx context.Context, operation, key string, request any) (idem.Claim, error) {
	id, err := keyOf(key)
	if err != nil {
		return idem.Claim{}, err
	}
	fingerprint, err := idem.Fingerprint(request)
	if err != nil {
		return idem.Claim{}, err
	}
	return idem.Claim{Key: id, Actor: actor(ctx), Operation: operation, Fingerprint: fingerprint}, nil
}

// idempotentlyClaimed runs the change under a claim made by idempotencyClaim and commits the claim with it.
func idempotentlyClaimed[T any](ctx context.Context, db *pg.DB, claim idem.Claim, do func(tx pg.Tx) (T, error)) (response T, replayed bool, err error) {
	err = db.Tx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		response, replayed, err = idem.Once(ctx, tx, claim, func() (T, error) { return do(tx) })
		return err
	})
	return response, replayed, err
}

// replayed reads a claim's stored response without claiming it, so a replayed request can skip preparation and probing.
// A key whose first request is still running has no response yet: found is false, and the caller prepares and lets Once
// wait for the first request.
func replayed[T any](ctx context.Context, db *pg.DB, claim idem.Claim) (response T, found bool, err error) {
	err = db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		var err error
		response, found, err = idem.Lookup[T](ctx, tx, claim)
		return err
	})
	return response, found, err
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
