package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/observability"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestCallReportFilters(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	for _, path := range []string{"/calls", "/calls/aggregate", "/calls/export"} {
		for _, filter := range []string{
			"key_id=" + accounts.KeyID(c.mid.Key).TypeID(),
			"workspace_id=" + accounts.WorkspaceID(c.mid.Workspace).TypeID(),
			"request_id=" + c.mid.ID.String(),
			"key_id=" + accounts.KeyID(c.mid.Key).TypeID() + "&status=success&model=claude-opus&client_app=claude-code",
		} {
			rec := c.f.do(http.MethodGet, path+"?"+filter, "", as(c.s))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
			}
			checkCallReport(t, path, rec, c.mid.ID.String())
		}
		checkCallReportBoundary(t, c, path)
	}
	for _, path := range []string{"/calls/aggregate", "/calls/export"} {
		for _, query := range []string{"since=2026-09-01T00:00:00Z&until=2026-10-29T00:00:00Z", "since=2026-10-29T00:00:00Z&until=2026-10-28T00:00:00Z"} {
			rec := c.f.do(http.MethodGet, path+"?"+query, "", as(c.s))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("invalid report window %s: %d", path, rec.Code)
			}
		}
	}
	rec := c.f.do(http.MethodGet, "/calls?since=2026-10-29T00:00:00Z&until=2026-10-28T00:00:00Z", "", as(c.s))
	if rec.Code != http.StatusOK {
		t.Errorf("legacy empty list window: %d", rec.Code)
	}
}

func checkCallReportBoundary(t *testing.T, c callsFixture, path string) {
	t.Helper()
	for _, filter := range []string{"status=unknown", "key_id=bad", "workspace_id=bad", "request_id=bad"} {
		rec := c.f.do(http.MethodGet, path+"?"+filter, "", as(c.s))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s?%s: %d %s", path, filter, rec.Code, rec.Body)
		}
	}
	rec := c.f.do(http.MethodGet, path+"?request_id="+c.stranger.ID.String(), "", as(c.s))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), c.stranger.ID.String()) {
		t.Errorf("foreign call leaks: %s", rec.Body)
	}
	if rec := c.f.do(http.MethodGet, path, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated %s: %d", path, rec.Code)
	}
}

func checkCallReport(t *testing.T, path string, rec *httptest.ResponseRecorder, id string) {
	t.Helper()
	switch path {
	case "/calls":
		var body consoleapi.CallList
		decode(t, rec, &body)
		if !slices.Equal(callIDs(body), []string{id}) {
			t.Errorf("path %s: %+v", path, body)
		}
	case "/calls/aggregate":
		var body consoleapi.CallAggregates
		decode(t, rec, &body)
		if body.Data == nil || len(*body.Data) != 1 || *(*body.Data)[0].Calls != "1" {
			t.Fatalf("%s: %s", path, rec.Body)
		}
	case "/calls/export":
		rows, err := csv.NewReader(rec.Body).ReadAll()
		if err != nil || len(rows) != 2 || rows[1][0] != id {
			t.Errorf("CSV %v: %v", rows, err)
		}
		if rec.Result().Trailer.Get("X-Export-Complete") != "true" {
			t.Error("no completion trailer")
		}
	}
}

func TestRecentErrorsBeforeTheFirstPage(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	failure := newCall(c.old.Account, c.now.Add(-4*time.Hour), "older-error", "codex")
	failure.ErrorKind = "upstream"
	c.f.record(failure)
	c.seedCallHistory(60)
	var page consoleapi.CallList
	decode(t, c.f.do(http.MethodGet, "/calls?status=error&limit=5", "", as(c.s)), &page)
	if page.StatusFilter == nil || *page.StatusFilter != consoleapi.CallStatusError {
		t.Fatalf("status filter not confirmed: %+v", page)
	}
	if !slices.Equal(callIDs(page), []string{failure.ID.String()}) {
		t.Fatalf("recent errors %+v", page)
	}
}

