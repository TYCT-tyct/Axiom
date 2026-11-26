package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"fmt"
	"strings"

	"axiom/internal/platform/jsonx"
)

// ChatToGemini converts a Chat request prepared for an endpoint into the generateContent body for Google's native API,
// whose path names the model and the stream (gemini.go). It is GeminiToChat run the other way, over the same structs
// and tables. What Gemini cannot express is *NotSupported; a malformed body wraps ErrInvalid.
func ChatToGemini(body []byte) ([]byte, error) {
	c, err := decodeCanon(body)
	if err != nil {
		return nil, err
	}
	if err := c.checkShape(); err != nil {
		return nil, err
	}
	g := geminiRequest{GenerationConfig: map[string]jsontext.Value{}}
	if err := c.geminiContents(&g); err != nil {
		return nil, err
	}
	if err := c.geminiTools(&g); err != nil {
		return nil, err
	}
	if err := c.geminiGeneration(g.GenerationConfig); err != nil {
		return nil, err
	}
	if len(g.GenerationConfig) == 0 {
		g.GenerationConfig = nil
	}
	return jsonValue(g), nil
}

// geminiContents writes the system messages that lead as the system instruction and the rest as contents, the results
// of a turn's calls together in one user content as Gemini wants them.
func (c *canon) geminiContents(g *geminiRequest) error {
	names := map[string]string{} // a call's name by its ID, which a result in Chat does not repeat
	leading := true
	for _, m := range c.Messages {
		if isSystem(m.Role) {
			if !leading {
				return &NotSupported{Reason: "Gemini takes system instructions only before the conversation"}
			}
			parts, err := geminiTextParts(m.Parts)
			if err != nil {
				return err
			}
			if g.SystemInstruction == nil {
				g.SystemInstruction = &geminiContent{}
			}
			g.SystemInstruction.Parts = append(g.SystemInstruction.Parts, parts...)
			continue
		}
		leading = false
		role, parts, err := geminiTurn(m, names)
		if err != nil {
			return err
		}
		if n := len(g.Contents); n > 0 && g.Contents[n-1].Role == role {
			g.Contents[n-1].Parts = append(g.Contents[n-1].Parts, parts...)
		} else {
			g.Contents = append(g.Contents, geminiContent{Role: role, Parts: parts})
		}
	}
	return nil
}

func geminiTurn(m canonMessage, names map[string]string) (string, []geminiPart, error) {
	switch m.Role {
	case "assistant":
		parts, err := geminiTextParts(m.Parts)
		if err != nil {
			return "", nil, err
		}
		for i, call := range m.ToolCalls {
			part, err := geminiCallPart(call, i == 0)
			if err != nil {
				return "", nil, err
			}
			names[call.ID] = part.FunctionCall.Name
			parts = append(parts, part)
		}
		return "model", parts, nil
	case "tool":
		id := ""
		if m.ToolCallID != nil {
			id = *m.ToolCallID
		}
		part, err := geminiResultPart(id, names[id], m.Parts)
		return "user", []geminiPart{part}, err
	case "user":
		parts, err := geminiUserParts(m.Parts)
		return "user", parts, err
	}
	return "", nil, &NotSupported{Reason: "a " + m.Role + " message has no Gemini counterpart"}
}

func geminiTextParts(parts []canonPart) ([]geminiPart, error) {
	var out []geminiPart
	for _, p := range parts {
		if p.Type != "text" && p.Type != "refusal" {
			return nil, &NotSupported{Reason: "a " + p.Type + " part has no Gemini counterpart here"}
		}
		var text string
		if err := jsonx.Unmarshal(p.Text, &text); err != nil {
			return nil, shapeError("a text that is not a string")
		}
		out = append(out, geminiPart{Text: &text})
	}
	return out, nil
}

// geminiCallPart is a call as a function call part. The first call of a turn carries the thought signature Gemini
// checks: its own, or the documented one that skips the check for a call another model made.
func geminiCallPart(call canonToolCall, first bool) (geminiPart, error) {
	var name string
	if err := jsonx.Unmarshal(call.Function.Name, &name); err != nil {
		return geminiPart{}, shapeError("a tool call's name is not a string")
	}
	args, err := arguments(call.Function.Arguments)
	if err != nil {
		return geminiPart{}, err
	}
	part := geminiPart{FunctionCall: &geminiCall{ID: call.ID, Name: name, Args: args}}
	if part.ThoughtSignature = call.signature(); part.ThoughtSignature == "" && first {
		part.ThoughtSignature = skipThoughtSignature
	}
	return part, nil
}

