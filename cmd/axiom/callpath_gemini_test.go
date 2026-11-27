package main

import (
	"bytes"
	"net/http"
	"strings"
)

// geminiToolCall is the matrix's question as a Gemini SDK sends it, with the same two functions declared.
const geminiToolCall = `{"contents":[{"role":"user","parts":[{"text":"Weather and time in Paris?"}]}],"tools":[{"functionDeclarations":[` +
	`{"name":"get_weather","description":"Weather","parametersJsonSchema":{"type":"object","properties":{"location":{"type":"string"}}}},` +
	`{"name":"get_time","description":"Time","parametersJsonSchema":{"type":"object","properties":{}}}]}]}`

// checkGeminiToolCall: a Gemini client reaches the model on the node's public listener, at the Gemini SDK's own path,
// gets the upstream's first call as a functionCall and is billed for the upstream's tokens; streamed, the last event
// carries the candidate with the usage, as Google's own stream does and the Google SDKs require.
func (w *callWorld) checkGeminiToolCall(n runningNode, up toolUpstream, streamed bool) {
	t := w.t
	t.Helper()
	path := "/v1beta/models/" + up.model + ":generateContent"
	if streamed {
		path = "/v1beta/models/" + up.model + ":streamGenerateContent?alt=sse"
	}
	r := w.call(n, path, w.richKey, geminiToolCall)
	if r.status != http.StatusOK {
		t.Fatalf("Gemini: status %d: %s", r.status, r.body)
	}
	if !bytes.Contains(r.body, []byte(`"functionCall":{`)) || !bytes.Contains(r.body, []byte(`"name":"`+up.calls[0].name+`"`)) {
		t.Errorf("the Gemini answer lacks the call %+v: %s", up.calls[0], r.body)
	}
	if streamed {
		events := strings.Split(strings.TrimSpace(string(r.body)), "\n\n")
		last := events[len(events)-1]
		if !strings.Contains(last, `"candidates":[`) || !strings.Contains(last, `"usageMetadata":`) || !strings.Contains(last, `"finishReason":"STOP"`) {
			t.Errorf("the last Gemini event does not carry the candidate, its finish and the usage: %s", last)
		}
	}
	up.booked.ref = w.lastRef()
	w.checkBooked(r, up.booked)
}
