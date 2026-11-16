package gateway

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/protocol"
)

func TestSearchFailureLogsOneRequestID(t *testing.T) {
	for name, loop := range map[string]bool{"plugin": false, "server tool": true} {
		t.Run(name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				var logs bytes.Buffer
				upstream := protocol.Chat
				o := options{
					client: protocol.Chat, upstream: &upstream, logs: &logs,
					price:   `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001","web_search":"0.01"}}`,
					request: []byte(`{"model":"acme/alpha","messages":[{"role":"user","content":"hello"}],"plugins":[{"id":"web"}]}`),
					search:  &recordingSearcher{err: errors.New("search failed")},
				}
				if loop {
					o.request = []byte(searchTool)
					o.server = &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2}
					o.script = map[int][]scripted{1: {serve(beat{data: chatToolCall})}}
				}
				h := newHarness(t, ctx, o)
				h.run()
				if h.w.status != http.StatusBadGateway {
					t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
				}
				_, failure, found := strings.Cut(logs.String(), "the web search backend failed")
				line, _, _ := strings.Cut(failure, "\n")
				if !found || strings.Count(line, "request_id=") != 1 || !strings.Contains(line, "request_id="+h.call.ID.String()) {
					t.Fatalf("want one request ID on search failure: %s", &logs)
				}
			})
		})
	}
}
