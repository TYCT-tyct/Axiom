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

// MaxStatementDays is the most nonempty calendar days a daily statement page holds.
const MaxStatementDays = 31

// StatementDay sums the statement's movements in billionths, as integer text because a day's total can exceed int64.
type StatementDay struct {
	Day, Spent, ToppedUp, Gifted, Expired string
	ClosingBalance                        *int64
}

// StatementDailyQuery selects the account's days in an IANA time zone, newest first. Before is the exclusive day
// returned by ParseStatementDailyCursor; empty starts at the newest day. TimeZone must have been validated at input.
type StatementDailyQuery struct {
	Account  uuid.UUID
	TimeZone string
	PageSize int
	Before   string
}

func statementDailyCursor(day, zone string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(day + "|" + zone))
}

// ParseStatementDailyCursor binds a continuation to the same calendar; changing zones requires starting a new page.
func ParseStatementDailyCursor(cursor, zone string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
	if err != nil {
		return "", ErrBadStatementCursor
	}
	day, calendar, ok := strings.Cut(string(raw), "|")
	if _, err := time.Parse(time.DateOnly, day); !ok || err != nil || calendar != zone {
		return "", ErrBadStatementCursor
	}
	return day, nil
}

// ListStatementDaily reads the same account-scoped balance and hold movements as ListStatement. Closing balances
// follow ledger sequence rather than timestamps, including when a clock stepped back across a calendar boundary.
func ListStatementDaily(ctx context.Context, tx pg.Tx, q StatementDailyQuery) ([]StatementDay, *string, error) {
	if q.PageSize < 1 || q.PageSize > MaxStatementDays {
		return nil, nil, fmt.Errorf("billing: a page of %d statement days", q.PageSize)
	}
	rows, err := queries.StatementDailyPage(ctx, tx, StatementDailyPageParams{
		AccountID: q.Account, TimeZone: q.TimeZone, BeforeDay: q.Before, Max: int32(q.PageSize + 1),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read daily statement of %s: %w", q.Account, err)
	}
	var next *string
	if len(rows) > q.PageSize {
		cursor := statementDailyCursor(rows[q.PageSize-1].Day, q.TimeZone)
		next = &cursor
		rows = rows[:q.PageSize]
	}
	out := make([]StatementDay, len(rows))
	for i, r := range rows {
		out[i] = StatementDay(r)
	}
	return out, next, nil
}
