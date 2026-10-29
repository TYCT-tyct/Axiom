package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestErrorShells: each format answers in its own shell, with the unified error_type where the docs put it; Responses
// before its stream uses Chat's.
func TestErrorShells(t *testing.T) {
	t.Parallel()
	refusal := &Refusal{Status: 402, Code: "credits_insufficient", RetryAfter: 600, Details: map[string]any{"limit_source": "key_limit"}}
	for _, tc := range []struct {
		format protocol.Format
		want   string
	}{
		{protocol.Chat, `{"error":{"code":"credits_insufficient","message":"The credits available are not enough for this request.","metadata":{"error_type":"credits_insufficient","limit_source":"key_limit"},"param":"","type":"billing_error"}}`},
		{protocol.Responses, `{"error":{"code":"credits_insufficient","message":"The credits available are not enough for this request.","metadata":{"error_type":"credits_insufficient","limit_source":"key_limit"},"param":"","type":"billing_error"}}`},
		{protocol.Messages, `{"error":{"error_type":"credits_insufficient","limit_source":"key_limit","message":"The credits available are not enough for this request.","type":"billing_error"},"type":"error"}`},
	} {
		rec := httptest.NewRecorder()
		writeRefusal(rec, tc.format, refusal)
		if rec.Code != 402 || rec.Body.String() != tc.want || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("format %d: %d %s", tc.format, rec.Code, rec.Body)
		}
		if rec.Header().Get("Retry-After") != "60" || rec.Header().Get("Connection") != "" {
			t.Errorf("format %d: Retry-After %q (the longest Claude Code honours is 60), Connection %q",
				tc.format, rec.Header().Get("Retry-After"), rec.Header().Get("Connection"))
		}
	}
}

// TestARefusalIsAnErrorOfProtocol: an error answered before the call and one answered after an attempt are written by two
// functions, and a client must not be able to tell which wrote it: each refusal has the body protocol writes for the
// same error, in every format.
func TestARefusalIsAnErrorOfProtocol(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		refusal *Refusal
		typ     protocol.ErrorType
	}{
		{errUnauthorized, protocol.Authentication},
		{&Refusal{Status: 402, Code: "credits_insufficient"}, protocol.CreditsInsufficient},
		{errKeyRate, protocol.RateLimited},
		{ErrOverloaded, protocol.Overloaded},
		{errNoModel, protocol.ModelNotFound},
		{&Refusal{Status: 404, Code: "no_endpoint"}, protocol.NoEndpoint},
		{&Refusal{Status: 503, Code: "no_endpoint"}, protocol.NoEndpointForNow},
		{invalid("x"), protocol.InvalidRequest},
		{errTooLarge, protocol.RequestTooLarge},
		{errBodyTimeout, protocol.Timeout},
	} {
		for _, f := range []protocol.Format{protocol.Chat, protocol.Responses, protocol.Messages} {
			rec := httptest.NewRecorder()
			r := *tc.refusal
			r.Message = "m"
			writeRefusal(rec, f, &r)
			status, body := protocol.RenderError(f, protocol.Error{Type: tc.typ, Message: "m"}, nil)
			if rec.Code != status {
				t.Errorf("%s, format %d: status %d, protocol's %d", r.Code, f, rec.Code, status)
			}
			var got, want any
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := jsonx.Unmarshal(body, &want); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("%s, format %d (-protocol +refusal):\n%s", r.Code, f, diff)
			}
		}
	}
}

// TestMessagesAvoidAutoDisablePhrases: no text a client reads uses a phrase new-api's channel auto-disable matches, so a
// relay with Axiom as its upstream does not switch the channel off over a billing problem
// (docs/domains/gateway.md「错误」). The list is new-api's, 2026-10, and the codes OpenAI's quota errors carry.
func TestMessagesAvoidAutoDisablePhrases(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"you exceeded your current quota", "your credit balance is too low", "this organization has been disabled",
		"permission denied", "the security token included in the request is invalid", "operation not allowed",
		"your account is not authorized", "insufficient_quota", "billing_hard_limit_reached",
	}
	texts := []string{}
	for code, message := range messages {
		texts = append(texts, code, message)
	}
	for _, r := range []*Refusal{insufficient(&billing.Insufficient{Source: billing.SourceKey}), noEndpoint(&routing.NoEndpoint{Reasons: []routing.Excluded{{Reason: routing.NoPrice}}, Permanent: true})} {
		rec := httptest.NewRecorder()
		writeRefusal(rec, protocol.Chat, r)
		texts = append(texts, rec.Body.String())
	}
	for _, text := range texts {
		for _, phrase := range forbidden {
			if strings.Contains(strings.ToLower(text), phrase) {
				t.Errorf("%q contains %q", text, phrase)
			}
		}
	}
	for _, status := range []int{400, 401, 402, 404, 408, 413, 429, 503} {
		if errorTypes[status] == "" {
			t.Errorf("no Messages type for %d", status)
		}
	}
}

