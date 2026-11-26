package protocol

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// geminiStreamResponse is one event of streamGenerateContent?alt=sse, or the error Google ends a stream with.
type geminiStreamResponse struct {
	Candidates []struct {
		Content      *geminiContent `json:"content"`
		FinishReason string         `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *geminiUsage `json:"usageMetadata"`
	ModelVersion  string       `json:"modelVersion"`
	ResponseID    string       `json:"responseId"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// ChatFromGemini reads the body of Google's streamGenerateContent?alt=sse as the Chat stream the relay reads from a Chat
// endpoint: text and thoughts as deltas, each function call whole with its thought signature, the finish reason, the
// usage chunk and the end marker; a Gemini error or a reason Chat has no word for ends it as an error chunk. A stream
// that stops before Gemini finished ends without the marker, which the relay reads as cut off.
func ChatFromGemini(body io.ReadCloser) io.ReadCloser {
	return &chatFromGemini{src: body, events: sse.NewReader(body, 32<<20)}
}

type chatFromGemini struct {
	src      io.ReadCloser
	events   *sse.Reader
	out      []byte
	err      error
	id       string
	model    string
	calls    int
	finish   string
	usage    *geminiUsage
	finished bool
}

func (s *chatFromGemini) Read(p []byte) (int, error) {
	for len(s.out) == 0 && s.err == nil {
		data, err := s.events.Next()
		switch {
		case errors.Is(err, io.EOF):
			s.end()
			s.err = io.EOF
		case err != nil:
			s.err = err
		default:
			if err := s.event(data); err != nil {
				s.err = err
			}
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	if len(s.out) > 0 {
		return n, nil
	}
	return n, s.err
}

func (s *chatFromGemini) Close() error { return s.src.Close() }

func (s *chatFromGemini) event(data []byte) error {
	if s.finished {
		return nil // what comes after the end is not read
	}
	var r geminiStreamResponse
	if err := jsonx.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("%w: %s", errGeminiStream, bodyFault(err))
	}
	if r.Error != nil {
		s.fail(cmp.Or(r.Error.Code, 502), r.Error.Message)
		return nil
	}
	s.id, s.model = cmp.Or(s.id, r.ResponseID), cmp.Or(s.model, r.ModelVersion)
	if r.UsageMetadata != nil {
		s.usage = r.UsageMetadata
	}
	if len(r.Candidates) > 1 {
		return fmt.Errorf("%w: more than one candidate", errGeminiStream)
	}
	if len(r.Candidates) == 0 {
		if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
			s.finish = "content_filter"
		}
		return nil
	}
	cand := r.Candidates[0]
	if cand.Content != nil {
		if err := s.parts(cand.Content.Parts); err != nil {
			return err
		}
	}
	s.finishWith(cand.FinishReason)
	return nil
}

// finishWith records Gemini's finish reason as Chat's, or ends the stream for one Chat has no word for.
func (s *chatFromGemini) finishWith(gemini string) {
	if gemini == "" {
		return
	}
	reason, ok := geminiFinish[gemini]
	switch {
	case !ok:
		s.fail(502, "Gemini stopped the reply: "+gemini)
	case reason == "stop" && s.calls > 0:
		s.finish = "tool_calls"
	default:
		s.finish = reason
	}
}

func (s *chatFromGemini) parts(parts []geminiPart) error {
	for _, p := range parts {
		delta := requestObject{}
		switch {
		case p.FunctionCall != nil:
			delta["tool_calls"] = jsonValue([]requestObject{s.call(p)})
		case p.Text != nil && p.Thought:
			delta["reasoning_content"] = jsonValue(*p.Text)
		case p.Text != nil:
			delta["content"] = jsonValue(*p.Text)
		case p.InlineData != nil || p.FileData != nil || p.FunctionResponse != nil || len(p.Unknown) > 0:
			return fmt.Errorf("%w: a Gemini reply part Chat has no counterpart for", ErrUpstream)
		default:
			continue // a part holding only a signature, which Chat's text needs no carrier for
		}
		s.chunk(requestObject{"index": jsonx.Raw("0"), "delta": jsonValue(delta), "finish_reason": jsonx.Raw("null")}, nil)
	}
	return nil
}

// call is a function call part as the Chat tool call that starts and finishes in one delta.
func (s *chatFromGemini) call(p geminiPart) requestObject {
	id := p.FunctionCall.ID
	if id == "" {
		id = "call_" + strconv.Itoa(s.calls) // unique within the reply, which is all a Chat call ID promises
		if s.id != "" {
			id = "call_" + s.id + "_" + strconv.Itoa(s.calls)
		}
	}
	call := requestObject{
		"index": jsonValue(s.calls), "id": jsonValue(id), "type": jsonValue("function"),
		"function": jsonValue(requestObject{"name": jsonValue(p.FunctionCall.Name), "arguments": jsonValue(string(geminiArgs(string(cmpOrValue(p.FunctionCall.Args)))))}),
	}
	if p.ThoughtSignature != "" {
		call["extra_content"] = googleExtra(p.ThoughtSignature)
	}
	s.calls++
	return call
}

func cmpOrValue(v jsonx.Raw) jsonx.Raw {
	if isNull(v) {
		return jsonx.Raw("{}")
	}
	return v
}

// chunk appends one Chat chunk with the choices given, and usage when it is the usage chunk.
func (s *chatFromGemini) chunk(choice requestObject, usage *chatUsageCounts) {
	c := requestObject{
		"id": jsonValue(cmp.Or(s.id, "gemini")), "object": jsonValue("chat.completion.chunk"),
		"created": jsonx.Raw("0"), "model": jsonValue(s.model), "choices": jsonx.Raw("[]"),
	}
	if choice != nil {
		c["choices"] = jsonValue([]requestObject{choice})
	}
	if usage != nil {
		c["usage"] = jsonValue(usage)
	}
	s.out = slices.Concat(s.out, []byte("data: "), jsonValue(c), []byte("\n\n"))
}

// end closes a stream Gemini finished: the finish reason, the usage and the end marker.
func (s *chatFromGemini) end() {
	if s.finished || s.finish == "" {
		return
	}
	s.finished = true
	s.chunk(requestObject{"index": jsonx.Raw("0"), "delta": jsonx.Raw("{}"), "finish_reason": jsonValue(s.finish)}, nil)
	if s.usage != nil {
		u := s.usage.chat()
		s.chunk(nil, &u)
	}
	s.out = append(s.out, "data: [DONE]\n\n"...)
}

// fail ends the stream with Chat's error chunk.
func (s *chatFromGemini) fail(status int, message string) {
	s.finished = true
	e := requestObject{"message": jsonValue(message), "type": jsonValue("upstream_error"), "code": jsonValue(status)}
	s.out = slices.Concat(s.out, []byte("data: "), jsonValue(requestObject{"error": jsonValue(e)}), []byte("\n\n"))
}
