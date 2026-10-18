package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		{protocol.Chat, `{"error":{"code":402,"message":"The credits available are not enough for this request.","metadata":{"error_type":"credits_insufficient","limit_source":"key_limit"},"type":"billing_error"}}`},
		{protocol.Responses, `{"error":{"code":402,"message":"The credits available are not enough for this request.","metadata":{"error_type":"credits_insufficient","limit_source":"key_limit"},"type":"billing_error"}}`},
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
		"a refusal, wrapped":                     {wrapped(ErrRateLimited), 429, "rate_limited", false},
		"a balance too small":                    {wrapped(&billing.Insufficient{Source: billing.SourceBalance}), 402, "credits_insufficient", false},
		"a model that is not there":              {catalog.ErrModelNotFound, 404, "model_not_found", false},
		"a retired model":                        {catalog.ErrRetired, 404, "model_not_found", false},
		"a catalog gone stale":                   {catalog.ErrStale, 503, "overloaded", false},
		"a routing snapshot gone stale":          {routing.ErrStale, 503, "overloaded", false},
		"a reaped holder":                        {billing.ErrTerminated, 503, "overloaded", false},
		"a deadline":                             {wrapped(errors.Join(errors.New("x"), context.DeadlineExceeded)), 503, "overloaded", false},
		"a body Inspect cannot read":             {fmt.Errorf("%w: model is missing", protocol.ErrInvalid), 400, "invalid_request", false},
		"a server tool with no limit":            {wrapped(fmt.Errorf("%w: web_search", billing.ErrUnbounded)), 400, "invalid_request", false},
		"a permanent lack of endpoints":          {&routing.NoEndpoint{Reasons: []routing.Excluded{{Reason: routing.NoCandidates}}, Permanent: true}, 404, "no_endpoint", false},
		"a key not allowed the only model asked": {&routing.NoEndpoint{Reasons: []routing.Excluded{{Reason: routing.NotAllowed}}, Permanent: true}, 404, "model_not_found", false},
		"an error nobody expected":               {errors.New("boom"), 503, "overloaded", true},
	} {
		got, expected := refusal(tc.err)
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
