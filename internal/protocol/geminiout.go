package protocol

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// ChatToGemini converts a Chat request prepared for an endpoint into the generateContent body for Google's native API,
// whose path names the model and the stream (gemini.go); model is the name the call is made with, which says how the
// model takes thinking. It is GeminiToChat run the other way, over the same structs and tables. A member that cannot
// change the answer is dropped, one Gemini can say is said, and one that would change it and that Gemini cannot say is
// *NotSupported, so the call goes to another endpoint; a malformed body wraps ErrInvalid.
func ChatToGemini(body []byte, model string) ([]byte, error) {
	c, err := decodeCanon(body)
	if err != nil {
		return nil, err
	}
	if err := c.checkShape(); err != nil {
		return nil, err
	}
	g := geminiOutRequest{GenerationConfig: map[string]jsonx.Raw{}}
	if g.ServiceTier, err = c.geminiTier(); err != nil {
		return nil, err
	}
	if err := c.geminiContents(&g.geminiRequest); err != nil {
		return nil, err
	}
	if err := c.geminiTools(&g.geminiRequest); err != nil {
		return nil, err
	}
	if err := c.geminiGeneration(g.GenerationConfig, model); err != nil {
		return nil, err
	}
	if len(g.GenerationConfig) == 0 {
		g.GenerationConfig = nil
	}
	return jsonValue(g), nil
}

// geminiOutRequest is the generateContent body with the members only a request to Google holds: a Gemini client's
// request asks for none of them (GeminiToChat).
type geminiOutRequest struct {
	geminiRequest
	ServiceTier string `json:"serviceTier,omitempty"`
}

