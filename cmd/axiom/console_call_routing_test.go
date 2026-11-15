package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
)

func TestCallAttemptHTTP(t *testing.T) {
	t.Parallel()
	c := newCallsFixture(t)
	call := newCall(c.mid.Account, c.now, "model", "cli")
	call.Route = []byte(`{"preset":{"id":"preset-id","version":7},"excluded":[{"endpoint":"internal-endpoint","reason":"capacity"}],"excluded_reasons":{"capacity":40},"excluded_more":24,"route":["internal-endpoint"],"fastest_unmeasured":true,"egress":"private-egress","supplier":"private-supplier","credentials":"private-secret","content":"private-conversation"}`)
	for i, seq := range []platform.AttemptSeq{1, 2, 3} {
		a := observability.AttemptRecord{Seq: seq, Endpoint: uuid.NewV7(), Supplier: uuid.NewV7(), SourceClass: "relay", Egress: "private-egress", StartedAt: call.At.Add(time.Duration(i) * time.Second), FinishedAt: call.At.Add(time.Duration(i+1) * time.Second), FirstToken: 100 * time.Millisecond, Outcome: observability.AttemptFailed, ErrorKind: "timeout"}
		if i == 2 {
			a.Outcome, a.ErrorKind = observability.AttemptCommitted, ""
		}
		call.Attempts = append(call.Attempts, a)
	}
	c.f.record(call)
	rec := c.f.do(http.MethodGet, "/calls/"+call.ID.String(), "", as(c.s))
	expect(t, "attempt detail", rec, http.StatusOK, "")
	var got consoleapi.Call
	decode(t, rec, &got)
	checkCallAttempts(t, got, call)
	for _, secret := range []string{"internal-endpoint", "private-egress", "private-supplier", "private-secret", "private-conversation", call.Attempts[0].Endpoint.String(), call.Attempts[0].Supplier.String()} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response leaked %q", secret)
		}
	}
	var list consoleapi.CallList
	decode(t, c.f.do(http.MethodGet, "/calls?fell_back=true&limit=1", "", as(c.s)), &list)
	if len(list.Data) != 1 {
		t.Fatalf("list: %+v", list)
	}
	if diff := cmp.Diff([]any{call.ID.String(), new(3), new(true)}, []any{list.Data[0].Id, list.Data[0].AttemptCount, list.FellBackFilter}); diff != "" {
		t.Fatalf("filtered list: %s", diff)
	}
	got = consoleapi.Call{}
	decode(t, c.f.do(http.MethodGet, "/calls/"+c.old.ID.String(), "", as(c.s)), &got)
	if got.Attempts == nil || len(*got.Attempts) != 0 || got.Route != nil {
		t.Fatalf("legacy detail: %+v", got)
	}
}

func checkCallAttempts(t *testing.T, got consoleapi.Call, call observability.CallRecord) {
	t.Helper()
	if got.Attempts == nil || len(*got.Attempts) != 3 {
		t.Fatalf("attempts: %+v", got.Attempts)
	}
	for i, a := range *got.Attempts {
		original := call.Attempts[i]
		want := consoleapi.CallAttempt{Sequence: new(i + 1), SourceClass: new(original.SourceClass), StartedAt: &original.StartedAt, FinishedAt: &original.FinishedAt, FirstTokenMs: new(100), Result: new(consoleapi.CallAttemptResult(original.Outcome)), ErrorKind: new(original.ErrorKind), Charged: new(i == 2)}
		if diff := cmp.Diff(want, a); diff != "" {
			t.Fatalf("attempt (-want +got): %s", diff)
		}
	}
	if got.Route == nil {
		t.Fatal("no route")
	}
	if diff := cmp.Diff(new(24), got.Route.ExcludedMore); diff != "" {
		t.Fatalf("excluded more: %s", diff)
	}
}

func TestAttemptChargeAttribution(t *testing.T) {
	for _, tc := range []struct {
		name      string
		price     int64
		endpoints []string
		want      []*bool
	}{
		{"fallback", 1, []string{"a", "b", "c"}, []*bool{new(false), new(false), new(true)}},
		{"zero charge", 0, []string{"a", "b"}, []*bool{new(false), new(false)}},
		{"loop attribution unknown", 1, []string{"a", "b", "b"}, []*bool{new(false), nil, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := observability.OwnCall{PriceNano: tc.price}
			for _, e := range tc.endpoints {
				call.Attempts = append(call.Attempts, observability.OwnAttempt{Endpoint: e})
			}
			for i, want := range tc.want {
				got := attemptCharged(call, i, tc.price > 0)
				if diff := cmp.Diff(want, got); diff != "" {
					t.Fatalf("attempt %d charged (-want +got): %s", i, diff)
				}
			}
		})
	}
}

func TestOwnRouteJSONAllowlist(t *testing.T) {
	body := consoleapi.Call{}
	call := observability.OwnCall{Route: &observability.OwnRoute{Preset: &observability.OwnRoutePreset{ID: "p", Version: 2}, SelectedEndpoint: "internal"}}
	(consoleAPI{}).callRoutingBody(call, &body, false)
	raw, err := jsonx.MarshalCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "internal") {
		t.Fatalf("internal ID exposed: %s", raw)
	}
}
