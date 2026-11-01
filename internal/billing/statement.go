package billing

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// StatementKind is what a movement of an account's credits was, as the user reads it.
type StatementKind string

// User-facing credit events; internal ledger kinds never appear on a statement.
const (
	StatementTopUp    StatementKind = "top_up" // paid credit, from a payment or an offline channel
	StatementGift     StatementKind = "gift"
	StatementRedeem   StatementKind = "redeem" // credit a redeem code gave
	StatementSpend    StatementKind = "spend"  // one call's charge
	StatementExpiry   StatementKind = "expiry"
	StatementRefund   StatementKind = "refund"
	StatementReversal StatementKind = "reversal"
	StatementFreeze   StatementKind = "freeze"
	StatementUnfreeze StatementKind = "unfreeze"
	// A chargeback took the credit back; its reversal gave it back after the dispute was won.
	StatementChargeback         StatementKind = "chargeback"
	StatementChargebackReversal StatementKind = "chargeback_reversal"
)

// MaxStatementPage is the most movements one page of a statement holds.
const MaxStatementPage = 200

// StatementRow projects only the account's balance and refund holds. Reference is the call ID for charges/reversals,
// otherwise the batch ID (the order ID for paid credit). See README.md「控制台的读取」.
type StatementRow struct {
	ID     uuid.UUID // the row's own identity; it names no ledger entry
	At     time.Time
	Kind   StatementKind
	Amount int64 // billionths of a credit, positive when the balance rose
	// Balance is the account's balance after this movement, in billionths of a credit, or nil for a row from before the
	// sequence: those are ordered by (created_at, id), which a clock stepping back or a shared microsecond keeps from
	// being the order they committed in (migration 00007), so the balance between two of them is not known. Frozen is
	// known for them: refund holds began after the sequence, so none of them moved it.
	Balance      *int64
	FrozenChange int64 // signed change in credits held for refunds; freezes do not move the total balance
	Frozen       int64 // frozen credits after this event
	Reference    uuid.UUID
}

// StatementCursor carries the last row's position and the counters for the next row. Carrying counters avoids
// summing all preceding entries on every page. A forged cursor can shift only the caller's own displayed counters;
// account RLS still determines which rows can be read.
type StatementCursor struct {
	Seq     int64     // the seq of the last numbered row read; 0 once the rows from before the sequence are all read
	At      time.Time // where the rows from before the sequence continue; the end of time until they begin
	ID      uuid.UUID
	Balance int64
	Frozen  int64
}

// statementCursorLen is a cursor's bytes: the seq, the time to the microsecond that the column keeps, the ID and the
// balance and frozen counters.
const statementCursorLen = 8 + 8 + 16 + 8 + 8

// ErrBadStatementCursor is text that is no cursor a page of a statement gave out.
var ErrBadStatementCursor = errors.New("billing: malformed statement cursor")

// String is the cursor as the console passes it back: opaque text, 64 characters.
func (c StatementCursor) String() string {
	var b [statementCursorLen]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.Seq))              // #nosec G115 -- the bits are kept, and read back by ParseStatementCursor
	binary.BigEndian.PutUint64(b[8:16], uint64(c.At.UnixMicro())) // #nosec G115 -- as for Seq
	copy(b[16:32], c.ID[:])
	binary.BigEndian.PutUint64(b[32:], uint64(c.Balance)) // #nosec G115 -- as for Seq, and a balance may be negative
	binary.BigEndian.PutUint64(b[40:], uint64(c.Frozen))  // #nosec G115 -- the bits are preserved
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// ParseStatementCursor reads what StatementCursor.String wrote, or returns ErrBadStatementCursor.
func ParseStatementCursor(s string) (StatementCursor, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != statementCursorLen {
		return StatementCursor{}, ErrBadStatementCursor
	}
	return StatementCursor{
		Seq:     int64(binary.BigEndian.Uint64(b[:8])),                         // #nosec G115 -- the bits String kept
		At:      time.UnixMicro(int64(binary.BigEndian.Uint64(b[8:16]))).UTC(), // #nosec G115 -- as for Seq
		ID:      uuid.UUID(b[16:32]),
		Balance: int64(binary.BigEndian.Uint64(b[32:])), // #nosec G115 -- the bits String kept
		Frozen:  int64(binary.BigEndian.Uint64(b[40:])), // #nosec G115 -- the bits String kept
	}, nil
}

// StatementQuery is a page of an account's statement: whose, how large, and where it starts; a nil After starts at the
// newest movement.
type StatementQuery struct {
	Account  uuid.UUID
	PageSize int
	After    *StatementCursor
}