// signature is the thought signature Google's endpoint put on the call, if any.
func (call canonToolCall) signature() string {
	var extra struct {
		Google struct {
			ThoughtSignature string `json:"thought_signature"`
		} `json:"google"`
	}
	if isNull(call.ExtraContent) || jsonx.Unmarshal(call.ExtraContent, &extra) != nil {
		return ""
	}
	return extra.Google.ThoughtSignature
}

// geminiResultPart is a tool's result as a function response: a JSON object as it is, any other text under result.
func geminiResultPart(id, name string, parts []canonPart) (geminiPart, error) {
	if name == "" {
		return geminiPart{}, &NotSupported{Reason: "a tool result answers no call before it: " + id}
	}
	texts, err := geminiTextParts(parts)
	if err != nil {
		return geminiPart{}, err
	}
	var text strings.Builder
	for _, t := range texts {
		text.WriteString(*t.Text)
	}
	response := jsontext.Value(strings.TrimSpace(text.String()))
	if len(response) == 0 || response[0] != '{' || jsonx.Validate(response) != nil {
		response = jsonValue(map[string]string{"result": text.String()})
	}
	return geminiPart{FunctionResponse: &geminiResult{ID: id, Name: name, Response: response}}, nil
}

func geminiUserParts(parts []canonPart) ([]geminiPart, error) {
	var out []geminiPart
	for _, p := range parts {
		switch {
		case p.Type == "text":
			texts, err := geminiTextParts([]canonPart{p})
			if err != nil {
				return nil, err
			}
			out = append(out, texts...)
		case p.Type == "image_url" && p.ImageURL != nil:
			media, data, ok := dataURL(p.ImageURL.URL)
			if !ok {
				return nil, &NotSupported{Reason: "Gemini takes images inline, not by URL"}
			}
			out = append(out, geminiPart{InlineData: &geminiBlob{MimeType: media, Data: data}})
		case p.Type == "file" && p.File != nil && p.File.FileData != nil:
			media, data, ok := dataURL(*p.File.FileData)
			if !ok {
				return nil, shapeError("a file's data is not a base64 data URL")
			}
			out = append(out, geminiPart{InlineData: &geminiBlob{MimeType: media, Data: data}})
		default:
			return nil, &NotSupported{Reason: "a " + p.Type + " part has no Gemini counterpart"}
		}
	}
	return out, nil
}

// geminiTools writes the functions and the choice among them. Gemini has no switch for parallel calls, so
// parallel_tool_calls is not sent: the client of a converted call handles several calls as its protocol allows.
func (c *canon) geminiTools(g *geminiRequest) error {
	var fns []geminiFunction
	for _, t := range c.Tools {
		if t.Type != "function" {
			return &NotSupported{Reason: "a " + t.Type + " tool has no Gemini counterpart"}
		}
		var fn geminiFunction
		if err := jsonx.Unmarshal(t.Function.Name, &fn.Name); err != nil {
			return shapeError("a tool's name is not a string")
		}
		if !isNull(t.Function.Description) {
			if err := jsonx.Unmarshal(t.Function.Description, &fn.Description); err != nil {
				return shapeError("a tool's description is not a string")
			}
		}
		fn.ParametersJSONSchema = t.Function.Parameters
		fns = append(fns, fn)
	}
	if len(fns) > 0 {
		g.Tools = []geminiTool{{FunctionDeclarations: fns}}
	}
	return c.geminiChoice(g)
}