// TestRefusalOfAnError: each failure of the admission path is answered as the docs say; the ones the gateway did not
// expect are 503 and reported.
func TestRefusalOfAnError(t *testing.T) {
	t.Parallel()
	wrapped := func(err error) error { return fmt.Errorf("context: %w", err) }
	for name, tc := range map[string]struct {
		err        error
		status     int
		code       string
		unexpected bool
	}{
		"a refusal, wrapped":                     {wrapped(errAccountGate), 429, "rate_limited", false},
		"a balance too small":                    {wrapped(&billing.Insufficient{Source: billing.SourceBalance}), 402, "credits_insufficient", false},
		"a model that is not there":              {catalog.ErrModelNotFound, 404, "model_not_found", false},
		"a retired model":                        {catalog.ErrRetired, 404, "model_not_found", false},
		"a catalog gone stale":                   {catalog.ErrStale, 503, "overloaded", false},
		"a routing snapshot gone stale":          {routing.ErrStale, 503, "overloaded", false},
		"a reaped holder":                        {billing.ErrTerminated, 503, "overloaded", false},
		"a deadline":                             {wrapped(errors.Join(errors.New("x"), context.DeadlineExceeded)), 503, "overloaded", false},
		"a body Inspect cannot read":             {fmt.Errorf("%w: model is missing", protocol.ErrInvalid), 400, "invalid_request", false},
		"a server tool with no limit":            {wrapped(&billing.UnboundedError{Item: catalog.ItemWebSearch}), 400, "invalid_request", false},
		"a permanent lack of endpoints":          {&routing.NoEndpoint{Reasons: []routing.Excluded{{Reason: routing.NoCandidates}}, Permanent: true}, 404, "no_endpoint", false},
		"a key not allowed the only model asked": {&routing.NoEndpoint{Reasons: []routing.Excluded{{Reason: routing.NotAllowed}}, Permanent: true}, 404, "model_not_found", false},
		"an error nobody expected":               {errors.New("boom"), 503, "overloaded", true},
	} {
		got, expected := refusal(protocol.Chat, tc.err)
		if got.Status != tc.status || got.Code != tc.code || expected == tc.unexpected {
			t.Errorf("%s: %d %s expected=%v, want %d %s", name, got.Status, got.Code, expected, tc.status, tc.code)
		}
	}
	rec := httptest.NewRecorder()
	writeRefusal(rec, protocol.Messages, noEndpoint(&routing.NoEndpoint{
		Reasons: []routing.Excluded{{Reason: routing.BreakerOpen}, {Reason: routing.BreakerOpen}, {Reason: routing.RetryBudget}},
	}))
	var body struct {
		Error struct {
			Reasons []string `json:"reasons"`
		} `json:"error"`
	}
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusServiceUnavailable ||
		strings.Join(body.Error.Reasons, ",") != "breaker_open,retry_budget" || rec.Header().Get("Retry-After") != "1" {
		t.Errorf("a temporary lack of endpoints: %d %s, Retry-After %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
}

// A client is told to write a limit only where its format has a field for one, and the tool is named as the client's
// format calls it; a tool the refusal cannot name is "a server tool".
func TestUnboundedSaysWhatToDo(t *testing.T) {
	t.Parallel()
	unnamed := &billing.UnboundedError{Item: catalog.ItemInput}
	for name, tc := range map[string]struct {
		format protocol.Format
		err    error
		want   string
	}{
		"messages web_search":         {protocol.Messages, &billing.UnboundedError{Item: catalog.ItemWebSearch}, "the web_search tool is enabled without a limit on its uses, and this model has no default one: set max_uses on the tool"},
		"messages code_execution":     {protocol.Messages, &billing.UnboundedError{Item: catalog.ItemCodeExecution}, "the code_execution tool is not supported yet"},
		"responses web_search":        {protocol.Responses, &billing.UnboundedError{Item: catalog.ItemWebSearch}, "the web_search tool is enabled without a limit on its uses, and this model has no default one: set max_tool_calls"},
		"responses code_interpreter":  {protocol.Responses, &billing.UnboundedError{Item: catalog.ItemCodeExecution}, "the code_interpreter tool is enabled without a limit on its uses, and this model has no default one: set max_tool_calls"},
		"chat web_search":             {protocol.Chat, &billing.UnboundedError{Item: catalog.ItemWebSearch}, "the web_search_options setting is not supported yet"},
		"messages, no tool named":     {protocol.Messages, billing.ErrUnbounded, "a server tool is not supported yet"},
		"responses, no tool named":    {protocol.Responses, billing.ErrUnbounded, "a server tool is enabled without a limit on its uses, and this model has no default one: set max_tool_calls"},
		"chat, no tool named":         {protocol.Chat, billing.ErrUnbounded, "a server tool is not supported yet"},
		"an item with no client name": {protocol.Responses, unnamed, "a server tool is enabled without a limit"},
	} {
		r, expected := refusal(tc.format, fmt.Errorf("entry 0: %w", tc.err))
		if !expected || r.Status != http.StatusBadRequest || !strings.Contains(r.Message, tc.want) {
			t.Errorf("%s: %d %q, want 400 containing %q", name, r.Status, r.Message, tc.want)
		}
	}
}

func TestAccountSuspensionErrorEnvelopes(t *testing.T) {
	t.Parallel()
	for _, format := range []protocol.Format{protocol.Chat, protocol.Responses, protocol.Messages} {
		r, expected := refusal(format, accounts.ErrAccountSuspended)
		if !expected || r.Status != http.StatusForbidden {
			t.Fatalf("refusal: %+v %v", r, expected)
		}
		rec := httptest.NewRecorder()
		writeRefusal(rec, format, r)
		var body map[string]any
		if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		detail := body["error"].(map[string]any)
		if rec.Code != 403 || detail["type"] != "permission_error" {
			t.Fatal(rec.Code, body)
		}
		if format != protocol.Messages && detail["code"] != float64(403) {
			t.Fatal("not OpenRouter compatible", body)
		}
	}
}
