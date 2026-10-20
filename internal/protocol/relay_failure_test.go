package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/protocol/internal/streamcheck"
)

// The streams of the failure tests: what an upstream says when it ends the stream with its own failure, in the shape of
// OpenAI's Responses events (testdata/responses/text).

// responsesFailureStream builds a stream that commits the call and then ends with what end says: the response being
// created, with a usage object of createdUsage, an output item with its part, and a piece of its text.
func responsesFailureStream(createdUsage string, end ...string) string {
	stream := responsesEvent("response.created", 0, strings.Replace(responsesObject, `"usage":null`, `"usage":`+createdUsage, 1))
	stream += responsesEvent("response.output_item.added", 1,
		`,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}`)
	stream += responsesEvent("response.content_part.added", 2,
		`,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}`)
	stream += responsesEvent("response.output_text.delta", 3, `,"output_index":0,"content_index":0,"item_id":"msg_1","delta":"Hello"`)
	return stream + strings.Join(end, "")
}

// responsesFailed is the frame that ends the stream with the upstream's own failure, and the usage of the attempt.
func responsesFailed(seq int, usage string) string {
	return responsesEvent("response.failed", seq, `,"response":{"id":"resp_1","object":"response","status":"failed",`+
		`"model":"gpt-upstream","output":[],"error":{"code":"server_error","message":"boom"},"usage":`+usage+`}`)
}

// responsesErrorEvent is the bare error event, which may come before the failed response or alone.
func responsesErrorEvent(seq int, code, message, usage string) string {
	return responsesEvent("error", seq, `,"code":"`+code+`","param":"input","message":"`+message+`","usage":`+usage)
}

// TestRelayResponsesFailureUsage: the usage of the frame that ends the stream with the upstream's failure is the last
// word on the attempt, so billing charges those items as it states them rather than estimating them from the bytes
// relayed; an item it does not state stays estimated, which a count reported earlier only bounds from below
// (docs/domains/billing.md「用量来源与暂估」).
func TestRelayResponsesFailureUsage(t *testing.T) {
	for _, c := range []struct {
		name                    string
		stream                  string
		input, output           int64
		inputFinal, outputFinal bool
	}{
		{
			"the failure frame after the content",
			responsesFailureStream("null", responsesFailed(4, `{"input_tokens":5,"output_tokens":11}`)),
			5, 11, true, true,
		},
		{
			"the error event's own usage",
			responsesFailureStream("null", responsesErrorEvent(4, "server_error", "boom", `{"input_tokens":5,"output_tokens":11}`)),
			5, 11, true, true,
		},
		{
			"an earlier count only bounds the output",
			responsesFailureStream(`{"output_tokens":4}`, responsesFailed(4, `{"input_tokens":5}`)),
			5, 4, true, false,
		},
		{
			"the stream ends after the error event",
			responsesFailureStream("null", responsesErrorEvent(4, "rate_limit_exceeded", "slow down", "null")),
			0, 0, false, false,
		},
		{
			"two failure frames",
			responsesFailureStream("null", responsesFailed(4, `{"input_tokens":5,"output_tokens":11}`),
				responsesFailed(5, `{"input_tokens":5,"output_tokens":11}`)),
			5, 11, true, true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, protocol.Responses, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if !errors.Is(got.err, protocol.ErrUpstream) {
				t.Fatalf("error %v", got.err)
			}
			checkFailureUsage(t, got, c.input, c.output, c.inputFinal, c.outputFinal)
		})
	}
}

// checkFailureUsage is the usage the attempt is charged for after the failure: the counts, and which of them the
// upstream's last word stated.
func checkFailureUsage(t *testing.T, got relayed, input, output int64, inputFinal, outputFinal bool) {
	t.Helper()
	reply := got.r.Usage().Segments[0]
	if reply.Usage.Count(catalog.ItemInput) != input || reply.Usage.OutputTotal() != output {
		t.Errorf("input %d, output %d; want %d, %d", reply.Usage.Count(catalog.ItemInput), reply.Usage.OutputTotal(), input, output)
	}
	if reply.Reported.Has(catalog.ItemInput) != inputFinal {
		t.Errorf("input reported %t, want %t", reply.Reported.Has(catalog.ItemInput), inputFinal)
	}
	if reply.Reported.Has(catalog.ItemOutput) != outputFinal {
		t.Errorf("output reported %t, want %t", reply.Reported.Has(catalog.ItemOutput), outputFinal)
	}
}