func (c *canon) geminiChoice(g *geminiRequest) error {
	if isNull(c.ToolChoice) {
		return nil
	}
	cfg := &geminiToolConfig{}
	cfg.FunctionCallingConfig = &struct {
		Mode                 string   `json:"mode,omitempty"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	}{}
	var scalar string
	if jsonx.Unmarshal(c.ToolChoice, &scalar) == nil {
		mode := inverseName(geminiChoices, scalar)
		if scalar == "auto" {
			mode = "AUTO"
		}
		if mode == "" {
			return &NotSupported{Reason: "tool_choice " + scalar + " has no Gemini counterpart"}
		}
		cfg.FunctionCallingConfig.Mode = mode
	} else {
		var named struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := jsonx.Unmarshal(c.ToolChoice, &named); err != nil || named.Type != "function" || named.Function.Name == "" {
			return &NotSupported{Reason: "tool_choice has no Gemini counterpart"}
		}
		cfg.FunctionCallingConfig.Mode, cfg.FunctionCallingConfig.AllowedFunctionNames = "ANY", []string{named.Function.Name}
	}
	g.ToolConfig = cfg
	return nil
}

// geminiGeneration writes the settings, the reply's format and thinking; a member Gemini has no counterpart for is
// refused unless it is Chat's default or one only Axiom reads (identities, caching keys, the usage it always asks for).
func (c *canon) geminiGeneration(config map[string]jsontext.Value) error {
	chat := map[string]jsontext.Value{"temperature": c.Temperature, "top_p": c.TopP, "stop": c.Stop}
	for name, v := range c.Unknown {
		chat[name] = v
	}
	if n := c.maxOutput(); n > 0 {
		chat["max_tokens"] = jsonValue(n)
	}
	for _, s := range geminiSettings {
		v := chat[s.chat]
		delete(chat, s.chat)
		if isNull(v) {
			continue
		}
		if s.chat == "stop" && v.Kind() == '"' {
			v = jsonValue([]jsontext.Value{v})
		}
		config[s.gemini] = v
	}
	for name, v := range chat {
		if !isNull(v) && name != "max_completion_tokens" && !isDefault(name, v) {
			return &NotSupported{Reason: name + " has no Gemini counterpart"}
		}
	}
	if !isNull(c.Verbosity) {
		return &NotSupported{Reason: "verbosity has no Gemini counterpart"}
	}
	if err := c.geminiFormat(config); err != nil {
		return err
	}
	return c.geminiThinking(config)
}

func (c *canon) geminiFormat(config map[string]jsontext.Value) error {
	f := c.ResponseFormat
	switch {
	case f == nil || f.Type == "text":
	case f.Type == "json_object":
		config["responseMimeType"] = jsonValue("application/json")
	case f.Type == "json_schema" && f.JSONSchema != nil:
		config["responseMimeType"], config["responseJsonSchema"] = jsonValue("application/json"), f.JSONSchema.Schema
	default:
		return &NotSupported{Reason: "response_format " + f.Type + " has no Gemini counterpart"}
	}
	return nil
}

// geminiThinking is geminiThinking of gemini.go the other way: effort none or reasoning disabled is a budget of 0, a
// reasoning budget is the thinking budget, any other effort the level; thoughts come back unless the client hid them.
func (c *canon) geminiThinking(config map[string]jsontext.Value) error {
	t := requestObject{}
	effort := ""
	if c.ReasoningEffort != nil {
		effort = *c.ReasoningEffort
	}
	if r := c.Reasoning; r != nil {
		switch {
		case r.Enabled != nil && !*r.Enabled:
			effort = "none"
		case r.MaxTokens != nil:
			t["thinkingBudget"] = jsonValue(*r.MaxTokens)
		case r.Effort != nil:
			effort = *r.Effort
		}
		if r.Exclude == nil || !*r.Exclude {
			t["includeThoughts"] = jsontext.Value("true")
		}
	} else if effort != "" && effort != "none" {
		t["includeThoughts"] = jsontext.Value("true")
	}
	switch effort {
	case "":
	case "none":
		t = requestObject{"thinkingBudget": jsontext.Value("0")}
	case "minimal", "low", "medium", "high":
		t["thinkingLevel"] = jsonValue(effort)
	default:
		return &NotSupported{Reason: "reasoning effort " + effort + " has no Gemini counterpart"}
	}
	if len(t) > 0 {
		config["thinkingConfig"] = jsonValue(t)
	}
	return nil
}

// errGeminiStream is a Gemini stream that broke the shape a generateContent stream has.
var errGeminiStream = fmt.Errorf("%w: a Gemini stream event is not a generateContent response", ErrUpstream)
