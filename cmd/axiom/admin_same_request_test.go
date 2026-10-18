package main

import (
	"net/http"
	"os"
	"testing"

	"axiom/internal/platform/jsonx"
)

// TestSameRequestUnderAKey: the cases of internal/platform/idem/testdata/same-request.json that name an operation,
// sent through its handler. That file is shared with the admin's end-to-end mock (web/e2e/fingerprint.spec.ts), so the
// mock's idea of the same request under one key cannot drift from the handlers': a login's secret is not part of it,
// an origin is compared as normalised.
func TestSameRequestUnderAKey(t *testing.T) {
	data, err := os.ReadFile("../../internal/platform/idem/testdata/same-request.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Case, Operation, First, Again string
		Same                          bool
	}
	if err := jsonx.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.Operation == "" {
			continue
		}
		t.Run(c.Case, func(t *testing.T) {
			f := newSupplierFixture(t)
			method, path, status := http.MethodPost, "/suppliers", http.StatusCreated
			if c.Operation == "replaceSupplierLogin" {
				made := f.connect()
				f.run("UPDATE suppliers.suppliers SET status = 'active'") // as the sync leaves it
				method, path, status = http.MethodPut, "/suppliers/"+made.Id+"/login", http.StatusOK
			}
			key := newKey()
			expectAdmin(t, "the first request", f.change(f.s, method, path, c.First, key), status, "")
			rec := f.change(f.s, method, path, c.Again, key)
			if c.Same {
				if expectAdmin(t, "the same request again", rec, status, ""); rec.Header().Get("Idempotency-Replayed") != "true" {
					t.Error("not a replay")
				}
				return
			}
			expectAdmin(t, "another request under the key", rec, http.StatusConflict, codeIdempotencyReuse)
		})
	}
}
