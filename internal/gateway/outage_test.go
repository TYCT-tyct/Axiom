package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/protocol"
)

// refusals is the scraped count of requests refused because dep was out.
func refusals(t *testing.T, tel *telemetry.Telemetry, dep string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if value, ok := strings.CutPrefix(line, `axiom_gateway_outage_refusals_total{dependency="`+dep+`"`); ok {
			return strings.TrimSpace(value[strings.LastIndex(value, " "):])
		}
	}
	return "absent from:\n" + rec.Body.String()
}

// outageRig is a handler with nothing but the outage's state, its log and its metrics.
type outageRig struct {
	*Handler
	tel  *telemetry.Telemetry
	logs *bytes.Buffer
}

func newOutageRig(t *testing.T) outageRig {
	t.Helper()
	tel, err := telemetry.New()
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	h := &Handler{}
	h.Logger = slog.New(slog.NewTextHandler(logs, nil))
	h.outage = newOutage(h.Logger, tel.Metrics.Meter("axiom/gateway"))
	return outageRig{h, tel, logs}
}

func (r outageRig) fail(t *testing.T, err error) int {
	w := httptest.NewRecorder()
	r.Handler.fail(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil), protocol.Messages, err)
	return w.Code
}

func (r outageRig) lines() int { return strings.Count(r.logs.String(), "\n") }

// expectTold checks the log and the metric after n requests were refused because dep was out: one line, the full count, and one
// line more when the outage ends.
func (r outageRig) expectTold(t *testing.T, dep string, n int) {
	t.Helper()
	if r.lines() != 1 {
		t.Errorf("%d requests refused by an outage wrote %d log lines, want 1:\n%s", n, r.lines(), r.logs)
	}
	if got := refusals(t, r.tel, dep); got != strconv.Itoa(n) {
		t.Errorf("the metric counts %s refusals, want %d", got, n)
	}
	r.outage.recovered(dep)
	r.outage.recovered(dep) // a request after the end tells nothing more
	if r.lines() != 2 || !strings.Contains(r.logs.String(), "refused="+strconv.Itoa(n)) {
		t.Errorf("the end of the outage is not told once with its count:\n%s", r.logs)
	}
}

// TestAnOutageIsLoggedOnceNotPerRequest: however many requests an outage refuses, the log holds one line when it begins
// and one when it ends, while the metric counts every request.
func TestAnOutageIsLoggedOnceNotPerRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, dep string
		err       error
	}{
		{"stale snapshot", depAccounts, fmt.Errorf("authenticate: %w", accounts.ErrStale)},
		{"database", depDatabase, databaseDown(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newOutageRig(t)
			const n = 500
			for range n {
				if code := r.fail(t, tc.err); code != http.StatusServiceUnavailable {
					t.Fatalf("an outage is answered %d, want 503", code)
				}
			}
			r.expectTold(t, tc.dep, n)
		})
	}
}

// TestAFailureThatIsNotAnOutageIsStillLoggedPerRequest: only a dependency being out is told once; a bug is told each time.
func TestAFailureThatIsNotAnOutageIsStillLoggedPerRequest(t *testing.T) {
	t.Parallel()
	r := newOutageRig(t)
	for range 3 {
		r.fail(t, errors.New("a bug"))
	}
	if r.lines() != 3 {
		t.Errorf("3 failures that are not an outage wrote %d lines, want 3", r.lines())
	}
}

// databaseDown is the error a request meets when the database cannot be reached: the pool is lazy, so opening one for
// an address nothing listens on succeeds and the first transaction fails to connect.
func databaseDown(t *testing.T) error {
	t.Helper()
	db, err := pg.Open(t.Context(), "postgres://axiom:x@127.0.0.1:1/axiom?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	err = db.Tx(t.Context(), pg.Scope{}, func(pg.Tx) error { return nil })
	if _, ok := pg.Unavailable(err); !ok {
		t.Fatalf("an unreachable database gave %v", err)
	}
	return err
}

// TestANewReasonIsTold: a dependency whose outage changes its reason is told again, one that repeats its reason is not,
// and another dependency's outage is a line of its own.
func TestANewReasonIsTold(t *testing.T) {
	t.Parallel()
	r := newOutageRig(t)
	for _, step := range []struct {
		dep, reason string
		lines       int // the log's lines after the step
	}{
		{depDatabase, "cannot connect to the database", 1},
		{depDatabase, "cannot connect to the database", 1},
		{depDatabase, "the database has no connection left (SQLSTATE 53300)", 3}, // the old one ends, the new begins
		{depDatabase, "the database has no connection left (SQLSTATE 53300)", 3},
		{depAccounts, "the site snapshot is out of date", 4},
		{depCatalog, "the catalog snapshot is stale", 5},
	} {
		r.outage.refuse(t.Context(), step.dep, step.reason)
		if r.lines() != step.lines {
			t.Fatalf("after %s: %s the log has %d lines, want %d:\n%s", step.dep, step.reason, r.lines(), step.lines, r.logs)
		}
	}
}

// TestAHelperRequestEndsTheOutageItUsed: through the real handler, a database outage that a model listing met ends when
// a listing gets through, so the next outage is told as a new one; the listing does not use routing, so it leaves a
// routing outage standing.
func TestAHelperRequestEndsTheOutageItUsed(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	var logs bytes.Buffer
	r.handler.outage.logger = slog.New(slog.NewTextHandler(&logs, nil))
	r.handler.outage.refuse(t.Context(), depRouting, "the routing snapshot is out of date")
	admit := r.handler.admitTx
	down := databaseDown(t)
	broken := func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error {
		return down
	}
	list := func() int {
		resp, _ := r.do(http.MethodGet, "/v1/models", a.key, nil)
		return resp.StatusCode
	}
	for _, step := range []struct {
		admit func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error
		want  int
	}{{broken, http.StatusServiceUnavailable}, {admit, http.StatusOK}, {broken, http.StatusServiceUnavailable}} {
		r.handler.admitTx = step.admit
		if got := list(); got != step.want {
			t.Fatalf("a listing is answered %d, want %d", got, step.want)
		}
	}
	if starts := strings.Count(logs.String(), "is unavailable"); starts != 3 { // the routing one, and the database's twice
		t.Errorf("%d outages were told, want 3 (routing, then database twice):\n%s", starts, &logs)
	}
	if ends := strings.Count(logs.String(), "how=recovered"); ends != 1 {
		t.Errorf("%d outages ended, want 1 (the first database one; routing was not used by a listing):\n%s", ends, &logs)
	}
}
