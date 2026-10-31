package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// TestCaptureKeepsTheAnswer: a relay given a capture keeps the upstream's content, unquoted and decoded, up to the
// bound; a relay given none keeps nothing, as every gateway call does.
func TestCaptureKeepsTheAnswer(t *testing.T) {
	content := chatRole + chatHead + `[{"index":0,"delta":{"content":"Anthropic \u00e9"},"finish_reason":null}]}` + "\n\n" + chatStop + chatDone
	for name, tc := range map[string]struct {
		stream string
		max    int
		want   string
	}{
		"decoded": {content, 64, "Anthropic é"},
		"bounded": {content, 4, "Anth"},
		"none":    {content, 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			o := options
			o.Memory = protocol.NewMemory(1 << 20)
			o.Capture = &protocol.Capture{Max: tc.max}
			r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(tc.stream), protocol.UsageContext{}, o)
			defer r.Release()
			for {
				step, err := r.Next()
				if err != nil || step.Kind == protocol.StepUpstreamDone {
					break
				}
			}
			if got := string(o.Capture.Text); got != tc.want {
				t.Errorf("captured %q, want %q", got, tc.want)
			}
		})
	}
}
