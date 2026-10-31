package gateway

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/synctest"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// bodyObject is a JSON object.
func bodyObject(t *testing.T, body string) map[string]any {
	t.Helper()
	m, ok := decode(t, body).(map[string]any)
	if !ok {
		t.Fatalf("not an object:\n%s", body)
	}
	return m
}

// objectOf is a member that is an object, or nil.
func objectOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// listOf is a member that is an array, or nil.
func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

// fixture reads a recorded sample of the protocol package.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../protocol/testdata/" + name) //nolint:gosec // a test fixture path from the test
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// jsonReply is an endpoint answering one JSON body.
func jsonReply(status int, body []byte) scripted { //nolint:unparam // the helper mirrors the upstream API; tests currently only use 200
	return func(context.Context) (*http.Response, *suppliers.Failure) {
		return &http.Response{
			StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(string(body))),
		}, nil
	}
}

// usage is a settlement's usage of the items, in the standard tier.
func usage(t *testing.T, counts map[catalog.Item]int64) catalog.Usage {
	t.Helper()
	u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestExecutorEmbeddings: an embeddings call is answered whole, after the settlement: the reply keeps the upstream's
// data, and Axiom's model and usage, with the cost. The upstream is asked in its own format, with the model rewritten.
//
//nolint:cyclop // one fallback flow through two entries is the test
func TestExecutorEmbeddings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request, response := fixture(t, "embeddings/text/request.json"), fixture(t, "embeddings/text/response.json")
		h := newHarness(t, context.Background(), options{
			client: protocol.Embeddings, request: request, entries: 1,
			// An embeddings model has no output price; a direct quote must not demand one.
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001"}}`,
			script: map[int][]scripted{1: {jsonReply(http.StatusOK, response)}},
			final:  billing.Final{Segments: []billing.Segment{{Usage: usage(t, map[catalog.Item]int64{catalog.ItemInput: 8})}}},
		})
		h.run()

		body := bodyObject(t, h.w.body())
		if body["object"] != "list" || body["model"] != "openai/text-embedding-3-small" || len(listOf(body["data"])) != 1 {
			t.Errorf("reply:\n%s", h.w.body())
		}
		if u := objectOf(body["usage"]); u["prompt_tokens"] != 8.0 || u["total_tokens"] != 8.0 || u["cost"] == nil {
			t.Errorf("usage %v:\n%s", u, h.w.body())
		}
		if p := h.ended().body; p != "" {
			// The reply is written after the settlement: at settlement time the client has seen nothing.
			t.Errorf("the reply was written before the settlement:\n%s", p)
		}
		call := h.up.endpoint(1).calls[0]
		if call.Format != protocol.Embeddings || !strings.Contains(string(call.Body), `"up-1"`) {
			t.Errorf("upstream call %v:\n%s", call.Format, call.Body)
		}
		a := h.ended().outcome.Attempts
		if len(a) != 1 || a[0].Result != billing.Aggregated || a[0].Segments[0].Usage.Count(catalog.ItemInput) != 8 {
			t.Errorf("outcome %+v", a)
		}
	})
}

// TestExecutorRerank: rerank searches are billed as web-search units beside the input tokens the upstream reports, and
// the reply is the upstream's results with our usage.
func TestExecutorRerank(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request, response := fixture(t, "rerank/text/request.json"), fixture(t, "rerank/text/response.json")
		u := usage(t, map[catalog.Item]int64{catalog.ItemInput: 150, catalog.ItemWebSearch: 1})
		h := newHarness(t, context.Background(), options{
			client: protocol.Rerank, request: request, entries: 1,
			// A rerank model has no output price either; its search unit is a flat web_search charge.
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","web_search":"0.002"}}`,
			script: map[int][]scripted{1: {jsonReply(http.StatusOK, response)}},
			final:  billing.Final{Segments: []billing.Segment{{Usage: u}}},
		})
		h.run()
		body := bodyObject(t, h.w.body())
		got := objectOf(body["usage"])
		if got["total_tokens"] != 150.0 || got["search_units"] != 1.0 || got["cost"] == nil {
			t.Errorf("usage %v:\n%s", got, h.w.body())
		}
		if body["id"] != h.call.ID.String() || len(listOf(body["results"])) != 1 {
			t.Errorf("reply:\n%s", h.w.body())
		}
		if a := h.ended().outcome.Attempts; a[0].Segments[0].Usage.Count(catalog.ItemWebSearch) != 1 {
			t.Errorf("outcome %+v", a)
		}
	})
}

