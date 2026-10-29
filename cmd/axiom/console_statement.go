package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// statementExport bounds response memory independently of the account's history.
const statementExport = 10_000

// statementRowOf is a statement row as the contract gives it.
func statementRowOf(row billing.StatementRow, currency platform.Currency) consoleapi.StatementRow {
	out := consoleapi.StatementRow{
		Id: row.ID.String(), At: row.At, Kind: consoleapi.StatementKind(row.Kind), Reference: row.Reference.String(),
		Amount:       moneyOf(row.Amount, currency),
		FrozenChange: moneyOf(row.FrozenChange, currency), FrozenAfter: moneyOf(row.Frozen, currency),
	}
	if row.Balance != nil {
		balance := moneyOf(*row.Balance, currency)
		out.BalanceAfter = &balance
	}
	return out
}

// ListStatement lists the account's credit statement, newest first, a page at a time.
func (c consoleAPI) ListStatement(ctx context.Context, request consoleapi.ListStatementRequestObject) (consoleapi.ListStatementResponseObject, error) {
	cl := callOf(ctx)
	q := billing.StatementQuery{Account: uuid.UUID(cl.principal.AccountID()), PageSize: pageSize(request.Params.Limit)}
	if request.Params.Cursor != nil {
		after, err := billing.ParseStatementCursor(*request.Params.Cursor)
		if err != nil {
			return nil, err
		}
		q.After = &after
	}
	var rows []billing.StatementRow
	var next *billing.StatementCursor
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		rows, next, err = billing.ListStatement(ctx, tx, q)
		return err
	}); err != nil {
		return nil, err
	}
	page := consoleapi.Statement{Data: make([]consoleapi.StatementRow, len(rows))}
	for i, row := range rows {
		page.Data[i] = statementRowOf(row, cl.site.Currency)
	}
	if next != nil {
		cursor := next.String()
		page.NextCursor = &cursor
	}
	return consoleapi.ListStatement200JSONResponse(page), nil
}

// statementDownload implements the generated response boundary while keeping the read transaction alive only while
// CSV is sent. A failure after the first bytes aborts the connection, so a truncated export cannot look complete.
type statementDownload func(http.ResponseWriter) error

func (d statementDownload) VisitExportStatementResponse(w http.ResponseWriter) error { return d(w) }

func (c consoleAPI) ExportStatement(ctx context.Context, _ consoleapi.ExportStatementRequestObject) (consoleapi.ExportStatementResponseObject, error) {
	cl := callOf(ctx)
	return statementDownload(func(w http.ResponseWriter) error {
		deadline, _ := ctx.Deadline()
		sink := csvDownloadWriter{ResponseWriter: w, deadline: deadline, name: "statement"}
		err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) error {
			return statementCSV(ctx, tx, uuid.UUID(cl.principal.AccountID()), cl.site.Currency, &sink)
		})
		if err != nil && sink.started {
			panic(fmt.Errorf("console: stream statement: %w", err))
		}
		return err
	}), nil
}

type csvDownloadWriter struct {
	http.ResponseWriter
	started  bool
	deadline time.Time
	name     string
}

func (w *csvDownloadWriter) Write(b []byte) (int, error) {
	if !w.started {
		if !w.deadline.IsZero() {
			// In-process response writers have no socket deadline.
			if err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(w.deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return 0, fmt.Errorf("console: bound %s download: %w", w.name, err)
			}
		}
		name := w.name + "-" + time.Now().UTC().Format(time.DateOnly) + ".csv"
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.started = true
	}
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		return n, err
	}
	return n, http.NewResponseController(w.ResponseWriter).Flush()
}

// statementCSV reads the account's statement as CSV, newest first: the same rows and the same order as the pages, a page
// at a time through the cursor, at most statementExport of them.
func statementCSV(ctx context.Context, tx pg.Tx, account uuid.UUID, currency platform.Currency, out io.Writer) error {
	w := csv.NewWriter(out)
	if err := w.Write([]string{"at", "kind", "amount", "balance_after", "currency", "reference", "frozen_change", "frozen_after"}); err != nil {
		return err
	}
	var after *billing.StatementCursor
	for written := 0; written < statementExport; {
		rows, next, err := billing.ListStatement(ctx, tx, billing.StatementQuery{
			Account: account, PageSize: billing.MaxStatementPage, After: after,
		})
		if err != nil {
			return err
		}
		for _, row := range rows[:min(len(rows), statementExport-written)] {
			if err := w.Write(statementCSVRow(row, currency)); err != nil {
				return err
			}
			written++
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return fmt.Errorf("console: write statement CSV: %w", err)
		}
		if next == nil {
			break
		}
		after = next
	}
	return nil
}

// statementCSVRow is one row of the file: the time in UTC as RFC 3339, the amounts as exact decimals of the site's
// currency, and the reference as the ID the pages give.
func statementCSVRow(row billing.StatementRow, currency platform.Currency) []string {
	return []string{
		row.At.UTC().Format(time.RFC3339), string(row.Kind), creditsOf(row.Amount), balanceCSV(row.Balance),
		currency.String(), row.Reference.String(), creditsOf(row.FrozenChange), creditsOf(row.Frozen),
	}
}

// balanceCSV leaves the cell empty where the balance after a movement is not known (billing.StatementRow.Balance).
func balanceCSV(balance *int64) string {
	if balance == nil {
		return ""
	}
	return creditsOf(*balance)
}

// creditsOf is an amount in billionths of a credit as the exact decimal it stands for: 1500000000 as 1.500000000,
// never through a floating point number.
func creditsOf(amount int64) string {
	digits, sign := strconv.FormatInt(amount, 10), ""
	if strings.HasPrefix(digits, "-") {
		digits, sign = digits[1:], "-"
	}
	digits = strings.Repeat("0", max(0, 10-len(digits))) + digits
	return sign + digits[:len(digits)-9] + "." + digits[len(digits)-9:]
}
