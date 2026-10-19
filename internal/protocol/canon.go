package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: strings are kept as written, never decoded and encoded again
	"fmt"
	"slices"
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
	Type     string         `json:"type"`
	Text     jsontext.Value `json:"text"` // a refusal part's refusal too (settle)
	Refusal  jsontext.Value `json:"refusal"`
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

// decodeCanon reads a Chat body Inspect accepted. A member of the wrong type is the client's error.
func decodeCanon(body []byte) (*canon, error) {
	c := &canon{}
	if err := jsonx.Unmarshal(body, c); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalid, bodyFault(err))
	}
	for i := range c.Messages {
		if err := c.Messages[i].settle(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// settle reads the message's content into Parts once, merges the reasoning details a client streamed back, and puts
// what the other formats have no member for where OpenRouter puts it: a name before the text as "name: " (OpenRouter
// 《API reference》"Requests", read 2026-10-16), and a refusal as a part of its own, which Messages writes as text and
// Responses as its refusal part.
func (m *canonMessage) settle() error {
	var err error
	if m.Parts, err = m.readParts(); err != nil {
		return err
	}
	for i, p := range m.Parts {
		if p.Type == "refusal" { // what it says is its refusal, which the conversions write as they write text
			m.Parts[i].Text = p.Refusal
		}
	}
	if m.ReasoningDetails, err = mergeDetails(m.ReasoningDetails); err != nil {
		return err
	}
	switch {
	case !isNull(m.Name) && m.Name.Kind() != '"', !isNull(m.Refusal) && m.Refusal.Kind() != '"':
		return shapeError("a name or refusal that is not a string")
	case !isNull(m.Refusal):
		m.Parts = append(m.Parts, canonPart{Type: "refusal", Text: m.Refusal})
	}
	if !isNull(m.Name) && m.Role != "tool" { // a tool result's name is its call's, which its ID already says
		m.prependName()
	}
	return nil
}

// prependName puts "name: " before the message's first text or refusal, or as text of its own when it has none.
func (m *canonMessage) prependName() {
	prefix := slices.Concat(m.Name[:len(m.Name)-1], []byte(`: "`))
	for i, p := range m.Parts {
		if (p.Type == "text" || p.Type == "refusal") && p.Text.Kind() == '"' {
			m.Parts[i].Text = slices.Concat(prefix[:len(prefix)-1], p.Text[1:])
			return
		}
	}
	m.Parts = slices.Insert(m.Parts, 0, canonPart{Type: "text", Text: prefix})
}

// mergeDetails joins the reasoning details a client hands back as it streamed them, an entry per chunk: entries of
// the same type and index are one detail (OpenRouter《Reasoning tokens》"Streaming Response", read 2026-10-15), whose
// text and summary are its pieces in order and whose signature, data, ID and format are the ones given. Entries
// without an index stand alone. Each piece is copied once, so many pieces cost what their bytes do.
func mergeDetails(details []canonReasoningDetail) ([]canonReasoningDetail, error) {
	var out []canonReasoningDetail
	var text, summary []joined
	at := map[string]int{} // by type and index
	for _, d := range details {
		key := d.Type + "\x00" + string(d.Index)
		i, seen := at[key]
		if !seen || isNull(d.Index) {
			at[key], i = len(out), len(out)
			out, text, summary = append(out, d), append(text, joined{}), append(summary, joined{})
		} else {
			m := &out[i]
			m.Signature, m.Data, m.ID = given(m.Signature, d.Signature), given(m.Data, d.Data), given(m.ID, d.ID)
			if m.Format == "" {
				m.Format = d.Format
			}
		}
		if err := text[i].add(d.Text); err != nil {
			return nil, err
		}
		if err := summary[i].add(d.Summary); err != nil {
			return nil, err
		}
	}
	for i := range out {
		out[i].Text, out[i].Summary = text[i].value(out[i].Text), summary[i].value(out[i].Summary)
	}
	return out, nil
}

// joined gathers the pieces of a JSON string: their contents, quotes off, one after another, as each escape lies
// whole within its piece.
type joined struct {
	pieces int
	only   jsontext.Value // the piece, while there is one
	body   []byte
}

func (j *joined) add(piece jsontext.Value) error {
	switch {
	case isNull(piece):
		return nil
	case piece.Kind() != '"':
		return shapeError("a reasoning detail's text that is not a string")
	}
	j.pieces++
	j.only = piece
	j.body = append(j.body, piece[1:len(piece)-1]...)
	return nil
}

// value is the joined string: the piece as written when there was one, unset as it was when none.
func (j *joined) value(unset jsontext.Value) jsontext.Value {
	switch j.pieces {
	case 0:
		return unset
	case 1:
		return j.only
	}
	return slices.Concat([]byte{'"'}, j.body, []byte{'"'})
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
		switch {
		case (p.Type == "text" || p.Type == "refusal") && p.Text.Kind() != '"':
			return shapeError("a text part without text or a refusal part without its refusal")
		case p.Type == "refusal" && m.Role != "assistant":
			return &NotSupported{Reason: "a refusal part in a " + m.Role + " message"}
		}
	}
	return nil
}

func shapeError(what string) error { return fmt.Errorf("%w: %s", ErrInvalid, what) }

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
