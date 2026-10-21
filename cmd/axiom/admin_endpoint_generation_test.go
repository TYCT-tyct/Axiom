package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// A disconnect must not roll back a paid generation's claim; a retry replays its recorded result.
func TestAdminEndpointGenerationSurvivesDisconnect(t *testing.T) {
	t.Parallel()
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.Header().Set("X-Oneapi-Request-Id", "admin-test-1")
		close(started)
		<-finish
		_, _ = io.WriteString(rw, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n") //nolint:errcheck // a test server
	}))
	t.Cleanup(up.Close)
	base := newAdminFixture(t)
	f := supplierFixture{startAdminNodeVia(base.commandLine, base.clock, egress.NewForTesting(egresstest.Loopback(t))), base.owner()}
	endpoint := f.seedTestEndpoint(up.URL)
	body := `{"endpoint_id":"` + endpoint + `","format":"chat","reason":"check"}`
	key := newKey()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.do(http.MethodPost, "/suppliers/endpoint-tests", body, as(f.s), idempotent(key), func(r *http.Request) { *r = *r.WithContext(ctx) })
	}()
	<-started
	cancel()
	close(finish)
	rec := <-done
	expectAdmin(t, "disconnected test", rec, http.StatusCreated, "")
	var first, replay adminapi.EndpointTest
	decode(t, rec, &first)
	if first.Outcome != adminapi.Passed || first.FirstTokenMs == nil || first.UpstreamRequestId == nil || *first.UpstreamRequestId != "admin-test-1" {
		t.Errorf("test result: %+v", first)
	}
	rec = f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key)
	expectAdmin(t, "replayed test", rec, http.StatusCreated, "")
	decode(t, rec, &replay)
	if diff := cmp.Diff(first, replay); diff != "" || calls.Load() != 1 || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("replay diff %s; upstream calls %d; headers %v", diff, calls.Load(), rec.Header())
	}
	if f.auditCount("endpoint.test") != "1" || f.text("SELECT count(*)::text FROM suppliers.attempt_costs WHERE test AND site_id IS NULL AND account_id IS NULL") != "1" {
		t.Fatal("the disconnected test did not commit exactly one cost and audit row")
	}
	for range testsPerMinute - 2 {
		f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key)
	}
	expectAdmin(t, "test rate limit", f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, key), http.StatusTooManyRequests, codeRateLimited)
}

// Seed the synchronization-owned projection while exercising credential binding and the API's real call service.
func (f supplierFixture) seedTestEndpoint(origin string) string {
	f.t.Helper()
	supplier := f.connect().Id
	group := f.group(supplier, "1")
	path := "/suppliers/" + supplier
	var selected adminapi.SupplierGroup
	f.send(http.MethodPut, path+"/groups/"+group, `{"selected":true,"reason":"r"}`, http.StatusOK, &selected)
	var bound adminapi.SupplierCredential
	f.send(http.MethodPost, path+"/credentials", `{"group_id":"`+group+`","upstream_key_ref":"key-7","reason":"r"}`, http.StatusCreated, &bound)
	sealed := masterKeys(f.t).Seal(secret.New("endpoint-test-key"), secret.Location{Table: "suppliers.credentials", RowID: bound.Id, Purpose: "key"})
	supplierID, groupID := uuid.MustParse(supplier), uuid.MustParse(group)
	statements := endpointStatements(supplierID, groupID, uuid.NewV7(), "gpt", "chat")
	err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		for _, s := range statements {
			if _, err := tx.Exec(f.t.Context(), s.sql, s.args...); err != nil {
				return err
			}
		}
		_, err := tx.Exec(f.t.Context(), `UPDATE suppliers.credentials SET status = 'active', ciphertext = $1, nonce = $2, wrapped_dek = $3, key_version = $4 WHERE id = $5`, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion), bound.Id)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.run(fmt.Sprintf(`UPDATE suppliers.suppliers SET origin = '%s', status = 'active', balance_native = 100 WHERE id = '%s'`, origin, supplier))
	f.run(fmt.Sprintf(`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ('%s', 1, 'test', 'seed')`, supplier))
	if err := f.upstreams.Reload(f.t.Context()); err != nil {
		f.t.Fatal(err)
	}
	return strings.TrimSpace(f.text("SELECT id::text FROM suppliers.endpoints LIMIT 1"))
}
