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

const (
	StatementTopUp  StatementKind = "top_up" // paid credit, from a payment or an offline channel
	StatementGift   StatementKind = "gift"
	StatementSpend  StatementKind = "spend" // one call's charge
	StatementExpiry StatementKind = "expiry"
	StatementRefund StatementKind = "refund"
)

// MaxStatementPage is the most movements one page of a statement holds.
const MaxStatementPage = 200

// StatementRow is one movement of an account's credits: what it was, what it did to the balance, and the balance after
// it. Reference is the call's request ID for a charge and the credit batch's ID for a top-up, a gift, an expiry and a
// refund (README.md「控制台的读取」).
type StatementRow struct {
	ID        uuid.UUID // the row's own identity; it names no ledger entry
	At        time.Time
	Kind      StatementKind
	Amount    int64 // billionths of a credit, positive when the balance rose
	Balance   int64 // billionths of a credit, the account's balance after this movement
	Reference uuid.UUID
}

// StatementCursor is where a page of a statement continues: the place of the page's last row in the account's ledger,
// and the balance after it. The balance is carried because the account's balance at a place in its ledger cannot be read
// again without summing every entry above it, which a statement cannot do page by page. A cursor only marks a place in
// one account's own statement: whoever forges one reads what the scope already lets them read, with a balance column
// shifted by what they chose.
type StatementCursor struct {
	Seq     int64     // the seq of the last numbered row read; 0 once the rows from before the sequence are all read
	At      time.Time // where the rows from before the sequence continue; the end of time until they begin
	ID      uuid.UUID
	Balance int64
}

// statementCursorLen is a cursor's bytes: the seq, the time to the microsecond that the column keeps, the ID and the
// balance.
const statementCursorLen = 8 + 8 + 16 + 8

// ErrBadStatementCursor is text that is no cursor a page of a statement gave out.
var ErrBadStatementCursor = errors.New("billing: malformed statement cursor")

// String is the cursor as the console passes it back: opaque text, 54 characters.
func (c StatementCursor) String() string {
	var b [statementCursorLen]byte
	binary.BigEndian.PutUint64(b[:8], uint64(c.Seq))              // #nosec G115 -- the bits are kept, and read back by ParseStatementCursor
	binary.BigEndian.PutUint64(b[8:16], uint64(c.At.UnixMicro())) // #nosec G115 -- as for Seq
	copy(b[16:32], c.ID[:])
	binary.BigEndian.PutUint64(b[32:], uint64(c.Balance)) // #nosec G115 -- as for Seq, and a balance may be negative
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

// ListStatement returns a page of the account's statement, newest first, and where the next page starts (nil on the last
// page), read in tx, which must hold the account's scope. One row is one movement of the user's balance: a credit, a
// charge, an expiry or a refund, with the balance after it; a transaction that moved none of it takes no row, and no
// entry or book is read out. A new movement arriving between pages lands above the cursor and does not shift the rows
// already read.
func ListStatement(ctx context.Context, tx pg.Tx, q StatementQuery) ([]StatementRow, *StatementCursor, error) {
	if q.PageSize < 1 || q.PageSize > MaxStatementPage {
		return nil, nil, fmt.Errorf("billing: a page of %d movements", q.PageSize)
	}
	after := statementNewest
	if q.After != nil {
		after = *q.After
	}
	rows, err := queries.StatementPage(ctx, tx, StatementPageParams{
		AccountID: q.Account, AfterSeq: after.Seq, Max: int32(q.PageSize + 1), AfterAt: after.At, AfterID: after.ID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read the statement of %s: %w", q.Account, err)
	}
	more := len(rows) > q.PageSize
	rows = rows[:min(len(rows), q.PageSize)]
	statement := make([]StatementRow, len(rows))
	// The page's newest row is where the balance stands if the page follows nothing, and otherwise the balance the
	// cursor carries; each row below it left the balance one movement further back.
	balance := after.Balance
	if q.After == nil && len(rows) > 0 {
		balance = rows[0].Balance
	}
	for i, r := range rows {
		kind, err := statementKind(r.Kind, r.Source)
		if err != nil {
			return nil, nil, fmt.Errorf("billing: the statement of %s, transaction %s: %w", q.Account, r.ID, err)
		}
		statement[i] = StatementRow{
			ID: r.ID, At: r.CreatedAt, Kind: kind, Amount: r.Amount, Balance: balance, Reference: r.RequestID,
		}
		balance -= r.Amount
	}
	if !more {
		return statement, nil, nil
	}
	last := rows[len(rows)-1]
	next := statementNewest
	next.Balance = balance
	if last.Seq != 0 {
		next.Seq = last.Seq // the rows from before the sequence are all below it, so its bound stays open
	} else {
		next.Seq, next.At, next.ID = 0, last.CreatedAt, last.ID
	}
	return statement, &next, nil
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
		}
		return "", fmt.Errorf("a credit from the batch source %q is not a statement kind", source)
	case "settle":
		return StatementSpend, nil
	case "expire":
		return StatementExpiry, nil
	case "refund":
		return StatementRefund, nil
	}
	return "", fmt.Errorf("a %s transaction is not a statement kind", kind)
}
