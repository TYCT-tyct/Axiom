package protocol_test

import (
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

const chatFileStream = `data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"The page says hello."}}]}

data: {"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`

// TestAggregateAnnotatesFiles: an aggregated Chat reply carries the parsed files as `file` annotations on every choice,
// in the shape OpenRouter returns, so a client can send the text back instead of the file.
func TestAggregateAnnotatesFiles(t *testing.T) {
	files := []protocol.FileAnnotation{{Hash: "abc123", Name: "scan.pdf", Text: "The parsed page."}}
	o := protocol.RelayOptions{
		ID: "gen-1", Model: "m", Created: 1, Aggregate: true, Memory: protocol.NewMemory(1 << 20),
	}
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(chatFileStream), protocol.UsageContext{}, o)
	var body []byte
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			body = r.Finish(protocol.Final{Files: files})
			break
		}
	}
	annotations := replyAnnotations(t, body)
	if len(annotations) != 1 {
		t.Fatalf("annotations = %+v", annotations)
	}
	a := annotations[0]
	if a.Type != "file" || a.File.Hash != "abc123" || a.File.Name != "scan.pdf" ||
		len(a.File.Content) != 1 || a.File.Content[0].Type != "text" || a.File.Content[0].Text != "The parsed page." {
		t.Fatalf("annotation = %+v", a)
	}
}

// replyAnnotation is one annotation of a Chat reply's first choice, as far as these tests read it.
type replyAnnotation struct {
	Type string `json:"type"`
	File struct {
		Hash    string `json:"hash"`
		Name    string `json:"name"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"file"`
}

// replyAnnotations reads the annotations of an aggregated Chat reply's first choice.
func replyAnnotations(t *testing.T, body []byte) []replyAnnotation {
	t.Helper()
	var reply struct {
		Choices []struct {
			Message struct {
				Annotations []replyAnnotation `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := jsonx.Unmarshal(body, &reply); err != nil {
		t.Fatalf("the reply is not JSON: %v: %s", err, body)
	}
	if len(reply.Choices) != 1 {
		t.Fatalf("the reply has %d choices, want 1: %s", len(reply.Choices), body)
	}
	return reply.Choices[0].Message.Annotations
}

// TestAggregateWithoutFilesHasNoAnnotations: a reply keeps the shape it had when nothing was parsed.
func TestAggregateWithoutFilesHasNoAnnotations(t *testing.T) {
	o := protocol.RelayOptions{ID: "gen-1", Model: "m", Created: 1, Aggregate: true, Memory: protocol.NewMemory(1 << 20)}
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, strings.NewReader(chatFileStream), protocol.UsageContext{}, o)
	var body []byte
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == protocol.StepUpstreamDone {
			body = r.Finish(protocol.Final{})
			break
		}
	}
	if strings.Contains(string(body), `"annotations"`) {
		t.Fatalf("a reply without parsed files carries annotations: %s", body)
	}
}
