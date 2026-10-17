// Package idem makes a change happen once per idempotency key (internal/platform/README.md「幂等」). A caller picks a
// key for one intended change and may send the request again after a lost response: the second request gets the first
// one's response and nothing is done twice. The key is claimed in the transaction of the change itself, so it is held
// exactly when the change was made; a request that fails leaves no claim behind, and one that arrives while the first
// is still running waits for it, then replays it.
package idem

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// ErrReused is a key that was first used by another actor, for another operation or with another request. It is the
// caller's mistake: the key stands for one intended change.
var ErrReused = errors.New("idem: the key was used for another request")

// Claim is a request's right to its key: the key, who sends it, the operation it asks for, and a fingerprint of the
// request's content (see Fingerprint).
type Claim struct {
	Key         uuid.UUID
	Actor       audit.Actor
	Operation   string
	Fingerprint [sha256.Size]byte
}

// Fingerprint identifies a request by its parts, such as the path parameters and the body, as canonical JSON: the same
// content in another order or spelling is the same request.
func Fingerprint(parts ...any) ([sha256.Size]byte, error) {
	encoded, err := jsonx.MarshalCanonical(parts)
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("idem: fingerprint the request: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

var queries = New()

// Once runs do in tx, the transaction of the change, unless the claim's key was used before by the same request: then
// it returns that request's response with replayed set, and does nothing. do's response is stored with the claim, so it
// must be what the caller answers with; its error fails the change and, with the transaction, the claim.
func Once[T any](ctx context.Context, tx pg.Tx, c Claim, do func() (T, error)) (response T, replayed bool, err error) {
	var zero T
	claimed, err := queries.ClaimKey(ctx, tx, ClaimKeyParams{
		Key: c.Key, Actor: c.Actor.String(), Operation: c.Operation, Fingerprint: c.Fingerprint[:],
	})
	if err != nil {
		return zero, false, fmt.Errorf("idem: claim the key: %w", err)
	}
	if claimed == 0 {
		response, err = replay[T](ctx, tx, c)
		return response, err == nil, err
	}
	if response, err = do(); err != nil {
		return zero, false, err
	}
	stored, err := jsonx.MarshalCanonical(response)
	if err != nil {
		return zero, false, fmt.Errorf("idem: encode the response: %w", err)
	}
	if err := queries.FinishKey(ctx, tx, FinishKeyParams{Key: c.Key, Response: stored}); err != nil {
		return zero, false, fmt.Errorf("idem: store the response: %w", err)
	}
	return response, false, nil
}

// replay is the response of the request that claimed the key, if c is that request.
func replay[T any](ctx context.Context, tx pg.Tx, c Claim) (T, error) {
	var response T
	rows, err := queries.ClaimedKey(ctx, tx, c.Key)
	if err != nil {
		return response, fmt.Errorf("idem: read the claim: %w", err)
	}
	if len(rows) != 1 || rows[0].Response == nil {
		// The claim was committed before this statement, with its response, in one transaction.
		return response, fmt.Errorf("idem: the claim of key %s has no response", c.Key)
	}
	first := rows[0]
	if first.Actor != c.Actor.String() || first.Operation != c.Operation || string(first.Fingerprint) != string(c.Fingerprint[:]) {
		return response, ErrReused
	}
	if err := jsonx.Unmarshal(first.Response, &response); err != nil {
		return response, fmt.Errorf("idem: read the stored response: %w", err)
	}
	return response, nil
}
