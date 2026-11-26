package protocol

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// The replies of gemini.go's two directions: Chat's chunks and bodies written as Gemini's for a Gemini client
// (GeminiReply), and a Gemini stream read as Chat's chunks for the relay (ChatFromGemini). Usage, finish reasons and
// errors map through the tables here both ways.

// geminiUsage is usageMetadata. Gemini counts thinking apart from the answer and tool prompts apart from the prompt,
// where Chat counts both in the totals and the thinking again as a detail.
type geminiUsage struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitzero"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitzero"`
	ToolUsePromptTokenCount int64 `json:"toolUsePromptTokenCount,omitzero"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

type chatUsageCounts struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u geminiUsage) chat() chatUsageCounts {
	var c chatUsageCounts
	c.PromptTokens = u.PromptTokenCount + u.ToolUsePromptTokenCount
	c.CompletionTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
	c.TotalTokens = c.PromptTokens + c.CompletionTokens
	c.PromptTokensDetails.CachedTokens = u.CachedContentTokenCount
	c.CompletionTokensDetails.ReasoningTokens = u.ThoughtsTokenCount
	return c
}

func (c chatUsageCounts) gemini() geminiUsage {
	thoughts := min(c.CompletionTokensDetails.ReasoningTokens, c.CompletionTokens)
	return geminiUsage{
		PromptTokenCount: c.PromptTokens, CandidatesTokenCount: c.CompletionTokens - thoughts, ThoughtsTokenCount: thoughts,
		CachedContentTokenCount: c.PromptTokensDetails.CachedTokens, TotalTokenCount: c.PromptTokens + c.CompletionTokens,
	}
}

// geminiFinish are Gemini's finish reasons and Chat's. One Gemini gives for a reply it cut off for another reason
// (OTHER, MALFORMED_FUNCTION_CALL and those added later) is an upstream failure, as an unknown stop reason is elsewhere.
var geminiFinish = map[string]string{
	"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "RECITATION": "content_filter",
	"BLOCKLIST": "content_filter", "PROHIBITED_CONTENT": "content_filter", "SPII": "content_filter", "IMAGE_SAFETY": "content_filter",
}

// geminiFinishOf is a Chat finish reason as Gemini's: a call ends the turn as STOP does, and a filter is SAFETY.
func geminiFinishOf(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return "STOP"
	case "content_filter":
		return "SAFETY"
	}
	return cmp.Or(inverseName(geminiFinish, reason), "STOP")
}

// geminiStatuses are the google.rpc status names of the HTTP statuses an error answers with.
var geminiStatuses = map[int]string{
	400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 402: "RESOURCE_EXHAUSTED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND",
	408: "DEADLINE_EXCEEDED", 409: "ABORTED", 413: "INVALID_ARGUMENT", 429: "RESOURCE_EXHAUSTED", 499: "CANCELLED",
	500: "INTERNAL", 501: "UNIMPLEMENTED", 502: "UNAVAILABLE", 503: "UNAVAILABLE", 504: "DEADLINE_EXCEEDED",
}

type chatError struct {
	Message  string    `json:"message"`
	Type     string    `json:"type"`
	Code     jsonx.Raw `json:"code"`
	Metadata jsonx.Raw `json:"metadata"`
}

// geminiError is Google's error object: the status as code and its google.rpc name, with the message; Axiom's own
// error type rides in details as ErrorInfo's reason, as Google puts a machine-readable cause.
func geminiError(status int, e chatError) jsonx.Raw {
	name := geminiStatuses[status]
	if name == "" {
		name = map[bool]string{true: "INTERNAL", false: "INVALID_ARGUMENT"}[status >= 500]
	}
	out := requestObject{"code": jsonValue(status), "message": jsonValue(e.Message), "status": jsonValue(name)}
	var meta struct {
		ErrorType string `json:"error_type"`
	}
	if !isNull(e.Metadata) && jsonx.Unmarshal(e.Metadata, &meta) == nil && meta.ErrorType != "" {
		out["details"] = jsonValue([]requestObject{{
			"@type": jsonValue("type.googleapis.com/google.rpc.ErrorInfo"), "reason": jsonValue(meta.ErrorType), "domain": jsonValue("axiom"),
		}})
	}
	return jsonValue(requestObject{"error": jsonValue(out)})
}

