package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: strings are kept as written, never decoded and encoded again
	"fmt"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
)

// canon is the Chat-shaped model shared by both request directions (README.md「跨格式转换」), read with jsonx.
// A string the conversion copies is kept as written, quotes
// and escapes included, so its decoded value reaches the upstream unchanged (D2). Members a conversion cannot express
// are refused, not dropped: Unknown holds those Chat defines that no member names here, and every other format's
// own members, so the conversion can tell.
type canon struct {
	legacy              bool
	Messages            []canonMessage            `json:"messages"`
	Tools               []canonTool               `json:"tools"`
	Functions           []canonFunction           `json:"functions"` // legacy: upgradeLegacy makes them tools
	ToolChoice          jsontext.Value            `json:"tool_choice"`
	FunctionCall        jsontext.Value            `json:"function_call"` // legacy: upgradeLegacy makes it the choice
	ParallelToolCalls   *bool                     `json:"parallel_tool_calls"`
	MaxTokens           *int64                    `json:"max_tokens"`
	MaxCompletionTokens *int64                    `json:"max_completion_tokens"`
	Temperature         jsontext.Value            `json:"temperature"`
	TopP                jsontext.Value            `json:"top_p"`
	Stop                jsontext.Value            `json:"stop"`
	Stream              *bool                     `json:"stream"`
	ReasoningEffort     *string                   `json:"reasoning_effort"`
	Reasoning           *canonReasoning           `json:"reasoning"`
	ResponseFormat      *canonFormat              `json:"response_format"`
	User                *string                   `json:"user"`
	SafetyIdentifier    *string                   `json:"safety_identifier"`
	PromptCacheKey      *string                   `json:"prompt_cache_key"`
	ServiceTier         *string                   `json:"service_tier"`
	Verbosity           jsontext.Value            `json:"verbosity"`
	Unknown             map[string]jsontext.Value `json:",embed"`
	// Read, not converted: the model is the endpoint's; stream_options only asks for the usage every conversion
	// gets; the rest are OpenRouter's routing parameters and our extensions, which the gateway has used.
	Model         jsontext.Value `json:"model"`
	StreamOptions jsontext.Value `json:"stream_options"`
	Preset        jsontext.Value `json:"preset"`
	Fallbacks     jsontext.Value `json:"fallbacks"`
	Provider      jsontext.Value `json:"provider"`
	Models        jsontext.Value `json:"models"`
	Route         jsontext.Value `json:"route"`
	Transforms    jsontext.Value `json:"transforms"`
	Plugins       jsontext.Value `json:"plugins"`
	Usage         jsontext.Value `json:"usage"`
	SessionID     jsontext.Value `json:"session_id"`
	Axiom         jsontext.Value `json:"axiom"`
}

// canonMessage is one message. Content is a string or an array of parts, as Chat has it; parts reads both.
type canonMessage struct {
	legacyResult     bool
	Role             string                    `json:"role"`
	Content          jsontext.Value            `json:"content"`
	ToolCalls        []canonToolCall           `json:"tool_calls"`
	ToolCallID       *string                   `json:"tool_call_id"`
	FunctionCall     jsontext.Value            `json:"function_call"` // legacy: upgradeLegacy makes it a tool call
	ReasoningDetails []canonReasoningDetail    `json:"reasoning_details"`
	Unknown          map[string]jsontext.Value `json:",embed"`
	// Name and Refusal go into Parts (settle). Reasoning in the other two members is read, not converted: it has no
	// signature to replay it with.
	Name             jsontext.Value `json:"name"`
	Refusal          jsontext.Value `json:"refusal"`
	ReasoningText    jsontext.Value `json:"reasoning"`
	ReasoningContent jsontext.Value `json:"reasoning_content"`
	// Parts is the content read once: a string is one text part, null none.
	Parts []canonPart `json:"-"`
}

