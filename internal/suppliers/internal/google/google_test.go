package google_test

import (
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/adaptertest"
	"axiom/internal/suppliers/internal/google"
)

// TestGoogleIsCalledThroughItsOpenAILayer: Google serves the OpenAI formats under /v1beta/openai, not /v1.
func TestGoogleIsCalledThroughItsOpenAILayer(t *testing.T) {
	t.Parallel()
	a := google.New()
	adaptertest.Run(t, a)
	if got := a.Calls().(adapter.Wire).Path(protocol.Chat, "/v1/chat/completions"); got != "/v1beta/openai/chat/completions" {
		t.Errorf("a Chat call goes to %s", got)
	}
}