// statementNewest is where a page that follows nothing starts: past every seq, and past every row from before the
// sequence.
var statementNewest = StatementCursor{Seq: math.MaxInt64, At: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), ID: uuid.Max()}

// start is where the page begins: after its cursor, or at the newest movement.
func (q StatementQuery) start() StatementCursor {
	if q.After != nil {
		return *q.After
	}
	return statementNewest
}

// statementRounds bounds work even when a long history contains only waived charges. Such a segment can return an
// empty page with a continuation cursor; the cursor advances past every examined transaction.
const statementRounds = 8

// ListStatement reads account-scoped balance and refund-hold events in commit order. Entry aggregation happens only
// after limiting candidate transactions; zero-effect transactions are skipped here, within statementRounds reads.
func ListStatement(ctx context.Context, tx pg.Tx, q StatementQuery) ([]StatementRow, *StatementCursor, error) {
	if q.PageSize < 1 || q.PageSize > MaxStatementPage {
		return nil, nil, fmt.Errorf("billing: a page of %d movements", q.PageSize)
	}
	if err := planLedgerPage(ctx, tx); err != nil {
		return nil, nil, err
	}
	after := q.start()
	statement := make([]StatementRow, 0, q.PageSize)
	for round := range statementRounds {
		rows, err := queries.StatementPage(ctx, tx, StatementPageParams{
			AccountID: q.Account, AfterSeq: after.Seq, Max: int32(q.PageSize + 1), AfterAt: after.At, AfterID: after.ID,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("billing: read the statement of %s: %w", q.Account, err)
		}
		if round == 0 && q.After == nil && len(rows) > 0 {
			after.Balance, after.Frozen = rows[0].Balance, rows[0].Frozen
		}
		var more bool
		statement, more, err = appendStatement(statement, rows, &after, q.PageSize)
		if err != nil {
			return nil, nil, err
		}
		if more {
			return statement, &after, nil
		}
		if len(rows) < q.PageSize+1 {
			return statement, nil, nil
		}
	}
	return statement, &after, nil
}

// appendStatement leaves after immediately before the first visible row that does not fit, including any skipped
// zero-effect rows in that position. This also carries unchanged balances across empty continuation pages.
func appendStatement(out []StatementRow, rows []StatementPageRow, after *StatementCursor, limit int) ([]StatementRow, bool, error) {
	for _, r := range rows {
		if r.Amount != 0 || r.FrozenChange != 0 {
			if len(out) == limit {
				return out, true, nil
			}
			kind, err := statementKind(r.Kind, r.Source)
			if err != nil {
				return nil, false, fmt.Errorf("billing: statement transaction %s: %w", r.ID, err)
			}
			out = append(out, StatementRow{
				ID: r.ID, At: r.CreatedAt, Kind: kind, Amount: r.Amount, Balance: balanceAfter(r, after.Balance),
				FrozenChange: r.FrozenChange, Frozen: after.Frozen, Reference: r.RequestID,
			})
		}
		*after = after.advance(r)
	}
	return out, false, nil
}

// balanceAfter is the balance after a numbered row, and nil after one from before the sequence (StatementRow.Balance).
func balanceAfter(r StatementPageRow, balance int64) *int64 {
	if r.Seq == 0 {
		return nil
	}
	return &balance
}

func (c StatementCursor) advance(r StatementPageRow) StatementCursor {
	c.Seq = r.Seq
	if r.Seq == 0 {
		c.At, c.ID = r.CreatedAt, r.ID
	}
	c.Balance -= r.Amount
	c.Frozen -= r.FrozenChange
	return c
}

// statementKind is what the ledger's kind of a transaction, and the source of the batch a credit opened, reads as on a
// statement. A kind with no card-facing meaning is an error rather than a row: skipping it would leave the balance column
// disagreeing with the money.
func statementKind(kind, source string) (StatementKind, error) {
	switch kind {
	case "credit":
		switch source {
		case "gift":
			return StatementGift, nil
		case "paid":
			return StatementTopUp, nil
		case "redeem":
			return StatementRedeem, nil
		}
		return "", fmt.Errorf("a credit from the batch source %q is not a statement kind", source)
	case "settle":
		return StatementSpend, nil
	case "expire":
		return StatementExpiry, nil
	case "refund":
		return StatementRefund, nil
	case "reverse":
		return StatementReversal, nil
	case "freeze":
		return StatementFreeze, nil
	case "unfreeze":
		return StatementUnfreeze, nil
	case "chargeback":
		return StatementChargeback, nil
	case "chargeback_reverse":
		return StatementChargebackReversal, nil
	}
	return "", fmt.Errorf("a %s transaction is not a statement kind", kind)
}