// TestRelayResponsesFailureMeaning: the frame that ended the stream says how it failed, through the error the read fails
// with and through the ending: the upstream's own code and words when its error event named them, and the failure frame
// after a bare error event rather than the event itself, or the event when the stream ends after it
// (docs/domains/gateway.md「错误」).
func TestRelayResponsesFailureMeaning(t *testing.T) {
	for _, c := range []struct {
		name               string
		stream             string
		fault              protocol.Fault
		last               string // the ending's type
		code, param, words string // its error's members, as the upstream named them
	}{
		{
			"the failure frame after the content",
			responsesFailureStream("null", responsesFailed(4, "null")),
			protocol.FaultServer, "response.failed", "server_error", "", "boom",
		},
		{
			"the stream ends after the error event",
			responsesFailureStream("null", responsesErrorEvent(4, "rate_limit_exceeded", "slow down", "null")),
			protocol.FaultRateLimited, "response.failed", "rate_limit_exceeded", "", "slow down",
		},
		{
			"the error event before the response starts",
			responsesErrorEvent(0, "context_length_exceeded", "too long", "null"),
			protocol.FaultContextTooLong, "error", "context_length_exceeded", "input", "too long",
		},
		{
			"the error event, then the failure frame",
			responsesFailureStream("null", responsesErrorEvent(4, "rate_limit_exceeded", "slow down", "null"),
				responsesFailed(5, "null")),
			protocol.FaultServer, "response.failed", "server_error", "", "boom",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := relay(t, protocol.Responses, strings.NewReader(c.stream), protocol.UsageContext{}, protocol.NewMemory(0), settled)
			if r := checkFailureRead(t, got, c.fault); r == nil || r.Code != c.code || r.Message != c.words {
				t.Errorf("rejection %v, want code %q and words %q", r, c.code, c.words)
			}
			checkFailureEnding(t, got, c.last, c.code, c.param)
		})
	}
}

// checkFailureRead is the error the read failed with: the upstream's fault, and what it named of the request.
func checkFailureRead(t *testing.T, got relayed, want protocol.Fault) *protocol.Rejection {
	t.Helper()
	if protocol.FaultOf(got.err) != want {
		t.Fatalf("error %v, fault %d, want %d", got.err, protocol.FaultOf(got.err), want)
	}
	return protocol.RejectionOf(got.err)
}

// checkFailureEnding is the last event the client reads: its type, and the code and param its error carries, which the
// failed response nests and the bare error event does not.
func checkFailureEnding(t *testing.T, got relayed, kind, code, param string) {
	t.Helper()
	es := events(t, got.out)
	last := es[len(es)-1]
	gotCode, gotParam := last["code"], last["param"]
	if response, _ := last["response"].(map[string]any); response != nil {
		gotCode = obj(response["error"])["code"]
	}
	if last["type"] != kind || gotCode != code || param != "" && gotParam != param {
		t.Errorf("ending %v, want %s with code %q and param %q", last, kind, code, param)
	}
	if err := streamcheck.Responses(bytes.NewReader(got.out)); err != nil {
		t.Error(err)
	}
}

// TestRelayChatFailureUsage: an error chunk ends the stream, so a usage object in it is taken before the error, and the
// error's members are the client's to read (docs/domains/gateway.md「错误」).
func TestRelayChatFailureUsage(t *testing.T) {
	chunk := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":7,"model":"gpt-upstream",` +
		`"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":11},` +
		`"error":{"message":"too long","type":"invalid_request_error","code":"context_length_exceeded","param":"messages"}}` + "\n\n"
	got := relay(t, protocol.Chat, strings.NewReader(chatRole+chatHi+chunk), protocol.UsageContext{}, protocol.NewMemory(0), settled)
	if !errors.Is(got.err, protocol.ErrUpstream) || protocol.FaultOf(got.err) != protocol.FaultContextTooLong {
		t.Fatalf("error %v, fault %d", got.err, protocol.FaultOf(got.err))
	}
	reply := got.r.Usage().Segments[0]
	if !reply.Reported.Has(catalog.ItemOutput) || reply.Usage.OutputTotal() != 11 || reply.Usage.Count(catalog.ItemInput) != 5 {
		t.Errorf("input %d, output %d, reported %d", reply.Usage.Count(catalog.ItemInput), reply.Usage.OutputTotal(), reply.Reported)
	}
	want := protocol.Rejection{Message: "too long", Type: "invalid_request_error", Code: "context_length_exceeded", Param: "messages"}
	if r := protocol.RejectionOf(got.err); r == nil || *r != want {
		t.Errorf("rejection %v, want %+v", r, want)
	}
	es := events(t, got.out)
	err := obj(es[len(es)-1]["error"])
	if err["code"] != want.Code || err["message"] != want.Message {
		t.Errorf("ending error %v", err)
	}
}