// chatReply is a Chat chunk or completion as the relay writes it.
type chatReply struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta        *chatReplyMessage `json:"delta"`
		Message      *chatReplyMessage `json:"message"`
		FinishReason *string           `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsageCounts `json:"usage"`
	Error *chatError       `json:"error"`
}

type chatReplyMessage struct {
	Content          *string `json:"content"`
	Reasoning        *string `json:"reasoning"`
	ReasoningContent *string `json:"reasoning_content"`
	ToolCalls        []struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
		ExtraContent jsonx.Raw `json:"extra_content"`
	} `json:"tool_calls"`
}

// GeminiReply writes a Chat reply as Gemini's for a Gemini client: the stream event by event, or the whole body. A
// stream's calls are held until the choice finishes, since Gemini gives each call whole.
type GeminiReply struct {
	opts     GeminiOptions
	calls    []geminiPart
	args     []strings.Builder
	finished bool // the relay repeats the finish reason on its usage chunk, which Gemini says once
}

// NewGeminiReply is the reply for a request GeminiToChat converted with opts.
func NewGeminiReply(opts GeminiOptions) *GeminiReply { return &GeminiReply{opts: opts} }

// Event converts one Chat stream event's data into a Gemini stream event's data, or nil when it has nothing for one:
// the end marker, or a chunk that only opened a call.
func (g *GeminiReply) Event(data []byte) ([]byte, error) {
	if string(data) == sse.Done {
		return nil, nil
	}
	var c chatReply
	if err := jsonx.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Error != nil {
		status, _ := strconv.Atoi(string(c.Error.Code))
		return geminiError(cmp.Or(status, 502), *c.Error), nil
	}
	var parts []geminiPart
	finish := ""
	for _, choice := range c.Choices {
		if choice.Delta != nil {
			parts = g.deltaParts(parts, choice.Delta)
		}
		if choice.FinishReason != nil && !g.finished {
			g.finished, finish = true, geminiFinishOf(*choice.FinishReason)
			parts = append(parts, g.flushCalls()...)
		}
	}
	if len(parts) == 0 && finish == "" && c.Usage == nil {
		return nil, nil
	}
	return geminiResponseOf(c, parts, finish), nil
}

func (g *GeminiReply) deltaParts(parts []geminiPart, d *chatReplyMessage) []geminiPart {
	if r := cmpOrPtr(d.ReasoningContent, d.Reasoning); r != nil && *r != "" && g.opts.Thoughts {
		parts = append(parts, geminiPart{Text: r, Thought: true})
	}
	if d.Content != nil && *d.Content != "" {
		parts = append(parts, geminiPart{Text: d.Content})
	}
	for _, t := range d.ToolCalls {
		for len(g.calls) <= t.Index {
			g.calls = append(g.calls, geminiPart{FunctionCall: &geminiCall{}})
			g.args = append(g.args, strings.Builder{})
		}
		call := &g.calls[t.Index]
		call.FunctionCall.ID = cmp.Or(call.FunctionCall.ID, t.ID)
		call.FunctionCall.Name += t.Function.Name
		g.args[t.Index].WriteString(t.Function.Arguments)
		if s := (canonToolCall{ExtraContent: t.ExtraContent}).signature(); s != "" {
			call.ThoughtSignature = s
		}
	}
	return parts
}

// flushCalls gives the calls held so far, whole.
func (g *GeminiReply) flushCalls() []geminiPart {
	calls := g.calls
	for i := range calls {
		calls[i].FunctionCall.Args = geminiArgs(g.args[i].String())
	}
	g.calls, g.args = nil, nil
	return calls
}

// geminiArgs is a call's arguments as Gemini's args object; arguments that are not one, which a model can write when it
// was cut off, are kept as text under arguments rather than lost.
func geminiArgs(arguments string) jsonx.Raw {
	v := jsonx.Raw(strings.TrimSpace(arguments))
	switch {
	case len(v) == 0:
		return jsonx.Raw("{}")
	case v[0] == '{' && jsonx.Validate(v) == nil:
		return v
	}
	return jsonValue(map[string]string{"arguments": arguments})
}

func geminiResponseOf(c chatReply, parts []geminiPart, finish string) jsonx.Raw {
	out := requestObject{"modelVersion": jsonValue(c.Model), "responseId": jsonValue(c.ID)}
	if len(parts) > 0 || finish != "" {
		candidate := requestObject{"index": jsonx.Raw("0")}
		if len(parts) > 0 {
			candidate["content"] = jsonValue(geminiContent{Role: "model", Parts: parts})
		}
		if finish != "" {
			candidate["finishReason"] = jsonValue(finish)
		}
		out["candidates"] = jsonValue([]requestObject{candidate})
	}
	if c.Usage != nil {
		out["usageMetadata"] = jsonValue(c.Usage.gemini())
	}
	return jsonValue(out)
}

// Body converts a whole Chat reply, or the error a call answered with, into Gemini's.
func (g *GeminiReply) Body(status int, body []byte) ([]byte, error) {
	var c chatReply
	if err := jsonx.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	if c.Error != nil || status >= 400 {
		if c.Error == nil {
			c.Error = &chatError{Message: "The request failed."}
		}
		return geminiError(status, *c.Error), nil
	}
	var parts []geminiPart
	finish := ""
	for i, choice := range c.Choices {
		if i > 0 || choice.Message == nil {
			continue
		}
		m := choice.Message
		parts = g.deltaParts(parts, &chatReplyMessage{Content: m.Content, Reasoning: m.Reasoning, ReasoningContent: m.ReasoningContent})
		for j := range m.ToolCalls {
			m.ToolCalls[j].Index = j
		}
		g.deltaParts(nil, &chatReplyMessage{ToolCalls: m.ToolCalls})
		parts = append(parts, g.flushCalls()...)
		if choice.FinishReason != nil {
			finish = geminiFinishOf(*choice.FinishReason)
		}
	}
	return geminiResponseOf(c, parts, finish), nil
}

// GeminiErrorBody is Gemini's error object for a Chat-shaped error body answered with status: what a Gemini client
// is told of a refusal before any call is made.
func GeminiErrorBody(status int, body []byte) []byte {
	var c chatReply
	if jsonx.Unmarshal(body, &c) != nil || c.Error == nil {
		c.Error = &chatError{Message: strings.TrimSpace(string(body))}
	}
	return geminiError(status, *c.Error)
}

func cmpOrPtr(a, b *string) *string {
	if a != nil && *a != "" {
		return a
	}
	return b
}

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
			id := p.FunctionCall.ID
			if id == "" {
				id = "call_" + strconv.Itoa(s.calls) // unique within the reply, which is all a Chat call ID promises
				if s.id != "" {
					id = "call_" + s.id + "_" + strconv.Itoa(s.calls)
				}
			}
			call := requestObject{"index": jsonValue(s.calls), "id": jsonValue(id), "type": jsonValue("function"),
				"function": jsonValue(requestObject{"name": jsonValue(p.FunctionCall.Name), "arguments": jsonValue(string(geminiArgs(string(cmpOrValue(p.FunctionCall.Args)))))})}
			if p.ThoughtSignature != "" {
				call["extra_content"] = googleExtra(p.ThoughtSignature)
			}
			s.calls++
			delta["tool_calls"] = jsonValue([]requestObject{call})
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

func cmpOrValue(v jsonx.Raw) jsonx.Raw {
	if isNull(v) {
		return jsonx.Raw("{}")
	}
	return v
}

// chunk appends one Chat chunk with the choices given, and usage when it is the usage chunk.
func (s *chatFromGemini) chunk(choice requestObject, usage *chatUsageCounts) {
	c := requestObject{"id": jsonValue(cmp.Or(s.id, "gemini")), "object": jsonValue("chat.completion.chunk"),
		"created": jsonx.Raw("0"), "model": jsonValue(s.model), "choices": jsonx.Raw("[]")}
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