// geminiTier is Chat's service_tier as Gemini's (Google《Generating content》ServiceTier, read 2026-11-27): Gemini's
// unspecified tier is its standard one, which is Chat's default, and auto, which leaves it to the project, so neither is
// sent; flex and priority are the same words. Gemini has no other tier.
func (c *canon) geminiTier() (string, error) {
	if c.ServiceTier == nil {
		return "", nil
	}
	switch t := *c.ServiceTier; t {
	case "auto", "default":
		return "", nil
	case "flex", "priority":
		return t, nil
	default:
		return "", &NotSupported{Reason: "service_tier " + t + " has no Gemini counterpart"}
	}
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
			if err := g.instruct(m); err != nil {
				return err
			}
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

// instruct adds a system message's text to the system instruction.
func (g *geminiRequest) instruct(m canonMessage) error {
	parts, err := geminiTextParts(m.Parts)
	if err != nil {
		return err
	}
	if g.SystemInstruction == nil {
		g.SystemInstruction = &geminiContent{}
	}
	g.SystemInstruction.Parts = append(g.SystemInstruction.Parts, parts...)
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
		if text == "" {
			continue // a part must hold data, and an empty text says nothing, as Chat's empty content before calls does
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
	response := jsonx.Raw(strings.TrimSpace(text.String()))
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
			part, err := geminiImage(p.ImageURL)
			if err != nil {
				return nil, err
			}
			out = append(out, part)
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

// geminiImage is an image given inline, at the detail Gemini chooses: one by address, or at a detail the client chose,
// has no exact Gemini counterpart here.
func geminiImage(img *canonImage) (geminiPart, error) {
	media, data, ok := dataURL(img.URL)
	switch {
	case !ok:
		return geminiPart{}, &NotSupported{Reason: "Gemini takes images inline here, not by URL"}
	case img.Detail != "" && img.Detail != "auto":
		return geminiPart{}, &NotSupported{Reason: "an image's detail has no exact Gemini counterpart"}
	}
	return geminiPart{InlineData: &geminiBlob{MimeType: media, Data: data}}, nil
}

// geminiTools writes the functions and the choice among them. Gemini has no switch for parallel calls and may make
// several in a turn, so parallel_tool_calls true asks for what Gemini does, and false with tools to call, which a legacy
// functions request always is, cannot be said unless no call may be made.
func (c *canon) geminiTools(g *geminiRequest) error {
	if c.ParallelToolCalls != nil && !*c.ParallelToolCalls && len(c.Tools) > 0 && !controlIs(c.ToolChoice, "none") {
		return &NotSupported{Reason: "Gemini cannot be held to one tool call at a time"}
	}
	var fns []geminiFunction
	strict := 0
	for _, t := range c.Tools {
		fn, err := geminiFunctionOf(t)
		if err != nil {
			return err
		}
		if t.Function.Strict != nil && *t.Function.Strict {
			strict++
		}
		fns = append(fns, fn)
	}
	if len(fns) > 0 {
		g.Tools = []geminiTool{{FunctionDeclarations: fns}}
	}
	if strict > 0 && strict < len(fns) {
		return &NotSupported{Reason: "Gemini holds either all functions to their schemas or none"}
	}
	return c.geminiChoice(g, strict > 0)
}

// geminiFunctionOf is a function tool as Gemini's declaration.
func geminiFunctionOf(t canonTool) (geminiFunction, error) {
	var fn geminiFunction
	if t.Type != "function" {
		return fn, &NotSupported{Reason: "a " + t.Type + " tool has no Gemini counterpart"}
	}
	if err := jsonx.Unmarshal(t.Function.Name, &fn.Name); err != nil {
		return fn, shapeError("a tool's name is not a string")
	}
	if !isNull(t.Function.Description) {
		if err := jsonx.Unmarshal(t.Function.Description, &fn.Description); err != nil {
			return fn, shapeError("a tool's description is not a string")
		}
	}
	fn.ParametersJSONSchema = t.Function.Parameters
	return fn, nil
}

// geminiChoice writes the choice among the functions. Strict functions the model may call or not are VALIDATED, the
// mode in which Gemini holds a call to its schema (Google《Function calling》modes, read 2026-11-27); ANY, for a call
// the model must make, holds it so already.
func (c *canon) geminiChoice(g *geminiRequest, strict bool) error {
	if isNull(c.ToolChoice) && !strict {
		return nil
	}
	cfg := &geminiToolConfig{}
	cfg.FunctionCallingConfig = &struct {
		Mode                 string   `json:"mode,omitempty"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	}{}
	scalar := "auto"
	if isNull(c.ToolChoice) || jsonx.Unmarshal(c.ToolChoice, &scalar) == nil {
		mode := inverseName(geminiChoices, scalar)
		if scalar == "auto" {
			mode = "AUTO"
			if strict {
				mode = "VALIDATED"
			}
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
func (c *canon) geminiGeneration(config map[string]jsonx.Raw, model string) error {
	chat := map[string]jsonx.Raw{"temperature": c.Temperature, "top_p": c.TopP, "stop": c.Stop}
	maps.Copy(chat, c.Unknown)
	n := c.maxOutput()
	if n > geminiOutputCap(model, c.Stream != nil && *c.Stream) {
		return &NotSupported{Reason: "an output past one request's cap needs Gemini's long decode, which is not run here"}
	}
	if n > 0 {
		chat["max_tokens"] = jsonValue(n)
	}
	moveSettings(chat, config)
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
	return c.geminiThinking(config, model)
}

// geminiOutputCap is the most output one generateContent request gives (Google《Long decode》, read 2026-11-27): 64K,
// and 32K for Gemini 4 Carbon and Argon answering whole. Past it Gemini continues by a token the client sends back
// (CONTINUATION), or refuses the request, so a larger cap goes to Google's OpenAI-compatible layer, which stops at it.
func geminiOutputCap(model string, stream bool) int64 {
	model = strings.TrimPrefix(model, "models/")
	if !stream && (strings.HasPrefix(model, "gemini-4-carbon") || strings.HasPrefix(model, "gemini-4-argon")) {
		return 32768
	}
	return 65536
}

// moveSettings moves the settings both sides share from chat into Gemini's config; a lone stop is Gemini's list of one.
func moveSettings(chat, config map[string]jsonx.Raw) {
	for _, s := range geminiSettings {
		v := chat[s.chat]
		delete(chat, s.chat)
		if isNull(v) {
			continue
		}
		if s.chat == "stop" && v.Kind() == '"' {
			v = jsonValue([]jsonx.Raw{v})
		}
		config[s.gemini] = v
	}
}

func (c *canon) geminiFormat(config map[string]jsonx.Raw) error {
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

// geminiThinker is how a model takes thinking: by the levels it lists, or, without levels, by a budget within its range;
// whether a budget of 0 turns its thinking off, and whether it thinks when not asked to.
type geminiThinker struct {
	levels               []string
	minBudget, maxBudget int64
	off, thinksByDefault bool
}

// geminiThinkers are the models whose thinking the conversion writes, as Google's《Gemini thinking》lists them (read
// 2026-11-27): the levels each takes, and the 2.5 models' budget ranges and defaults. A model not listed, or a setting
// its row cannot say, is sent through Google's OpenAI-compatible layer, which maps it itself.
var geminiThinkers = map[string]geminiThinker{
	"gemini-2.5-pro":               {minBudget: 128, maxBudget: 32768, thinksByDefault: true},
	"gemini-2.5-flash":             {minBudget: 1, maxBudget: 24576, off: true, thinksByDefault: true},
	"gemini-2.5-flash-lite":        {minBudget: 512, maxBudget: 24576, off: true},
	"gemini-4-carbon":              {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-4-argon":               {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-4-neon":                {levels: []string{"low", "medium", "high", "xhigh"}, thinksByDefault: true},
	"gemini-4-hydrogen":            {levels: []string{"low", "medium", "high", "xhigh"}, thinksByDefault: true},
	"gemini-3.8-flash":             {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.6-flash":             {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.5-flash-lite":        {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-pro-preview":       {levels: []string{"low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-flash-lite":        {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3.1-flash-lite-image":  {levels: []string{"minimal", "high"}, thinksByDefault: true},
	"gemini-3-flash-preview":       {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
	"gemini-3-pro-preview":         {levels: []string{"low", "high"}, thinksByDefault: true},
	"gemini-robotics-er-2-preview": {levels: []string{"minimal", "low", "medium", "high"}, thinksByDefault: true},
}

// geminiEffortBudgets are the 2.5 models' budgets for a reasoning effort, Google's OpenAI-compatible layer's own
// (《OpenAI compatibility》Thinking, read 2026-11-27), so a request thinks as much whichever way it is sent.
var geminiEffortBudgets = map[string]int64{"minimal": 1024, "low": 1024, "medium": 8192, "high": 24576}

// geminiThinking writes the thinking the request asks for in the terms its model takes (geminiThinkers): effort none
// or reasoning disabled is a budget of 0, a reasoning budget the thinking budget, any other effort the model's level
// or budget for it; thoughts come back unless the client hid them. What the model's row cannot say exactly is not
// supported.
func (c *canon) geminiThinking(config map[string]jsonx.Raw, model string) error {
	a := c.thinkingAsked()
	if !a.asked {
		return nil
	}
	m, ok := geminiThinkers[strings.TrimPrefix(model, "models/")]
	if !ok {
		return &NotSupported{Reason: "the model's thinking settings are not known here"}
	}
	if a.effort == "none" {
		if !m.off {
			return &NotSupported{Reason: "the model's thinking cannot be turned off"}
		}
		config["thinkingConfig"] = jsonValue(requestObject{"thinkingBudget": jsonx.Raw("0")})
		return nil
	}
	t := requestObject{}
	name, amount, err := m.amount(a)
	if err != nil {
		return err
	}
	if name != "" {
		t[name] = amount
	}
	if a.thoughts {
		t["includeThoughts"] = jsonx.Raw("true")
	}
	if len(t) > 0 {
		config["thinkingConfig"] = jsonValue(t)
	}
	return nil
}

// amount is how much the model is asked to think, as the thinkingConfig member that says it, none for its default.
func (m geminiThinker) amount(a thinkingAsk) (string, jsonx.Raw, error) {
	switch {
	case a.budget != nil:
		if m.levels != nil || *a.budget < m.minBudget || *a.budget > m.maxBudget {
			return "", nil, &NotSupported{Reason: "the model takes no such thinking budget"}
		}
		return "thinkingBudget", jsonValue(*a.budget), nil
	case a.effort != "" && m.levels != nil:
		if !slices.Contains(m.levels, a.effort) {
			return "", nil, &NotSupported{Reason: "reasoning effort " + a.effort + " is not one of the model's levels"}
		}
		return "thinkingLevel", jsonValue(a.effort), nil
	case a.effort != "":
		budget, ok := geminiEffortBudgets[a.effort]
		if !ok {
			return "", nil, &NotSupported{Reason: "reasoning effort " + a.effort + " has no budget on the model"}
		}
		return "thinkingBudget", jsonValue(budget), nil
	case !m.thinksByDefault: // reasoning on, at the model's default, which is off
		return "", nil, &NotSupported{Reason: "the model does not think unless given a budget"}
	}
	return "", nil, nil
}

// thinkingAsk is what the request's reasoning settings ask of Gemini's thinking.
type thinkingAsk struct {
	asked    bool   // any reasoning setting is present
	effort   string // the effort, none for reasoning disabled
	budget   *int64 // the reasoning budget
	thoughts bool   // the thoughts come back
}

func (c *canon) thinkingAsked() thinkingAsk {
	var a thinkingAsk
	if c.ReasoningEffort != nil && *c.ReasoningEffort != "" { // an empty effort is none set, as elsewhere
		a.asked, a.effort, a.thoughts = true, *c.ReasoningEffort, true
	}
	r := c.Reasoning
	if r == nil {
		return a
	}
	a.asked, a.thoughts = true, r.Exclude == nil || !*r.Exclude
	switch {
	case r.Enabled != nil && !*r.Enabled:
		a.effort = "none"
	case r.MaxTokens != nil:
		a.budget = r.MaxTokens
	case r.Effort != nil:
		a.effort = *r.Effort
	}
	return a
}

// errGeminiStream is a Gemini stream that broke the shape a generateContent stream has.
var errGeminiStream = fmt.Errorf("%w: a Gemini stream event is not a generateContent response", ErrUpstream)
