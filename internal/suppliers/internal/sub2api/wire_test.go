package sub2api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
)

func TestQuotedChargeNumbersAreNotMissingUsage(t *testing.T) {
	t.Parallel()
	for _, field := range []string{`"input_tokens":""`, `"input_tokens":"7"`, `"actual_cost":"0.5"`} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			body := `{"request_id":"r","api_key_id":1,"created_at":"2026-10-28T00:00:00Z","actual_cost":0.5,"billing_type":0,"billing_mode":"token",` + field + `}`
			if strings.HasPrefix(field, `"actual_cost"`) {
				body = strings.Replace(body, `"actual_cost":0.5,`, "", 1)
			}
			var item usageItem
			if err := decode([]byte(body), &item); err != nil {
				t.Fatal(err)
			}
			if _, _, err := item.charge(); !errors.Is(err, adapter.ErrUnrecognised) {
				t.Fatalf("quoted numeric field was accepted: %v", err)
			}
		})
	}
}

func TestOfferPositionsKeepNumericValues(t *testing.T) {
	t.Parallel()
	srv := newServer(func(r adapter.Request) (adapter.Response, error) { return manyGroups(2)(r), nil })
	m := manager0(t, srv, login(t, epoch.Add(24*time.Hour)))
	p, err := m.(*manager).startOffer(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Keys = map[string]number{"1": "7"}
	p.Overrides = map[string]number{"1": "1.25"}
	raw := listing(p)
	var back offerProgress
	if err := jsonx.UnmarshalKnown(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !sameGroups(p.Groups, back.Groups) || back.Keys["1"] != "7" || back.Overrides["1"] != "1.25" {
		t.Fatalf("numeric values changed: %s", raw)
	}
}
