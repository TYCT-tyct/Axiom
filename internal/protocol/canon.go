package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: strings are kept as written, never decoded and encoded again
	"fmt"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
)

// canon is a request in the canonical model the conversions write from (README.md「跨格式转换」). In M1 only Chat
// requests convert, so its shape is Chat's, read with jsonx; a string the conversion copies is kept as written, quotes
// and escapes included, so its decoded value reaches the upstream unchanged (D2). Members a conversion cannot express
// are refused, not dropped: Unknown holds those Chat defines that no member names here, and every other format's
// own members, so the conversion can tell.
type canon struct {
	Messages            []canonMessage            `json:"messages"`
	Tools               []canonTool               `json:"tools"`
	ToolChoice          jsontext.Value            `json:"tool_choice"`
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
	Role             string                    `json:"role"`
	Content          jsontext.Value            `json:"content"`
	ToolCalls        []canonToolCall           `json:"tool_calls"`
	ToolCallID       *string                   `json:"tool_call_id"`
	ReasoningDetails []canonReasoningDetail    `json:"reasoning_details"`
	Unknown          map[string]jsontext.Value `json:",embed"`
	// Read, not converted: a speaker's name has no place in the other formats, and reasoning in these members has
	// no signature to replay it with.
	Name             jsontext.Value `json:"name"`
	Refusal          jsontext.Value `json:"refusal"`
	ReasoningText    jsontext.Value `json:"reasoning"`
	ReasoningContent jsontext.Value `json:"reasoning_content"`
}

// canonPart is one content part.
type canonPart struct {
	Type     string         `json:"type"`
	Text     jsontext.Value `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
	File *struct {
		FileData *string        `json:"file_data"`
		FileID   *string        `json:"file_id"`
		Filename jsontext.Value `json:"filename"`
	} `json:"file"`
	CacheControl jsontext.Value `json:"cache_control"`
}

type canonToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      jsontext.Value `json:"name"`
		Arguments jsontext.Value `json:"arguments"`
	} `json:"function"`
}

type canonTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        jsontext.Value `json:"name"`
		Description jsontext.Value `json:"description"`
		Parameters  jsontext.Value `json:"parameters"`
		Strict      *bool          `json:"strict"`
	} `json:"function"`
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

// decodeCanon reads a Chat body Inspect accepted. A member of the wrong type is the client's error.
func decodeCanon(body []byte) (*canon, error) {
	c := &canon{}
	if err := jsonx.Unmarshal(body, c); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err) //nolint:errorlint // the decoding error describes the body, not a cause to match
	}
	return c, nil
}

// checkShape holds the members the conversions copy as written to the type Chat gives them (OpenAI《Create chat
// completion》, read 2026-10-14): a tool's and a call's name and a text part's text are strings, a call's arguments a
// string, and a JSON schema reply names itself and has an object for a schema. Anything else is the client's error.
func (c *canon) checkShape() error {
	for _, tool := range c.Tools {
		if tool.Function.Name.Kind() != '"' {
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
		if call.Function.Name.Kind() != '"' || call.Function.Arguments.Kind() != '"' {
			return shapeError("a tool call without a name or string arguments")
		}
	}
	parts, err := m.parts()
	if err != nil {
		return err
	}
	for _, p := range parts {
		if p.Type == "text" && p.Text.Kind() != '"' {
			return shapeError("a text part without text")
		}
	}
	return nil
}

func shapeError(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }

// parts reads a message's content: a string is one text part, null none.
func (m *canonMessage) parts() ([]canonPart, error) {
	if isNull(m.Content) {
		return nil, nil
	}
	if m.Content.Kind() == '"' {
		return []canonPart{{Type: "text", Text: m.Content}}, nil
	}
	var parts []canonPart
	if err := jsonx.Unmarshal(m.Content, &parts); err != nil {
		return nil, fmt.Errorf("%w: a %s message's content: %v", ErrInvalid, m.Role, err) //nolint:errorlint // as in decodeCanon
	}
	return parts, nil
}

// effort is the reasoning effort the request asks for, "" for none: reasoning_effort, or OpenRouter's reasoning,
// whose enabled alone means medium (OpenRouter《Reasoning tokens》).
func (c *canon) effort() string {
	switch r := c.Reasoning; {
	case c.ReasoningEffort != nil:
		return *c.ReasoningEffort
	case r == nil:
		return ""
	case r.Effort != nil:
		return *r.Effort
	case r.Enabled != nil && *r.Enabled, r.Enabled == nil && r.MaxTokens != nil:
		return "medium"
	}
	return ""
}

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
			return &NotSupported{Reason: name + " is not converted"}
		}
	}
	for _, m := range c.Messages {
		for name := range m.Unknown {
			return &NotSupported{Reason: "a message's " + name + " is not converted"}
		}
	}
	return nil
}

// isDefault reports whether a member the conversion does not express is absent in effect: null, or the value Chat
// takes when it is left out (OpenAI《Create chat completion》, read 2026-10-14), so a client that writes the default
// is not refused.
func isDefault(name string, v jsontext.Value) bool {
	if v.Kind() == 'n' {
		return true
	}
	s := string(v)
	switch name {
	case "frequency_penalty", "presence_penalty":
		f, err := strconv.ParseFloat(s, 64)
		return err == nil && f == 0
	case "n":
		return s == "1"
	case "logprobs", "store":
		return s == "false"
	case "modalities":
		return strings.ReplaceAll(s, " ", "") == `["text"]`
	case "metadata", "logit_bias":
		return strings.TrimSpace(strings.Trim(s, "{}")) == ""
	}
	return false
}

// numberIs reports whether v is absent, null or the number want.
func numberIs(v jsontext.Value, want float64) bool {
	if len(v) == 0 || v.Kind() == 'n' {
		return true
	}
	f, err := strconv.ParseFloat(string(v), 64)
	return err == nil && f == want
}
