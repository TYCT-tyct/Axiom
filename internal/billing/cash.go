package billing

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// CashMovement is one cash entry of the ledger: a paid credit in or a refund out, at the transaction's own time. The
// amount is decimal text because a set of movements can sum wider than int64; a payment currency's own unit is the
// amount's unit, as the ledger stores it (docs/domains/billing.md「钱的表示」).
type CashMovement struct {
	ID, Account, Batch uuid.UUID
	Kind               string
	At                 time.Time
	Amount, Currency   string
	OfflineChannel     string
	Online             bool
}

// CashCursor is the (created_at, id) of the last movement of a page; the next page starts after it.
type CashCursor struct {
	At time.Time
	ID uuid.UUID
}

func (c CashCursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()))
}

// ParseCashCursor reads what CashCursor.String writes; anything else is not a cursor this API gave out.
func ParseCashCursor(v string) (CashCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return CashCursor{}, fmt.Errorf("billing: read a cash cursor: %w", err)
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return CashCursor{}, fmt.Errorf("billing: read a cash cursor: no id")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return CashCursor{}, fmt.Errorf("billing: read a cash cursor: %w", err)
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return CashCursor{}, fmt.Errorf("billing: read a cash cursor: %w", err)
	}
	return CashCursor{At: t, ID: u}, nil
}

// CashQuery is one page of a site's cash movements: the window, the optional payer, and where the page starts.
type CashQuery struct {
	Site         uuid.UUID
	Account      *uuid.UUID
	Since, Until time.Time
	After        CashCursor
	Limit        int
}

// ListCashMovements reads one site's cash entries in [since, until), newest first, after a cursor. The time is the
// transaction's, not an order's: a refund of an older order and an offline top-up are both cash movements of this
// window (README.md「运营看板」).
func ListCashMovements(ctx context.Context, tx pg.Tx, q CashQuery) ([]CashMovement, *CashCursor, error) {
	rows, err := queries.ListCashMovements(ctx, tx, ListCashMovementsParams{
		SiteID: q.Site, AccountID: q.Account, Since: q.Since, Until: q.Until, AfterAt: q.After.At, AfterID: q.After.ID,
		Max: int32(q.Limit + 1), // #nosec G115 -- the admin contract bounds the page size at 200
	})
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read cash movements: %w", err)
	}
	out := make([]CashMovement, 0, len(rows))
	for _, r := range rows {
		out = append(out, CashMovement{
			ID: r.ID, Account: r.AccountID, Batch: r.BatchID, Kind: r.Kind, At: r.CreatedAt,
			Amount: r.Amount, Currency: r.Currency, OfflineChannel: r.OfflineChannel, Online: r.Online,
		})
	}
	var next *CashCursor
	if len(out) > q.Limit {
		out = out[:q.Limit]
		last := out[len(out)-1]
		next = &CashCursor{At: last.At, ID: last.ID}
	}
	return out, next, nil
}
