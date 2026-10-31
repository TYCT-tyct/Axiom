package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

func TestModelDetailVariants(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, prefix := range []string{"/v1/models/", "/api/v1/models/"} {
		for _, tc := range []struct {
			suffix string
			status int
		}{
			{":nitro", 200},
			{":floor:nitro", 200},
			{":exacto", 200},
			{":free", 404},
			{":thinking", 200},
			{":online", 200},
			{":unknown", 404},
		} {
			path := prefix + "acme/alpha" + tc.suffix
			resp, body := r.do(http.MethodGet, path, a.key, nil)
			if resp.StatusCode != tc.status {
				t.Errorf("%s: %d %s, want %d", path, resp.StatusCode, body, tc.status)
			}
			id := "acme/alpha"
			if prefix == "/v1/models/" {
				id += tc.suffix
			}
			if tc.status == http.StatusOK && !strings.Contains(body, `"id":"`+id+`"`) {
				t.Errorf("%s: wrong resolved model: %s", path, body)
			}
		}
	}
}

func TestModelEndpointVariantsAndVisibility(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"acme/alpha:nitro", 200}, {"acme/alpha:free", 404}, {"~acme/alpha-latest", 404},
	} {
		resp, body := r.do(http.MethodGet, "/api/v1/models/"+tc.name+"/endpoints", a.key, nil)
		if resp.StatusCode != tc.status {
			t.Errorf("endpoints %s: %d %s", tc.name, resp.StatusCode, body)
		}
	}
	// A supported routing suffix cannot bypass a key's model permissions.
	restrict(r, a, "{acme/beta}")
	for _, prefix := range []string{"/v1/models/", "/api/v1/models/"} {
		resp, body := r.do(http.MethodGet, prefix+"acme/alpha:nitro", a.key, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("hidden model: %d %s", resp.StatusCode, body)
		}
	}
}

func TestHTTPRequestErrorVocabulary(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
		for _, tc := range []struct {
			failure suppliers.Failure
			code    string
			status  int
		}{
			{suppliers.Failure{Cause: suppliers.ContextTooLong{}}, "context_length_exceeded", 400},
			{suppliers.Failure{Cause: suppliers.ContentRejected{}}, "content_policy_violation", 403},
			{suppliers.Failure{Cause: suppliers.BadRequest{}}, "invalid_request", 400},
			{suppliers.Failure{Cause: suppliers.BadRequest{}, Rejection: &protocol.Rejection{Message: "request rejected", Code: "context_length_exceeded"}}, "context_length_exceeded", 400},
			{suppliers.Failure{Cause: suppliers.BadRequest{}, Rejection: &protocol.Rejection{Message: "request rejected", Code: "content_policy_violation", Type: "invalid_request_error"}}, "content_policy_violation", 403},
			{suppliers.Failure{Cause: suppliers.BadRequest{}, Rejection: &protocol.Rejection{Message: "request rejected", Code: "permission_denied"}}, "permission_denied", 403},
			{suppliers.Failure{Cause: suppliers.BadRequest{}, Rejection: &protocol.Rejection{Message: "request rejected", Code: "413"}}, "payload_too_large", 413},
		} {
			run(t, func(t *testing.T, ctx context.Context) {
				h := newHarness(t, ctx, options{client: f, entries: 2})
				h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) {
					return nil, failing(routing.ScopeRequest, true, tc.failure)
				}}
				h.run()
				if h.w.status != tc.status || !strings.Contains(h.w.body(), `"error_type":"`+tc.code+`"`) {
					t.Errorf("%v: %d %s", tc.failure.Cause, h.w.status, h.w.body())
				}
				if h.up.endpoint(2).sends != 0 {
					t.Fatal("retried a request rejection")
				}
			})
		}
	}
}