func TestCallAggregateExactTotals(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	a := newCall(c.old.Account, c.now.Add(-time.Minute), "expensive", "codex")
	b := a
	a.PriceNano, b.PriceNano, b.ID = math.MaxInt64, math.MaxInt64, uuid.NewV7()
	c.f.record(a, b)
	for _, group := range []string{"day", "model", "key"} {
		rec := c.f.do(http.MethodGet, "/calls/aggregate?model=expensive&group_by="+group, "", as(c.s))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s", rec.Body)
		}
		var body consoleapi.CallAggregates
		decode(t, rec, &body)
		if body.Data == nil || len(*body.Data) != 1 {
			t.Fatalf("%s", rec.Body)
		}
		checkAggregateContract(t, rec.Body.Bytes())
		row := (*body.Data)[0]
		amount, currency := "18446744073709551614", "USD"
		if diff := cmp.Diff(&consoleapi.CallAggregateMoney{Amount: &amount, Currency: &currency}, row.Price); diff != "" {
			t.Errorf("aggregate money (-want +got):\n%s", diff)
		}
		if *row.Calls != "2" {
			t.Errorf("%s", rec.Body)
		}
		if group == "key" && *row.Bucket != accounts.KeyID(a.Key).TypeID() {
			t.Errorf("internal key leaked: %s", rec.Body)
		}
	}
}

func checkAggregateContract(t *testing.T, body []byte) {
	t.Helper()
	spec, err := consoleapi.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err := jsonx.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	if err := spec.Components.Schemas["CallAggregates"].Value.VisitJSON(value); err != nil {
		t.Fatalf("aggregate response breaks its contract: %v", err)
	}
}

func TestCallCSVSafeExactCells(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", "  ", "=1+1", "+SUM(1,1)", "-1", "@command", " \t=1", "\r\n+1", "normal,\"quoted\"\nline", "模型"} {
		c := observability.OwnCall{ID: uuid.NewV7(), At: time.Now(), Model: text, ClientApp: text, ErrorKind: text, PriceNano: math.MaxInt64, Currency: "USD"}
		row := callCSVRow(c)
		want := text
		if value := strings.TrimSpace(text); value != "" && strings.ContainsAny(value[:1], "=+-@") {
			want = "'" + text
		}
		if row[4] != want || row[5] != want || row[7] != want || row[8] != "9223372036.854775807" {
			t.Errorf("row %q", row)
		}

	}
}

func (c callsFixture) seedCallHistory(n int) {
	c.f.t.Helper()
	c.f.run(`INSERT INTO observability.call_log SELECT (jsonb_populate_record(NULL::observability.call_log,
        to_jsonb(c) || jsonb_build_object('id', uuidv7(), 'at', c.at - n * interval '1 millisecond'))).*
        FROM observability.call_log c CROSS JOIN generate_series(1, ` + strconv.Itoa(n) + `) n
        WHERE id = '` + c.recent.ID.String() + `'`)
}

func TestCallCSVBoundAndContinuation(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	c.seedCallHistory(callExportRows)
	rec := c.f.do(http.MethodGet, "/calls/export", "", as(c.s))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(rows) != callExportRows+1 {
		t.Fatalf("%d rows: %v", len(rows), err)
	}
	trailer := rec.Result().Trailer
	next := trailer.Get("X-Next-Cursor")
	if next == "" || trailer.Get("X-Export-Complete") != "true" {
		t.Fatalf("trailers %v", trailer)
	}
	params := url.Values{"cursor": {next}, "since": {rec.Header().Get("X-Export-Since")}, "until": {rec.Header().Get("X-Export-Until")}}
	second := c.f.do(http.MethodGet, "/calls/export?"+params.Encode(), "", as(c.s))
	rest, err := csv.NewReader(second.Body).ReadAll()
	if err != nil || len(rest) != 4 {
		t.Fatalf("rest %d: %v %s", len(rest), err, second.Body)
	}
	seen := map[string]bool{}
	for _, row := range append(rows[1:], rest[1:]...) {
		if seen[row[0]] {
			t.Fatalf("duplicate %s", row[0])
		}
		seen[row[0]] = true
	}
	if len(seen) != callExportRows+3 || seen[c.stranger.ID.String()] {
		t.Fatalf("IDs %d", len(seen))
	}
}

