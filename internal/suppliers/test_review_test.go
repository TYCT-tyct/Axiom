package suppliers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
)

func TestEndpointNotExecutedCostsZero(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 401, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			checkEndpointNotExecutedCost(t, status)
		})
	}
}

func checkEndpointNotExecutedCost(t *testing.T, status int) {
	t.Helper()
	w := newWorld(t, 1)
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(status) }))
	t.Cleanup(up.Close)
	w.pointAt(up.URL)
	r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure == nil || r.Failure.Executed != NotExecuted {
		t.Fatalf("not a refusal: %+v", r)
	}
	if r.Estimate() != "0" || r.Cost.Native != (platform.Exact{}) || r.Cost.Conversion != ConversionNotRun {
		t.Errorf("an unexecuted test has a cost: %+v", r)
	}
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		return RecordTest(ctx, tx, audit.User(uuid.NewV7()), "r", r)
	})
	if w.count(`SELECT count(*) FROM suppliers.attempt_costs WHERE test AND estimated_native = 0 AND conversion_state = 'not_executed' AND cost_flag = 'complete'`) != 1 {
		t.Error("the row does not describe a known zero cost")
	}
	if !strings.Contains(w.auditDetail("endpoint.test"), `"estimated_native": "0"`) {
		t.Error("audit lost the zero cost")
	}
}

func TestEndpointTenantOwnersAreNotTested(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"site", "account"} {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			checkEndpointTenantOwner(t, owner)
		})
	}
}

func checkEndpointTenantOwner(t *testing.T, owner string) {
	t.Helper()
	w := newWorld(t, 1)
	account := "NULL"
	if owner == "account" {
		account = "'" + uuid.NewV7().String() + "'"
	}
	err := pgtest.Run(t.Context(), w.database.Superuser, fmt.Sprintf(`UPDATE suppliers.suppliers SET owner_scope = '%s', site_id = '%s', account_id = %s WHERE id = '%s'`, owner, uuid.NewV7(), account, w.supplier))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	up := newTestUpstream(t, answersWhole)
	w.pointAt(up.URL)
	_, err = w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if !errors.Is(err, ErrTestOwner) || up.sent() != "" {
		t.Errorf("tenant test: %v, body %s", err, up.sent())
	}
	if w.count(`SELECT count(*) FROM suppliers.attempt_costs`) != 0 {
		t.Error("tenant refusal left a cost")
	}
}

// The probe lifetime is the table's own maximum, not a separate idle or header-wait timeout.
func TestEndpointLifetimeIsNotAnIdleFailure(t *testing.T) {
	t.Parallel()
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprint(committed), func(t *testing.T) {
			checkEndpointLifetime(t, committed)
		})
	}
}

func checkEndpointLifetime(t *testing.T, committed bool) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), TestDeadline)
		defer cancel()
		var prefix string
		if committed {
			prefix = chatEvent(`"choices":[{"index":0,"delta":{"content":"hi"}}]`)
		}
		r := TestResult{Format: protocol.Chat}
		r.read(ctx, io.MultiReader(strings.NewReader(prefix), deadlineBody(ctx.Done())), time.Now())
		cause, scope, ok := r.Problem()
		if r.Outcome() != TestInconclusive || !ok || cause != "timeout" || scope != "" {
			t.Errorf("lifetime judged the endpoint: %+v", r)
		}
		if (r.FirstToken != nil) != committed {
			t.Errorf("commit = %v, first token %v", committed, r.FirstToken)
		}
	})
}

type deadlineBody <-chan struct{}

func (b deadlineBody) Read([]byte) (int, error) { <-b; return 0, context.DeadlineExceeded }

func TestEndpointUnsentCostsZero(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.pointAt("http://127.0.0.1:0")
	r, err := w.svc.TestEndpoint(t.Context(), TestRequest{Endpoint: w.endpoint, Format: protocol.Chat})
	if err != nil {
		t.Fatal(err)
	}
	if r.Failure == nil || r.Failure.Scope != ScopeEgress || r.Failure.Executed != NotExecuted {
		t.Fatalf("unsent failure: %+v", r)
	}
	if r.Estimate() != "0" || r.Cost.Native != (platform.Exact{}) || r.Cost.Conversion != ConversionNotRun || len(r.Usage) != 0 {
		t.Errorf("an unsent test has a cost or usage: %+v", r)
	}
}

func TestCallChargeEventIsNotATest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	w.record(w.attempt(newRequest(), 1, "call"))
	w.ingest(w.charge("call", "1", w.clock.now()))
	w.reconcile(w.clock.now())
	if w.count(`SELECT count(*) FROM suppliers.charge_events WHERE payload->>'test' = 'false' AND payload->>'attribution' = 'attempt'`) != 1 {
		t.Error("normal charge event lacks its false test flag")
	}
}
