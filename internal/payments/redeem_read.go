package payments

import (
	"context"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5/pgtype" //nolint:depguard // the generated parameters take an optional timestamp

	"axiom/internal/platform/page"
	"axiom/internal/platform/pg"
)

// ListRedemptions is one page of the account's redeemed codes, newest first. It reads no code, only the hint and the
// credit's facts, so history never becomes a way to recover what was redeemed.
func (p *Payments) ListRedemptions(ctx context.Context, s pg.Scope, account uuid.UUID, after *page.Cursor, size int) ([]Redemption, *page.Cursor, error) {
	if account == uuid.Nil() {
		return nil, nil, errors.New("payments: list redemptions: no account")
	}
	if size < 1 || size > 200 {
		return nil, nil, fmt.Errorf("payments: list redemptions: page size %d, want 1 to 200", size)
	}
	at, id := endOfOrders, uuid.UUID{}
	if after != nil {
		at, id = after.At, after.ID
	}
	var rows []ListRedemptionsRow
	if err := p.db.ReadTx(ctx, s, func(tx pg.Tx) (err error) {
		rows, err = queries.ListRedemptions(ctx, tx, ListRedemptionsParams{
			AccountID: account, AfterAt: at, AfterID: id, Max: int32(size + 1), //nolint:gosec // size is at most 200
		})
		return err
	}); err != nil {
		return nil, nil, fmt.Errorf("payments: list redemptions: %w", err)
	}
	var next *page.Cursor
	if len(rows) > size {
		rows = rows[:size]
		last := rows[len(rows)-1]
		next = &page.Cursor{At: last.CreatedAt, ID: last.ID}
	}
	out := make([]Redemption, len(rows))
	for i, row := range rows {
		out[i] = Redemption{
			ID: RedemptionID(row.ID), Amount: row.Amount, Hint: row.CodeHint,
			ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt,
		}
	}
	return out, next, nil
}

// codeOf is a stored code as the page shows it: an enabled code past its expiry reads expired.
func codeOf(row PaymentsRedeemCode, now time.Time) RedeemCode {
	state := RedeemState(row.State)
	if state == RedeemEnabled && row.ExpiresAt.Valid && !row.ExpiresAt.Time.After(now) {
		state = RedeemExpired
	}
	return RedeemCode{
		ID: CodeID(row.ID), Site: row.SiteID, State: state, Amount: row.Amount, Note: row.Note, Hint: row.CodeHint,
		ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
		RedeemedAt: optionalTimestamp(row.RedeemedAt), DisabledAt: optionalTimestamp(row.DisabledAt),
	}
}

// redemptionOf is a stored redemption, its answer to a replay.
func redemptionOf(row RedemptionByKeyRow) Redemption {
	return Redemption{
		ID: RedemptionID(row.ID), Amount: row.Amount, Hint: row.CodeHint,
		ExpiresAt: optionalTimestamp(row.ExpiresAt), CreatedAt: row.CreatedAt,
	}
}

// timestamp is a Go expiry as the generated parameters take it.
func timestamp(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// optionalTimestamp is the reverse: no time, never the zero instant.
func optionalTimestamp(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	return new(t.Time)
}