// TestExecutorCompletions: a legacy completions call is a stream the client did not ask for, so it is aggregated into
// one text completion, with the usage and cost of the settlement.
func TestExecutorCompletions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := []byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"Say this is a test","max_tokens":7,"stream":false}`)
		stream := string(fixture(t, "completions/text/upstream.sse"))
		h := newHarness(t, context.Background(), options{
			client: protocol.Completions, request: request, entries: 1,
			script: map[int][]scripted{1: {serve(beat{data: stream})}},
			final:  billing.Final{Segments: []billing.Segment{{Usage: usage(t, map[catalog.Item]int64{catalog.ItemInput: 4, catalog.ItemOutput: 7})}}},
		})
		h.run()
		body := bodyObject(t, h.w.body())
		choices := listOf(body["choices"])
		if body["object"] != "text_completion" || len(choices) != 1 || objectOf(choices[0])["text"] != "Say this is" ||
			objectOf(choices[0])["finish_reason"] != "length" {
			t.Errorf("reply:\n%s", h.w.body())
		}
		if u := objectOf(body["usage"]); u["prompt_tokens"] != 4.0 || u["completion_tokens"] != 7.0 || u["cost"] == nil {
			t.Errorf("usage %v:\n%s", u, h.w.body())
		}
		if call := h.up.endpoint(1).calls[0]; call.Format != protocol.Completions || !strings.Contains(string(call.Body), `"stream":true`) {
			t.Errorf("upstream call %v:\n%s", call.Format, call.Body)
		}
	})
}

// TestExecutorDirectFailure: a reply without the member its format answers with is the upstream breaking the format
// before the commit point, so the call retries the next entry; when none is left the client gets the documented
// error, not half a reply.
func TestExecutorDirectFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := fixture(t, "embeddings/text/request.json")
		h := newHarness(t, context.Background(), options{
			client: protocol.Embeddings, request: request, entries: 2,
			script: map[int][]scripted{
				1: {jsonReply(http.StatusOK, []byte(`{"object":"list","usage":{"prompt_tokens":1}}`))},
				2: {jsonReply(http.StatusOK, fixture(t, "embeddings/text/response.json"))},
			},
			final: billing.Final{Segments: []billing.Segment{{Usage: usage(t, map[catalog.Item]int64{catalog.ItemInput: 8})}}},
		})
		h.run()
		if h.w.status != http.StatusOK || len(h.up.endpoint(1).calls) != 1 || len(h.up.endpoint(2).calls) != 1 {
			t.Errorf("status %d, calls %d and %d", h.w.status, len(h.up.endpoint(1).calls), len(h.up.endpoint(2).calls))
		}
		if !strings.Contains(h.w.body(), `"data"`) {
			t.Errorf("the client did not get the second entry's reply:\n%s", h.w.body())
		}
	})
}

// TestExecutorDirectReportsServedModel: the reply's model is the one the attempt actually served, not the alias the
// client asked for. A direct reply is rewritten after the settlement, so it must read the entry's model slug.
func TestExecutorDirectReportsServedModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request, response := fixture(t, "embeddings/text/request.json"), fixture(t, "embeddings/text/response.json")
		h := newHarness(t, context.Background(), options{
			client: protocol.Embeddings, request: request, entries: 1,
			script: map[int][]scripted{1: {jsonReply(http.StatusOK, response)}},
			final:  billing.Final{Segments: []billing.Segment{{Usage: usage(t, map[catalog.Item]int64{catalog.ItemInput: 8})}}},
			mutate: func(c *Call) {
				c.Request.Model = "alias/text-embedding"
				c.Plan.Entries[0].ModelSlug = "real/text-embedding"
			},
		})
		h.run()
		if got := bodyObject(t, h.w.body())["model"]; got != "real/text-embedding" {
			t.Errorf("reply model = %v, want the served model", got)
		}
	})
}

// TestExecutorDirectRewriteRespectsMemory: the rewritten reply is a second copy while the upstream's body is still held.
// The node's budget is asked before it is allocated; when it cannot hold both, the client gets 503 instead of the node
// overcommitting.
func TestExecutorDirectRewriteRespectsMemory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		request := fixture(t, "embeddings/text/request.json")
		var data strings.Builder
		data.WriteString(`{"object":"list","data":[`)
		for i := range 2000 {
			if i > 0 {
				data.WriteByte(',')
			}
			data.WriteString(`{"embedding":[0.1],"index":0}`)
		}
		data.WriteString(`],"model":"upstream","usage":{"prompt_tokens":8,"total_tokens":8}}`)
		h := newHarness(t, context.Background(), options{
			client: protocol.Embeddings, request: request, entries: 1, memory: 160 << 10,
			price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001"}}`,
			script: map[int][]scripted{1: {jsonReply(http.StatusOK, []byte(data.String()))}},
			final:  billing.Final{Segments: []billing.Segment{{Usage: usage(t, map[catalog.Item]int64{catalog.ItemInput: 8})}}},
		})
		h.run()
		if h.w.status != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503 when the rewritten copy does not fit: %s", h.w.status, h.w.body())
		}
	})
}
