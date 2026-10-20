package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

// statementExport is how many movements a download holds: enough for months of a busy account's charges, and still a
// file of about a megabyte. An account with more pages through the list for the rest.
const statementExport = 10_000

// statementRowOf is a statement row as the contract gives it.
func statementRowOf(row billing.StatementRow, currency platform.Currency) consoleapi.StatementRow {
	return consoleapi.StatementRow{
		Id: row.ID.String(), At: row.At, Kind: consoleapi.StatementKind(row.Kind), Reference: row.Reference.String(),
		Amount: moneyOf(row.Amount, currency), BalanceAfter: moneyOf(row.Balance, currency),
	}
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

// ExportStatement answers the statement as a CSV file, newest first.
func (c consoleAPI) ExportStatement(ctx context.Context, _ consoleapi.ExportStatementRequestObject) (consoleapi.ExportStatementResponseObject, error) {
	cl := callOf(ctx)
	var body []byte
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		body, err = statementCSV(ctx, tx, uuid.UUID(cl.principal.AccountID()), cl.site.Currency)
		return err
	}); err != nil {
		return nil, err
	}
	name := "statement-" + time.Now().UTC().Format(time.DateOnly) + ".csv"
	return consoleapi.ExportStatement200TextcsvResponse{
		Body: bytes.NewReader(body), ContentLength: int64(len(body)),
		Headers: consoleapi.ExportStatement200ResponseHeaders{ContentDisposition: `attachment; filename="` + name + `"`},
	}, nil
}

// statementCSV reads the account's statement as CSV, newest first: the same rows and the same order as the pages, a page
// at a time through the cursor, at most statementExport of them.
func statementCSV(ctx context.Context, tx pg.Tx, account uuid.UUID, currency platform.Currency) ([]byte, error) {
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	if err := w.Write([]string{"at", "kind", "amount", "balance_after", "currency", "reference"}); err != nil {
		return nil, err
	}
	var after *billing.StatementCursor
	for written := 0; written < statementExport; {
		rows, next, err := billing.ListStatement(ctx, tx, billing.StatementQuery{
			Account: account, PageSize: billing.MaxStatementPage, After: after,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows[:min(len(rows), statementExport-written)] {
			if err := w.Write(statementCSVRow(row, currency)); err != nil {
				return nil, err
			}
			written++
		}
		if next == nil || len(rows) == 0 {
			break
		}
		after = next
	}
	w.Flush()
	return out.Bytes(), w.Error()
}

// statementCSVRow is one row of the file: the time in UTC as RFC 3339, the amounts as exact decimals of the site's
// currency, and the reference as the ID the pages give.
func statementCSVRow(row billing.StatementRow, currency platform.Currency) []string {
	return []string{
		row.At.UTC().Format(time.RFC3339), string(row.Kind), creditsOf(row.Amount), creditsOf(row.Balance),
		currency.String(), row.Reference.String(),
	}
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
