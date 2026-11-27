package protocol

import (
	"cmp"
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
// where Chat counts both in the totals and the thinking again as a detail; both break the counts down by modality, in
// lists on Gemini's side and in the details' members on Chat's, which is how audio and images get their own price.
type geminiUsage struct {
	PromptTokenCount        int64           `json:"promptTokenCount"`
	CandidatesTokenCount    int64           `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int64           `json:"thoughtsTokenCount,omitzero"`
	CachedContentTokenCount int64           `json:"cachedContentTokenCount,omitzero"`
	ToolUsePromptTokenCount int64           `json:"toolUsePromptTokenCount,omitzero"`
	TotalTokenCount         int64           `json:"totalTokenCount"`
	PromptTokensDetails     []geminiByMedia `json:"promptTokensDetails,omitzero"`
	CacheTokensDetails      []geminiByMedia `json:"cacheTokensDetails,omitzero"`
	CandidatesTokensDetails []geminiByMedia `json:"candidatesTokensDetails,omitzero"`
}

// geminiByMedia is Gemini's ModalityTokenCount.
type geminiByMedia struct {
	Modality   string `json:"modality"`
	TokenCount int64  `json:"tokenCount"`
}

// chatMedia are the modality counts Chat's usage details carry, the same members on both sides of the usage; video is
// only an input (openai.go).
type chatMedia struct {
	Audio int64 `json:"audio_tokens,omitzero"`
	Image int64 `json:"image_tokens,omitzero"`
	Video int64 `json:"video_tokens,omitzero"`
}

type chatUsageCounts struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens  int64      `json:"cached_tokens"`
		CachedDetails *chatMedia `json:"cached_tokens_details,omitzero"`
		chatMedia
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
		chatMedia
	} `json:"completion_tokens_details"`
}

// mediaOf is a modality list as Chat's counts; TEXT and modalities Chat has no member for stay in the plain total.
func mediaOf(counts []geminiByMedia) chatMedia {
	var m chatMedia
	for _, c := range counts {
		switch c.Modality {
		case "AUDIO":
			m.Audio += c.TokenCount
		case "IMAGE":
			m.Image += c.TokenCount
		case "VIDEO":
			m.Video += c.TokenCount
		}
	}
	return m
}

// byMedia is mediaOf the other way: Chat's counts as Gemini's list, the modalities with tokens only.
func byMedia(m chatMedia) []geminiByMedia {
	var out []geminiByMedia
	for _, c := range [...]geminiByMedia{{"AUDIO", m.Audio}, {"IMAGE", m.Image}, {"VIDEO", m.Video}} {
		if c.TokenCount > 0 {
			out = append(out, c)
		}
	}
	return out
}

func (u geminiUsage) chat() chatUsageCounts {
	var c chatUsageCounts
	c.PromptTokens = u.PromptTokenCount + u.ToolUsePromptTokenCount
	c.CompletionTokens = u.CandidatesTokenCount + u.ThoughtsTokenCount
	c.TotalTokens = c.PromptTokens + c.CompletionTokens
	c.PromptTokensDetails.CachedTokens = u.CachedContentTokenCount
	c.PromptTokensDetails.chatMedia = mediaOf(u.PromptTokensDetails)
	if len(u.CacheTokensDetails) > 0 {
		cached := mediaOf(u.CacheTokensDetails)
		c.PromptTokensDetails.CachedDetails = &cached
	}
	c.CompletionTokensDetails.ReasoningTokens = u.ThoughtsTokenCount
	c.CompletionTokensDetails.chatMedia = mediaOf(u.CandidatesTokensDetails)
	return c
}

func (c chatUsageCounts) gemini() geminiUsage {
	thoughts := min(c.CompletionTokensDetails.ReasoningTokens, c.CompletionTokens)
	u := geminiUsage{
		PromptTokenCount: c.PromptTokens, CandidatesTokenCount: c.CompletionTokens - thoughts, ThoughtsTokenCount: thoughts,
		CachedContentTokenCount: c.PromptTokensDetails.CachedTokens, TotalTokenCount: c.PromptTokens + c.CompletionTokens,
		PromptTokensDetails: byMedia(c.PromptTokensDetails.chatMedia), CandidatesTokensDetails: byMedia(c.CompletionTokensDetails.chatMedia),
	}
	if d := c.PromptTokensDetails.CachedDetails; d != nil {
		u.CacheTokensDetails = byMedia(*d)
	}
	return u
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
	args     [][]byte
	finished bool // the relay repeats the finish reason on its usage chunk, which Gemini says once
	// held is the event that finished the candidate, kept for the usage the relay sends after it: Google's stream ends
	// with one event holding both, and the Google SDKs refuse a turn whose last event has no candidate.
	held *geminiFrame
	// last is the latest content part, held back for the finishing event: Google's last event carries the candidate's
	// final parts, and the Google SDKs refuse a candidate without any.
	last []geminiPart
}

// geminiFrame is an event's makings: the chunk it came of, its parts and the finish reason.
type geminiFrame struct {
	c      chatReply
	parts  []geminiPart
	finish string
}

// NewGeminiReply is the reply for a request GeminiToChat converted with opts.
func NewGeminiReply(opts GeminiOptions) *GeminiReply { return &GeminiReply{opts: opts} }

// Event converts one Chat stream event's data into a Gemini stream event's data, or nil when it has nothing for one:
// the end marker, or a chunk that only opened a call.
func (g *GeminiReply) Event(data []byte) ([]byte, error) {
	if string(data) == sse.Done {
		return g.release(nil), nil
	}
	var c chatReply
	if err := jsonx.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.Error != nil {
		return geminiErrorEvent(*c.Error), nil
	}
	parts, finish := g.chunkParts(c)
	parts, g.last = append(g.last, parts...), nil
	switch {
	case g.held != nil:
		g.held.parts = append(g.held.parts, parts...)
		return g.release(c.Usage), nil
	case finish != "" && c.Usage == nil:
		g.held = &geminiFrame{c, parts, finish}
		return nil, nil
	case finish == "" && len(parts) > 0:
		parts, g.last = parts[:len(parts)-1], slices.Clone(parts[len(parts)-1:])
	}
	if len(parts) == 0 && finish == "" && c.Usage == nil {
		return nil, nil
	}
	return geminiResponseOf(c, parts, finish), nil
}

// chunkParts are a chunk's parts and the finish reason it gives first: a call's parts come whole at the finish.
func (g *GeminiReply) chunkParts(c chatReply) ([]geminiPart, string) {
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
	return parts, finish
}

// release gives the held finishing event with the usage, if one is held.
func (g *GeminiReply) release(usage *chatUsageCounts) []byte {
	h := g.held
	if h == nil && g.last != nil { // the stream ended unfinished: what was held back still goes out
		h = &geminiFrame{parts: g.last}
	}
	g.last = nil
	if h == nil {
		return nil
	}
	g.held = nil
	if usage != nil {
		h.c.Usage = usage
	}
	return geminiResponseOf(h.c, h.parts, h.finish)
}

// geminiErrorEvent is a Chat stream's error chunk as Gemini's error, at the status its code says.
func geminiErrorEvent(e chatError) jsonx.Raw {
	status, err := strconv.Atoi(string(e.Code))
	if err != nil || status == 0 {
		status = 502 // an error event's code is the status Axiom wrote it with
	}
	return geminiError(status, e)
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
			g.args = append(g.args, nil)
		}
		call := &g.calls[t.Index]
		call.FunctionCall.ID = cmp.Or(call.FunctionCall.ID, t.ID)
		call.FunctionCall.Name += t.Function.Name
		g.args[t.Index] = append(g.args[t.Index], t.Function.Arguments...)
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
		calls[i].FunctionCall.Args = geminiArgs(string(g.args[i]))
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
		if len(parts) == 0 { // a reply with nothing in it still finishes with a part, as every candidate must have one
			empty := ""
			parts = []geminiPart{{Text: &empty}}
		}
		candidate["content"] = jsonValue(geminiContent{Role: "model", Parts: parts})
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
