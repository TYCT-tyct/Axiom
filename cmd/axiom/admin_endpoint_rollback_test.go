package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// Failing FinishKey after the paid call rolls back the cost, audit and claim; a retry therefore generates again.
func TestAdminEndpointTestRollbackAllowsAnotherGeneration(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		rw.Header().Set("X-Oneapi-Request-Id", "rollback-test")
		_, _ = io.WriteString(rw, endpointTestStream) //nolint:errcheck // a test server
	}))
	t.Cleanup(up.Close)
	base := newAdminFixture(t)
	f := supplierFixture{startAdminNodeVia(base.commandLine, base.clock, egress.NewForTesting(egresstest.Loopback(t))), base.owner()}
	endpoint := f.seedTestEndpoint(up.URL)
	body := `{"endpoint_id":"` + endpoint + `","format":"chat","reason":"check"}`
	key := newKey()
	f.run(`ALTER TABLE platform.idempotency ADD CONSTRAINT reject_test_response CHECK (response IS NULL OR operation <> 'testSupplierEndpoint') NOT VALID`)
	rec := f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key)
	expectAdmin(t, "response write refused", rec, http.StatusInternalServerError, codeInternal)
	if calls.Load() != 1 || f.auditCount("endpoint.test") != "0" || f.text(`SELECT count(*)::text FROM suppliers.attempt_costs`) != "0" {
		t.Fatal("the paid call did not roll back its cost and audit together")
	}
	if f.text(`SELECT count(*)::text FROM platform.idempotency WHERE key = '`+key+`'`) != "0" {
		t.Fatal("the failed response retained its claim")
	}
	f.run(`ALTER TABLE platform.idempotency DROP CONSTRAINT reject_test_response`)
	expectAdmin(t, "retry after rollback", f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key), http.StatusCreated, "")
	rec = f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key)
	expectAdmin(t, "replay after commit", rec, http.StatusCreated, "")
	if calls.Load() != 2 || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("calls %d, headers %v", calls.Load(), rec.Header())
	}
	if f.auditCount("endpoint.test") != "1" || f.text(`SELECT count(*)::text FROM suppliers.attempt_costs WHERE test`) != "1" {
		t.Fatal("the committed retry did not keep one cost and audit")
	}
}