// canonPart is one content part.
type canonPart struct {
	Type         string         `json:"type"`
	Text         jsontext.Value `json:"text"` // a refusal part's refusal too (settle)
	Refusal      jsontext.Value `json:"refusal"`
	ImageURL     *canonImage    `json:"image_url"`
	File         *canonFile     `json:"file"`
	CacheControl jsontext.Value `json:"cache_control"`
}

type canonImage struct {
	URL    string `json:"url"`
	Detail string `json:"detail"`
}

type canonFile struct {
	FileData *string        `json:"file_data"`
	FileID   *string        `json:"file_id"`
	Filename jsontext.Value `json:"filename"`
}

type canonToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function canonCallFunction `json:"function"`
}

type canonCallFunction struct {
	Name      jsontext.Value `json:"name"`
	Arguments jsontext.Value `json:"arguments"`
}

type canonTool struct {
	Type         string         `json:"type"`
	Function     canonFunction  `json:"function"`
	CacheControl jsontext.Value `json:"cache_control"`
}

// canonReasoning is OpenRouter's reasoning setting (OpenRouter《Reasoning tokens》, read 2026-10-14).
type canonReasoning struct {
	Effort    *string `json:"effort"`
	MaxTokens *int64  `json:"max_tokens"`
	Exclude   *bool   `json:"exclude"`
	Enabled   *bool   `json:"enabled"`
}

// canonReasoningDetail is one entry of OpenRouter's reasoning_details, by which a client hands reasoning back.
type canonReasoningDetail struct {
	Type      string         `json:"type"`
	Format    string         `json:"format"`
	ID        jsontext.Value `json:"id"`
	Text      jsontext.Value `json:"text"`
	Summary   jsontext.Value `json:"summary"`
	Signature jsontext.Value `json:"signature"`
	Data      jsontext.Value `json:"data"`
	Index     jsontext.Value `json:"index"`
}

type canonFormat struct {
	Type       string `json:"type"`
	JSONSchema *struct {
		Name        jsontext.Value `json:"name"`
		Description jsontext.Value `json:"description"`
		Schema      jsontext.Value `json:"schema"`
		Strict      *bool          `json:"strict"`
	} `json:"json_schema"`
}

