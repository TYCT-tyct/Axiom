package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/platform/adminapi"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestTestEndpointRefusals: an endpoint this node's snapshot does not hold cannot be tested, and the refusal costs
// nothing: no generation, no cost row, no audit entry.
func TestTestEndpointRefusals(t *testing.T) {
	t.Parallel()
	f := newSupplierFixture(t)
	body := `{"endpoint_id":"` + uuid.NewV7().String() + `","format":"chat","reason":"check the endpoint"}`
	rec := f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, newKey())
	expectAdmin(t, "testSupplierEndpoint", rec, http.StatusConflict, codeConflict)
	if n := f.auditCount("endpoint.test"); n != "0" {
		t.Errorf("%s audit entries of a test that never ran", n)
	}
	body = `{"endpoint_id":"` + uuid.NewV7().String() + `","format":"compaction","reason":"check the endpoint"}`
	rec = f.change(f.s, http.MethodPost, "/suppliers/endpoint-tests", body, newKey())
	expectAdmin(t, "testSupplierEndpoint in a format the contract has not", rec, http.StatusBadRequest, codeInvalidRequest)
}

// TestTheContractNamesEveryProblemATestCanHave: the module's names of what a test found are the contract's enum values,
// so that a result the module describes is one the API can answer.
func TestTheContractNamesEveryProblemATestCanHave(t *testing.T) {
	t.Parallel()
	for _, cause := range suppliers.ProblemCauses() {
		if !adminapi.EndpointTestCause(cause).Valid() {
			t.Errorf("the contract has no cause %q", cause)
		}
	}
	for _, scope := range suppliers.ProblemScopes {
		if !adminapi.EndpointTestProblemScope(scope).Valid() {
			t.Errorf("the contract has no scope %q", scope)
		}
	}
	for _, outcome := range []suppliers.TestOutcome{suppliers.TestPassed, suppliers.TestFailed, suppliers.TestInconclusive} {
		if !adminapi.EndpointTestOutcome(outcome.String()).Valid() {
			t.Errorf("the contract has no outcome %q", outcome)
		}
	}
}

// TestAdminFormats: the contract's names of the call formats and protocol's formats are one table, in the same order,
// so that neither what a request asks for nor what an answer names can be the wrong one.
func TestModelFormatsNamesDirectFormats(t *testing.T) {
	got := modelFormats([]string{"messages", "completions", "embeddings", "rerank"})
	want := []adminapi.ModelFormat{"messages", "completions", "embeddings", "rerank"}
	if len(got) != len(want) {
		t.Fatalf("modelFormats = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("modelFormats[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAdminFormats(t *testing.T) {
	t.Parallel()
	for name, want := range map[adminapi.TestFormat]protocol.Format{
		adminapi.TestFormat("messages"): protocol.Messages, adminapi.TestFormat("responses"): protocol.Responses, adminapi.TestFormat("chat"): protocol.Chat,
	} {
		if got, err := formatOf(name); err != nil || got != want {
			t.Errorf("%s is %v, %v; want %v", name, got, err, want)
		}
		if got := adminFormats[want]; got != name {
			t.Errorf("%v is named %s, want %s", want, got, name)
		}
	}
}
