package gateway

import (
	"os"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

// sampleDirs are the samples' directories, one per format.
var sampleDirs = map[protocol.Format]string{protocol.Messages: "messages", protocol.Responses: "responses", protocol.Chat: "chat"}

// sample reads the recorded stream of format f and the request it answers.
func sample(t *testing.T, f protocol.Format) (events []string, request []byte) {
	t.Helper()
	// The directory comes from a fixed map, never from a request: a test fixture, not a file inclusion.
	dir := "../protocol/testdata/" + sampleDirs[f] + "/text/"
	stream, err := os.ReadFile(dir + "upstream.sse") //nolint:gosec // a test fixture path from a fixed map
	if err != nil {
		t.Fatal(err)
	}
	request, err = os.ReadFile(dir + "request.json") //nolint:gosec // likewise
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range strings.SplitAfter(strings.TrimSpace(string(stream))+"\n\n", "\n\n") {
		if e != "" {
			events = append(events, e)
		}
	}
	return events, request
}