// decodeCanon reads a Chat body Inspect accepted without a budget, with encoding/json's own decoder: the caller
// prepares a request outside a node, and the fast path allocates no more than the conversion it feeds.
func decodeCanon(body []byte) (*canon, error) {
	c := &canon{}
	if err := jsonx.Unmarshal(body, c); err != nil {
		return nil, invaliderr(err)
	}
	if err := c.upgradeLegacy(nil); err != nil {
		return nil, err
	}
	for i := range c.Messages {
		m := &c.Messages[i]
		var err error
		if m.Parts, err = m.readParts(); err != nil {
			return nil, err
		}
		if err := settleMessage(m, nil); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// readParts reads a message's content: a string is one text part, null none.
func (m *canonMessage) readParts() ([]canonPart, error) {
	if isNull(m.Content) {
		return nil, nil
	}
	if m.Content.Kind() == '"' {
		return []canonPart{{Type: "text", Text: m.Content}}, nil
	}
	var parts []canonPart
	if err := jsonx.Unmarshal(m.Content, &parts); err != nil {
		return nil, fmt.Errorf("%w: a %s message's content: %s", ErrInvalid, m.Role, bodyFault(err))
	}
	return parts, nil
}

// settleMessage finishes a message the decoder read: the content is already in Parts, and this merges the reasoning
// details a client streamed back and puts what the other formats have no member for where OpenRouter puts it — a name
// before the text as "name: " (OpenRouter《API reference》"Requests", read 2026-10-16), and a refusal as a part of its
// own, which Messages writes as text and Responses as its refusal part. Each allocation is drawn from b first.
func settleMessage(m *canonMessage, b *budget) error {
	for i, p := range m.Parts {
		if p.Type == "refusal" { // what it says is its refusal, which the conversions write as they write text
			m.Parts[i].Text = p.Refusal
		}
	}
	var err error
	if m.ReasoningDetails, err = mergeDetailsWithin(m.ReasoningDetails, b); err != nil {
		return err
	}
	switch {
	case !isNull(m.Name) && m.Name.Kind() != '"', !isNull(m.Refusal) && m.Refusal.Kind() != '"':
		return shapeError("a name or refusal that is not a string")
	case !isNull(m.Refusal):
		if m.Parts, err = growWithin(m.Parts, b); err != nil {
			return err
		}
		m.Parts[len(m.Parts)-1] = canonPart{Type: "refusal", Text: m.Refusal}
	}
	if !isNull(m.Name) && m.Role != "tool" { // a tool result's name is its call's, which its ID already says
		return m.prependName(b)
	}
	return nil
}

// prependName puts "name: " before the message's first text or refusal, or as text of its own when it has none.
func (m *canonMessage) prependName(b *budget) error {
	if err := b.draw(int64(len(m.Name) + 2)); err != nil {
		return err
	}
	prefix := slices.Concat(m.Name[:len(m.Name)-1], []byte(`: "`))
	for i, p := range m.Parts {
		if (p.Type == "text" || p.Type == "refusal") && p.Text.Kind() == '"' {
			if err := b.draw(int64(len(prefix) - 1 + len(p.Text) - 1)); err != nil {
				return err
			}
			m.Parts[i].Text = slices.Concat(prefix[:len(prefix)-1], p.Text[1:])
			return nil
		}
	}
	var err error
	if m.Parts, err = growWithin(m.Parts, b); err != nil {
		return err
	}
	copy(m.Parts[1:], m.Parts[:len(m.Parts)-1])
	m.Parts[0] = canonPart{Type: "text", Text: prefix}
	return nil
}

// given is b when it was given, else a.
func given(a, b jsontext.Value) jsontext.Value {
	if isNull(b) {
		return a
	}
	return b
}

// checkShape holds the members the conversions copy as written to the type Chat gives them (OpenAI《Create chat
// completion》, read 2026-10-14): a tool's and a call's name and a text part's text are strings, a call's arguments a
// string, and a JSON schema reply names itself and has an object for a schema. Anything else is the client's error.
func (c *canon) checkShape() error {
	for _, tool := range c.Tools {
		if tool.Type == "function" && tool.Function.Name.Kind() != '"' {
			return shapeError("a tool without a name")
		}
	}
	for _, m := range c.Messages {
		if err := m.checkShape(); err != nil {
			return err
		}
	}
	if f := c.ResponseFormat; f != nil && f.Type == "json_schema" &&
		(f.JSONSchema == nil || f.JSONSchema.Name.Kind() != '"' || f.JSONSchema.Schema.Kind() != '{') {
		return shapeError("a JSON schema reply without a name or a schema")
	}
	return nil
}

func (m *canonMessage) checkShape() error {
	for _, call := range m.ToolCalls {
		if call.Type == "function" && (call.Function.Name.Kind() != '"' || call.Function.Arguments.Kind() != '"') {
			return shapeError("a tool call without a name or string arguments")
		}
	}
	for _, p := range m.Parts {
		if err := checkPart(p, m.Role); err != nil {
			return err
		}
	}
	return nil
}

// checkPart holds a part to what the conversions copy as written and the targets write or refuse by name: a text or
// refusal part carries its text, a refusal belongs to the assistant, and an image's detail is one Chat defines
// (OpenAI《Create chat completion》"detail", read 2026-10-14).
func checkPart(p canonPart, role string) error {
	switch p.Type {
	case "text", "refusal":
		if p.Text.Kind() != '"' {
			return shapeError("a text part without text or a refusal part without its refusal")
		}
		if p.Type == "refusal" && role != "assistant" {
			return &NotSupported{Reason: "a refusal part in a " + role + " message"}
		}
	case "image_url":
		if p.ImageURL == nil {
			return nil
		}
		switch p.ImageURL.Detail {
		case "", "auto", "low", "high":
			return nil
		}
		return shapeError("an image detail that is not auto, low or high")
	}
	return nil
}

func shapeError(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }

// maxOutput is the output cap the request sets, 0 for none.
func (c *canon) maxOutput() int64 {
	var n int64
	for _, m := range []*int64{c.MaxTokens, c.MaxCompletionTokens} {
		if m != nil {
			n = max(n, *m)
		}
	}
	return n
}

// unsupported reports the first member of Unknown that is set, which the conversion cannot express.
func (c *canon) unsupported() error {
	for name, v := range c.Unknown {
		if !isDefault(name, v) {
			return &NotSupported{Reason: notConverted(name)}
		}
	}
	for _, m := range c.Messages {
		for name := range m.Unknown {
			return &NotSupported{Reason: "a message's " + notConverted(name)}
		}
	}
	return nil
}

// isDefault reports whether a member the conversion does not express is absent in effect: null, or the value Chat
// takes when it is left out (OpenAI《Create chat completion》, read 2026-10-14), so a client that writes the default
// is not refused.
// notConverted is the reason a member is refused, naming it by its first 64 bytes at most: a name may be as long as
// the body, and the refusal is not drawn from the budget.
func notConverted(name string) string {
	if len(name) > 64 {
		name = strings.ToValidUTF8(name[:64], "") + "…"
	}
	return name + " is not converted"
}

func isDefault(name string, v jsontext.Value) bool {
	if v.Kind() == 'n' {
		return true
	}
	// v is compared in place, never copied: a value may be as long as the body.
	switch name {
	case "frequency_penalty", "presence_penalty":
		return zeroNumber(v)
	case "n":
		return string(v) == "1"
	case "logprobs", "store":
		return string(v) == "false"
	case "modalities":
		if string(v) == `["text"]` { // keep the common default allocation-free; equivalent spellings decode below
			return true
		}
		return onlyText(v)
	case "metadata", "logit_bias":
		return len(bytes.TrimSpace(bytes.Trim(v, "{}"))) == 0
	}
	return false
}

// onlyText reports whether v is an array of the one string "text", in any spelling. The element is unquoted into a small
// array of its own, so neither a long element nor many of them allocate.
func onlyText(v jsontext.Value) bool {
	dec := newDecoder(v)
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return false
	}
	s, err := dec.ReadValue()
	if err != nil || s.Kind() != '"' || len(s) > 32 {
		return false
	}
	var buf [32]byte
	text, err := jsontext.AppendUnquote(buf[:0], s)
	return err == nil && string(text) == "text" && dec.PeekKind() == ']'
}

// zeroNumber reports whether v is a JSON number whose value is zero: every digit of its mantissa is 0, whatever its
// sign and exponent. It is what ParseFloat(v) == 0 says of a valid number, without copying v to a string.
func zeroNumber(v jsontext.Value) bool {
	if v.Kind() != '0' {
		return false
	}
	for _, c := range bytes.TrimPrefix(v, []byte("-")) {
		switch {
		case c == 'e' || c == 'E':
			return true
		case c != '0' && c != '.':
			return false
		}
	}
	return true
}

// numberIs reports whether v is absent, null or the number want.
func numberIs(v jsontext.Value, want float64) bool {
	if len(v) == 0 || v.Kind() == 'n' {
		return true
	}
	f, err := strconv.ParseFloat(string(v), 64)
	return err == nil && f == want
}

// numberAtMost is whether v, a JSON number, is an integer no larger than limit.
func numberAtMost(v jsontext.Value, limit int64) bool {
	n, err := strconv.ParseInt(string(v), 10, 64)
	return err == nil && n <= limit
}
