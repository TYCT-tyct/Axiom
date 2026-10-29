package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
)

const callExportRows = 10_000

type callDownload func(http.ResponseWriter) error

func (d callDownload) VisitExportCallsResponse(w http.ResponseWriter) error { return d(w) }

func (c consoleAPI) ExportCalls(ctx context.Context, request consoleapi.ExportCallsRequestObject) (consoleapi.ExportCallsResponseObject, error) {
	cl := callOf(ctx)
	q, err := ownCallQuery(cl)
	if err != nil {
		return nil, err
	}
	q, err = boundedCallWindow(q, request.Params.Since, request.Params.Until)
	if err != nil {
		return nil, err
	}
	return callDownload(func(w http.ResponseWriter) error {
		ctx, cancel := context.WithTimeout(ctx, callReportTimeout)
		defer cancel()
		deadline, _ := ctx.Deadline()
		sink := csvDownloadWriter{ResponseWriter: w, deadline: deadline, name: "calls"}
		w.Header().Set("Trailer", "X-Next-Cursor, X-Export-Complete")
		w.Header().Set("X-Export-Since", q.Since.UTC().Format(time.RFC3339Nano))
		w.Header().Set("X-Export-Until", q.Until.UTC().Format(time.RFC3339Nano))
		var next *observability.Cursor
		err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
			next, err = callsCSV(ctx, tx, q, &sink)
			return err
		})
		if err != nil && sink.started {
			panic(fmt.Errorf("console: stream calls: %w", err))
		}
		if err != nil {
			return err
		}
		if next != nil {
			w.Header().Set("X-Next-Cursor", next.String())
		}
		w.Header().Set("X-Export-Complete", "true")
		return nil
	}), nil
}

func callsCSV(ctx context.Context, tx pg.Tx, q observability.OwnQuery, out io.Writer) (*observability.Cursor, error) {
	w := csv.NewWriter(out)
	if err := w.Write([]string{"request_id", "at", "workspace_id", "key_id", "model", "client_app", "status", "error_kind", "price", "currency"}); err != nil {
		return nil, err
	}
	for written := 0; ; {
		q.PageSize = min(observability.MaxPageSize, callExportRows-written)
		calls, next, err := observability.ListOwn(ctx, tx, q)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			if err := w.Write(callCSVRow(call)); err != nil {
				return nil, err
			}
		}
		written += len(calls)
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, fmt.Errorf("write calls CSV: %w", err)
		}
		if next == nil || written == callExportRows {
			return next, nil
		}
		q.After = next
	}
}

func callCSVRow(c observability.OwnCall) []string {
	status := "success"
	if c.ErrorKind != "" {
		status = "error"
	}
	row := []string{
		c.ID.String(), c.At.UTC().Format(time.RFC3339Nano), accounts.WorkspaceID(c.Workspace).TypeID(),
		accounts.KeyID(c.Key).TypeID(), c.Model, c.ClientApp, status, c.ErrorKind, creditsOf(c.PriceNano), c.Currency,
	}
	for i, cell := range row {
		value := strings.TrimLeftFunc(cell, unicode.IsSpace)
		if value != "" && strings.ContainsAny(value[:1], "=+-@") {
			row[i] = "'" + cell
		}
	}
	return row
}
