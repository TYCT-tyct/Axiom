package gateway

import (
	"bytes"
	"log/slog"
	"testing"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

func TestAttemptCountsOnlyMissingInput(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"alias","messages":[{"role":"user","content":"お誕生日おめでとう"}]}`)
	req, err := protocol.Inspect(protocol.Chat, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, model string
		reported    catalog.ItemSet
		invalid     bool
		counted     bool
	}{
		{"endpoint model selects the tokenizer", "openai/gpt-4o", 0, false, true},
		{"a Claude endpoint estimates from the shape", "claude-sonnet-4-5", 0, false, true},
		{"a name of no family keeps the byte rule", "deployment-name", 0, false, false},
		{"reported input", "gpt-4o", 1 << catalog.ItemInput, false, false},
		{"reported cache", "gpt-4o", 1 << catalog.ItemCacheRead, false, false},
		{"reported output only", "gpt-4o", 1 << catalog.ItemOutput, false, true},
		{"extraction failure", "gpt-4o", 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := &Call{Format: protocol.Chat, Body: body, Request: req}
			if tc.invalid {
				c.Body = []byte(`{"model":`)
			}
			a := attempt{
				s: &session{c: c, logger: slog.New(slog.NewTextHandler(&logs, nil))},
				entry: routing.Entry{
					Target: protocol.Target{Model: tc.model}, Limits: billing.Model{ContextLength: 1000},
				},
				usage: protocol.Usage{Segments: []protocol.Segment{{Reported: tc.reported}}},
			}
			d := a.demand()
			if (d.TextTokens > 0) != tc.counted || d.TextBytes != req.TextBytes {
				t.Fatalf("unexpected demand: %+v", d)
			}
			if (logs.Len() > 0) != tc.invalid {
				t.Fatalf("unexpected warning: %s", logs.String())
			}
		})
	}
}
