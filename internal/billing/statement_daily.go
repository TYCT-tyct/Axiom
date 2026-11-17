package billing

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// MaxStatementDays is the most nonempty calendar days a daily statement page holds.
const MaxStatementDays = 31

// MaxStatementTransactions bounds the complete history needed to reconstruct exact daily closing balances.
const MaxStatementTransactions = 5000

var (
	// ErrStatementDailySize refuses aggregation before reading entries when the account exceeds its read budget.
	ErrStatementDailySize = errors.New("billing: daily statement exceeds the transaction budget")
	// ErrStatementTimeZone is a calendar name PostgreSQL does not recognize.
	ErrStatementTimeZone = errors.New("billing: unknown statement time zone")
)

// StatementDay sums the statement's movements in billionths, as integer text because a day's total can exceed int64.
type StatementDay struct {
	Day, Spent, ToppedUp, Gifted, Expired string
	ClosingBalance                        *int64
}

// StatementDailyQuery selects the account's days in an IANA time zone, newest first. Before is the exclusive day
// returned by ParseStatementDailyCursor; empty starts at the newest day. TimeZone is validated against PostgreSQL's named time zones.
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
// tx must be a ReadTx so the size probe and aggregation see the same bounded history.
func ListStatementDaily(ctx context.Context, tx pg.Tx, q StatementDailyQuery) ([]StatementDay, *string, error) {
	if q.PageSize < 1 || q.PageSize > MaxStatementDays {
		return nil, nil, fmt.Errorf("billing: a page of %d statement days", q.PageSize)
	}
	if err := q.check(ctx, tx); err != nil {
		return nil, nil, err
	}
	rows, err := queries.StatementDailyKinds(ctx, tx, StatementDailyKindsParams{
		AccountID: q.Account, TimeZone: q.TimeZone, BeforeDay: q.Before, Max: int32(q.PageSize + 1),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("billing: read daily statement of %s: %w", q.Account, err)
	}
	out := make([]StatementDay, 0, q.PageSize+1)
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].Day != r.Day {
			out = append(out, StatementDay{Day: r.Day, Spent: "0", ToppedUp: "0", Gifted: "0", Expired: "0", ClosingBalance: r.ClosingBalance})
		}
		kind, err := statementKind(r.Kind, r.Source)
		if err != nil {
			return nil, nil, err
		}
		if err := out[len(out)-1].add(kind, r.Amount); err != nil {
			return nil, nil, err
		}
	}
	var next *string
	if len(out) > q.PageSize {
		cursor := statementDailyCursor(out[q.PageSize-1].Day, q.TimeZone)
		next = &cursor
		out = out[:q.PageSize]
	}
	return out, next, nil
}

func (q StatementDailyQuery) check(ctx context.Context, tx pg.Tx) error {
	var valid bool
	err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = $1)", q.TimeZone).Scan(&valid)
	if err != nil {
		return fmt.Errorf("billing: validate statement time zone: %w", err)
	}
	if !valid {
		return ErrStatementTimeZone
	}
	count, err := queries.StatementDailyBudget(ctx, tx, StatementDailyBudgetParams{AccountID: q.Account, Max: MaxStatementTransactions + 1})
	if err != nil {
		return fmt.Errorf("billing: probe daily statement size: %w", err)
	}
	if count > MaxStatementTransactions {
		return ErrStatementDailySize
	}
	return nil
}

func (d *StatementDay) add(kind StatementKind, amount string) error {
	var field *string
	var debit bool
	switch kind {
	case StatementSpend, StatementReversal:
		field, debit = &d.Spent, true
	case StatementTopUp, StatementRefund, StatementChargeback, StatementChargebackReversal:
		field = &d.ToppedUp
	case StatementGift, StatementRedeem:
		field = &d.Gifted
	case StatementExpiry:
		field, debit = &d.Expired, true
	case StatementFreeze, StatementUnfreeze:
		return nil
	default:
		return fmt.Errorf("billing: no daily statement category for %q", kind)
	}
	n, _ := new(big.Int).SetString(amount, 10)
	if debit {
		n.Neg(n)
	}
	total, _ := new(big.Int).SetString(*field, 10)
	*field = total.Add(total, n).String()
	return nil
}