func TestCallCSVStreamsAndCancels(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	c.seedCallHistory(300)
	q := observability.OwnQuery{Account: c.old.Account, Since: c.now.Add(-24 * time.Hour), Until: c.now}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var streamed bytes.Buffer
	sink := statementWriterFunc(func(b []byte) (int, error) { cancel(); return streamed.Write(b) })
	err := c.f.db.ReadTx(ctx, pg.NewScope(false, nil, []uuid.UUID{q.Account}), func(tx pg.Tx) error {
		_, err := callsCSV(ctx, tx, q, sink)
		return err
	})
	if !errors.Is(err, context.Canceled) || streamed.Len() == 0 {
		t.Fatalf("streamed %d bytes: %v", streamed.Len(), err)
	}
	err = c.f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := callsCSV(t.Context(), tx, q, statementWriterFunc(func([]byte) (int, error) { return 0, io.ErrClosedPipe }))
		return err
	})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error: %v", err)
	}
}

func TestCallReportWindowBounds(t *testing.T) {
	t.Parallel()
	until := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{0, 30, 31, 32} {
		since := until.Add(-time.Duration(days) * 24 * time.Hour)
		q, err := boundedCallWindow(observability.OwnQuery{Since: since, Until: until}, &since, &until)
		if (err == nil) != (days > 0 && days <= 31) {
			t.Errorf("%d days: %+v %v", days, q, err)
		}
	}
	since := endOfTime.Add(-24 * time.Hour)
	q, err := boundedCallWindow(observability.OwnQuery{Since: since, Until: endOfTime}, &since, &endOfTime)
	if err != nil || !q.Since.Equal(since) || !q.Until.Equal(endOfTime) {
		t.Fatalf("explicit extreme time window changed: %+v %v", q, err)
	}
	q, err = boundedCallWindow(observability.OwnQuery{Until: until}, nil, &until)
	if err != nil || q.Until.Sub(q.Since) != 30*24*time.Hour {
		t.Fatalf("default window: %+v %v", q, err)
	}
}

type callExportWriter struct {
	*httptest.ResponseRecorder
	beforeWrite func()
}

func (w *callExportWriter) Write(b []byte) (int, error) {
	if w.beforeWrite != nil {
		before := w.beforeWrite
		w.beforeWrite = nil
		before()
	}
	return w.ResponseRecorder.Write(b)
}

func TestCallCSVUsesOneSnapshot(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	c.seedCallHistory(300)
	late := newCall(c.old.Account, c.now.Add(-90*time.Minute), "late-insert", "codex")
	sink := &callExportWriter{ResponseRecorder: httptest.NewRecorder(), beforeWrite: func() { c.f.record(late) }}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+consoleHost+basePath+"/calls/export", nil)
	as(c.s)(r)
	c.f.routes.ServeHTTP(sink, r)
	if sink.Code != http.StatusOK {
		t.Fatalf("snapshot export: %d %s", sink.Code, sink.Body)
	}
	rows, err := csv.NewReader(sink.Body).ReadAll()
	if err != nil || len(rows) != 304 {
		t.Fatalf("snapshot %d rows: %v", len(rows), err)
	}
	for _, row := range rows[1:] {
		if row[0] == late.ID.String() {
			t.Fatal("late write entered existing snapshot")
		}
	}
}

func TestCallCSVQuotesUntrustedMetadata(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	r := newCall(c.old.Account, c.now.Add(-time.Minute), "=1,\"quoted\"\nline", " \t+1")
	r.ErrorKind = "@error"
	c.f.record(r)
	rec := c.f.do(http.MethodGet, "/calls/export?request_id="+r.ID.String(), "", as(c.s))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("CSV %v: %v", rows, err)
	}
	if rows[1][4] != "'"+r.Model || rows[1][5] != "'"+r.ClientApp || rows[1][7] != "'"+r.ErrorKind {
		t.Errorf("unsafe CSV row %q", rows[1])
	}
}
