package protocol

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
)

// Gemini's generateContent is not a format a call is routed by: it maps onto Chat at the edge, one way for a Gemini
// client (GeminiToChat, GeminiReply) and the other way for an upstream that speaks it (ChatToGemini, ChatFromGemini).
// Both directions read the same structs and tables below, so a member converts the same way whichever side has it
// (README.md「Gemini」).

// geminiRequest is a generateContent body. What it holds besides these members is refused by name: a member we do not
// convert would change the answer without the client knowing.
type geminiRequest struct {
	Contents          []geminiContent      `json:"contents"`
	SystemInstruction *geminiContent       `json:"systemInstruction,omitempty"`
	Tools             []geminiTool         `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig    `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]jsonx.Raw `json:"generationConfig,omitempty"`
	SafetySettings    jsonx.Raw            `json:"safetySettings,omitzero"` // read and ignored: Chat has no counterpart, and the provider's own policy applies
	Unknown           map[string]jsonx.Raw `json:",embed"`
}

type geminiContent struct {
	Role    string               `json:"role,omitempty"`
	Parts   []geminiPart         `json:"parts"`
	Unknown map[string]jsonx.Raw `json:",embed"`
}

type geminiPart struct {
	Text             *string              `json:"text,omitempty"`
	Thought          bool                 `json:"thought,omitzero"`
	ThoughtSignature string               `json:"thoughtSignature,omitempty"`
	InlineData       *geminiBlob          `json:"inlineData,omitempty"`
	FileData         *geminiFile          `json:"fileData,omitempty"`
	FunctionCall     *geminiCall          `json:"functionCall,omitempty"`
	FunctionResponse *geminiResult        `json:"functionResponse,omitempty"`
	Unknown          map[string]jsonx.Raw `json:",embed"`
}

type geminiBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFile struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type geminiCall struct {
	ID   string    `json:"id,omitempty"`
	Name string    `json:"name"`
	Args jsonx.Raw `json:"args,omitzero"`
}

type geminiResult struct {
	ID       string    `json:"id,omitempty"`
	Name     string    `json:"name"`
	Response jsonx.Raw `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunction     `json:"functionDeclarations,omitempty"`
	Unknown              map[string]jsonx.Raw `json:",embed"` // googleSearch, codeExecution, urlContext: Google runs them
}

type geminiFunction struct {
	Name                 string    `json:"name"`
	Description          string    `json:"description,omitempty"`
	Parameters           jsonx.Raw `json:"parameters,omitzero"` // OpenAPI's subset, types in capitals
	ParametersJSONSchema jsonx.Raw `json:"parametersJsonSchema,omitzero"`
}

type geminiToolConfig struct {
	FunctionCallingConfig *struct {
		Mode                 string   `json:"mode,omitempty"`
		AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
	} `json:"functionCallingConfig,omitempty"`
}

// geminiSettings are the generation settings that are the same number or list on both sides, by their Gemini and their
// Chat name.
var geminiSettings = [...]struct{ gemini, chat string }{
	{"temperature", "temperature"}, {"topP", "top_p"}, {"topK", "top_k"}, {"maxOutputTokens", "max_tokens"},
	{"stopSequences", "stop"}, {"seed", "seed"}, {"presencePenalty", "presence_penalty"}, {"frequencyPenalty", "frequency_penalty"},
}

// geminiChoices are the function calling modes and Chat's tool choices; ANY with named functions is the choice of one,
// or required among the tools it names (geminiChoice).
var geminiChoices = map[string]string{"AUTO": "auto", "VALIDATED": "auto", "NONE": "none", "ANY": "required"}

// skipThoughtSignature is the signature Google documents for a function call whose own was lost, such as one another
// model made: Gemini then skips the check instead of refusing the turn.
const skipThoughtSignature = "skip_thought_signature_validator"

// GeminiOptions are what a Gemini client's request says that Chat cannot: whether it wants its thoughts back, which a
// Chat reply always carries when the model gives them.
type GeminiOptions struct {
	Thoughts bool
}

// GeminiToChat converts a Gemini client's generateContent body into the Chat request the call runs as, for model,
// streamed or not. Errors wrap ErrInvalid and say what cannot be converted.
func GeminiToChat(body []byte, model string, stream bool) ([]byte, GeminiOptions, error) {
	var g geminiRequest
	if err := jsonx.Unmarshal(body, &g); err != nil {
		return nil, GeminiOptions{}, invaliderr(err)
	}
	if err := unknownMember("request", g.Unknown); err != nil {
		return nil, GeminiOptions{}, err
	}
	// Gemini has no priority tier a client could ask for: the call is the standard one, as the Messages conversion's is.
	dst := requestObject{"model": jsonValue(model), "stream": jsonValue(stream), "service_tier": jsonValue("default")}
	if stream {
		dst["stream_options"] = jsonx.Raw(`{"include_usage":true}`)
	}
	var messages []requestObject
	if g.SystemInstruction != nil {
		content, err := g.SystemInstruction.chatContent()
		if err != nil {
			return nil, GeminiOptions{}, err
		}
		messages = append(messages, requestObject{"role": jsonValue("system"), "content": content})
	}
	turns, err := geminiTurns(g.Contents)
	if err != nil {
		return nil, GeminiOptions{}, err
	}
	dst["messages"] = jsonValue(append(messages, turns...))
	if err := g.chatTools(dst); err != nil {
		return nil, GeminiOptions{}, err
	}
	opts, err := geminiGeneration(g.GenerationConfig, dst)
	if err != nil {
		return nil, GeminiOptions{}, err
	}
	return jsonValue(dst), opts, nil
}

func unknownMember(where string, members map[string]jsonx.Raw) error {
	if len(members) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the Gemini %s member %s is not supported", ErrInvalid, where, slices.Min(slices.Collect(maps.Keys(members))))
}

// chatContent is a system instruction's text as Chat content.
func (c *geminiContent) chatContent() (jsonx.Raw, error) {
	var parts []requestObject
	for _, p := range c.Parts {
		if p.Text == nil || p.Thought || p.InlineData != nil || p.FileData != nil || p.FunctionCall != nil || p.FunctionResponse != nil {
			return nil, fmt.Errorf("%w: a Gemini system instruction holds only text", ErrInvalid)
		}
		parts = append(parts, requestObject{"type": jsonValue("text"), "text": jsonValue(*p.Text)})
	}
	return jsonValue(parts), nil
}

// geminiTurns converts the contents. A call without an ID is given one by its place, which a later request repeats,
// so a conversation's earlier turns convert the same each time; a response pairs with the call of its ID, or else the
// earliest unanswered call of its name, as Gemini pairs them.
func geminiTurns(contents []geminiContent) ([]requestObject, error) {
	var out []requestObject
	pending := map[string][]string{} // unanswered call IDs by name
	for i, c := range contents {
		if err := unknownMember("content", c.Unknown); err != nil {
			return nil, err
		}
		var err error
		switch c.Role {
		case "model":
			out, err = geminiModelTurn(out, c, i, pending)
		case "user", "", "function", "tool":
			out, err = geminiUserTurn(out, c, pending)
		default:
			err = fmt.Errorf("%w: a Gemini content's role %q is not user or model", ErrInvalid, c.Role)
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func geminiModelTurn(out []requestObject, c geminiContent, at int, pending map[string][]string) ([]requestObject, error) {
	var text strings.Builder
	var calls []requestObject
	for j, p := range c.Parts {
		if err := unknownMember("part", p.Unknown); err != nil {
			return nil, err
		}
		switch {
		case p.Thought: // a thought summary is shown, not replayed: the signature on the call is what Gemini checks
		case p.FunctionCall != nil:
			id := cmp.Or(p.FunctionCall.ID, "call_"+strconv.Itoa(at)+"_"+strconv.Itoa(j))
			args := p.FunctionCall.Args
			if isNull(args) {
				args = jsonx.Raw("{}")
			}
			call := requestObject{"id": jsonValue(id), "type": jsonValue("function"),
				"function": jsonValue(requestObject{"name": jsonValue(p.FunctionCall.Name), "arguments": jsonValue(string(args))})}
			if p.ThoughtSignature != "" {
				call["extra_content"] = googleExtra(p.ThoughtSignature)
			}
			calls = append(calls, call)
			pending[p.FunctionCall.Name] = append(pending[p.FunctionCall.Name], id)
		case p.Text != nil:
			text.WriteString(*p.Text)
		default:
			return nil, fmt.Errorf("%w: a Gemini model turn holds only text, thoughts and function calls", ErrInvalid)
		}
	}
	msg := requestObject{"role": jsonValue("assistant"), "content": jsonValue(text.String())}
	if len(calls) > 0 {
		msg["tool_calls"] = jsonValue(calls)
	}
	return append(out, msg), nil
}

func geminiUserTurn(out []requestObject, c geminiContent, pending map[string][]string) ([]requestObject, error) {
	var parts []requestObject
	for _, p := range c.Parts {
		if err := unknownMember("part", p.Unknown); err != nil {
			return nil, err
		}
		switch {
		case p.FunctionResponse != nil:
			result, err := geminiResultMessage(p.FunctionResponse, pending)
			if err != nil {
				return nil, err
			}
			out = append(out, result)
		case p.Text != nil && !p.Thought:
			parts = append(parts, requestObject{"type": jsonValue("text"), "text": jsonValue(*p.Text)})
		case p.InlineData != nil:
			part, err := geminiMedia(p.InlineData.MimeType, "data:"+p.InlineData.MimeType+";base64,"+p.InlineData.Data, p.InlineData.Data)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case p.FileData != nil && strings.HasPrefix(p.FileData.MimeType, "image/"):
			parts = append(parts, requestObject{"type": jsonValue("image_url"), "image_url": jsonValue(requestObject{"url": jsonValue(p.FileData.FileURI)})})
		default:
			return nil, fmt.Errorf("%w: a Gemini user part must be text, inline data, an image file or a function response", ErrInvalid)
		}
	}
	if len(parts) > 0 {
		out = append(out, requestObject{"role": jsonValue("user"), "content": jsonValue(parts)})
	}
	return out, nil
}

// geminiMedia is inline data as a Chat part: an image, a document or audio Chat takes inline.
func geminiMedia(mime, url, data string) (requestObject, error) {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return requestObject{"type": jsonValue("image_url"), "image_url": jsonValue(requestObject{"url": jsonValue(url)})}, nil
	case mime == "application/pdf":
		return requestObject{"type": jsonValue("file"), "file": jsonValue(requestObject{"file_data": jsonValue(url)})}, nil
	case mime == "audio/wav" || mime == "audio/mp3" || mime == "audio/mpeg":
		format := strings.TrimPrefix(mime, "audio/")
		if format == "mpeg" {
			format = "mp3"
		}
		return requestObject{"type": jsonValue("input_audio"), "input_audio": jsonValue(requestObject{"data": jsonValue(data), "format": jsonValue(format)})}, nil
	}
	return nil, fmt.Errorf("%w: inline data of type %q is not supported", ErrInvalid, mime)
}

func geminiResultMessage(r *geminiResult, pending map[string][]string) (requestObject, error) {
	queue := pending[r.Name]
	at := slices.Index(queue, r.ID)
	if r.ID == "" || at < 0 {
		at = 0
	}
	if len(queue) == 0 {
		return nil, fmt.Errorf("%w: the function response for %q answers no function call", ErrInvalid, r.Name)
	}
	id := queue[at]
	pending[r.Name] = slices.Delete(queue, at, at+1)
	content := r.Response
	if isNull(content) {
		content = jsonx.Raw("{}")
	}
	return requestObject{"role": jsonValue("tool"), "tool_call_id": jsonValue(id), "content": jsonValue(string(content))}, nil
}

// chatTools converts the function declarations and the calling mode. Google's own tools run on Google's side and have
// no Chat counterpart.
func (g *geminiRequest) chatTools(dst requestObject) error {
	var tools []requestObject
	for _, t := range g.Tools {
		if len(t.Unknown) > 0 {
			return fmt.Errorf("%w: the Gemini %s tool runs only on Google's side and is not supported", ErrInvalid, slices.Min(slices.Collect(maps.Keys(t.Unknown))))
		}
		for _, f := range t.FunctionDeclarations {
			tool, err := chatFunction(f)
			if err != nil {
				return err
			}
			tools = append(tools, tool)
		}
	}
	if cfg := g.ToolConfig; cfg != nil && cfg.FunctionCallingConfig != nil && cfg.FunctionCallingConfig.Mode != "" {
		var err error
		if tools, err = geminiChoice(cfg.FunctionCallingConfig.Mode, cfg.FunctionCallingConfig.AllowedFunctionNames, tools, dst); err != nil {
			return err
		}
	}
	if len(tools) > 0 {
		dst["tools"] = jsonValue(tools)
	}
	return nil
}

// chatFunction is a function declaration as a Chat tool, its parameters in JSON Schema.
func chatFunction(f geminiFunction) (requestObject, error) {
	params := f.ParametersJSONSchema
	if isNull(params) && !isNull(f.Parameters) {
		var err error
		if params, err = jsonSchemaOf(f.Parameters); err != nil {
			return nil, err
		}
	}
	if isNull(params) {
		params = jsonx.Raw(`{"type":"object","properties":{}}`)
	}
	fn := requestObject{"name": jsonValue(f.Name), "parameters": params}
	if f.Description != "" {
		fn["description"] = jsonValue(f.Description)
	}
	return requestObject{"type": jsonValue("function"), "function": jsonValue(fn)}, nil
}

// geminiChoice writes the calling mode as Chat's tool choice. ANY with allowed names keeps only those tools, as the
// model may call no other: one is the forced choice of it, several are required among them.
func geminiChoice(mode string, allowed []string, tools []requestObject, dst requestObject) ([]requestObject, error) {
	choice, ok := geminiChoices[mode]
	if !ok {
		return nil, fmt.Errorf("%w: the function calling mode %q is not supported", ErrInvalid, mode)
	}
	dst["tool_choice"] = jsonValue(choice)
	if mode != "ANY" || len(allowed) == 0 {
		return tools, nil
	}
	if len(allowed) == 1 {
		dst["tool_choice"] = jsonValue(requestObject{"type": jsonValue("function"), "function": jsonValue(requestObject{"name": jsonValue(allowed[0])})})
		return tools, nil
	}
	return slices.DeleteFunc(tools, func(t requestObject) bool {
		var fn struct {
			Name string `json:"name"`
		}
		return jsonx.Unmarshal(t["function"], &fn) != nil || !slices.Contains(allowed, fn.Name)
	}), nil
}

// geminiGeneration converts generationConfig: the settings both sides share, the reply's format, and thinking.
func geminiGeneration(config map[string]jsonx.Raw, dst requestObject) (GeminiOptions, error) {
	var opts GeminiOptions
	config = maps.Clone(config)
	for _, s := range geminiSettings {
		if v, ok := config[s.gemini]; ok {
			dst[s.chat] = v
			delete(config, s.gemini)
		}
	}
	if v, ok := config["candidateCount"]; ok && string(v) != "1" {
		return opts, fmt.Errorf("%w: candidateCount other than 1 is not supported", ErrInvalid)
	}
	delete(config, "candidateCount")
	if v, ok := config["responseModalities"]; ok && !strings.EqualFold(string(v), `["TEXT"]`) {
		return opts, fmt.Errorf("%w: responseModalities other than TEXT are not supported", ErrInvalid)
	}
	delete(config, "responseModalities")
	if err := geminiFormat(config, dst); err != nil {
		return opts, err
	}
	if v, ok := config["thinkingConfig"]; ok {
		delete(config, "thinkingConfig")
		var err error
		if opts, err = geminiThinking(v, dst); err != nil {
			return opts, err
		}
	}
	return opts, unknownMember("generationConfig", config)
}

func geminiFormat(config map[string]jsonx.Raw, dst requestObject) error {
	mime, schema, jsonSchema := config["responseMimeType"], config["responseSchema"], config["responseJsonSchema"]
	delete(config, "responseMimeType")
	delete(config, "responseSchema")
	delete(config, "responseJsonSchema")
	switch {
	case isNull(mime) || string(mime) == `"text/plain"`:
		if !isNull(schema) || !isNull(jsonSchema) {
			return fmt.Errorf("%w: a response schema needs responseMimeType application/json", ErrInvalid)
		}
		return nil
	case string(mime) != `"application/json"`:
		return fmt.Errorf("%w: responseMimeType %s is not supported", ErrInvalid, mime)
	}
	if isNull(jsonSchema) && !isNull(schema) {
		var err error
		if jsonSchema, err = jsonSchemaOf(schema); err != nil {
			return err
		}
	}
	if isNull(jsonSchema) {
		dst["response_format"] = jsonx.Raw(`{"type":"json_object"}`)
		return nil
	}
	dst["response_format"] = jsonValue(requestObject{"type": jsonValue("json_schema"),
		"json_schema": jsonValue(requestObject{"name": jsonValue("response"), "schema": jsonSchema})})
	return nil
}

// geminiThinking converts thinkingConfig: a budget of 0 turns thinking off, -1 leaves it to the model, a positive one
// is OpenRouter's reasoning budget, and a level is the effort. Whether thoughts come back is the reply's business.
func geminiThinking(v jsonx.Raw, dst requestObject) (GeminiOptions, error) {
	var t struct {
		IncludeThoughts bool                 `json:"includeThoughts"`
		ThinkingBudget  *int64               `json:"thinkingBudget"`
		ThinkingLevel   string               `json:"thinkingLevel"`
		Unknown         map[string]jsonx.Raw `json:",embed"`
	}
	if err := jsonx.Unmarshal(v, &t); err != nil {
		return GeminiOptions{}, invaliderr(err)
	}
	if err := unknownMember("thinkingConfig", t.Unknown); err != nil {
		return GeminiOptions{}, err
	}
	switch {
	case t.ThinkingLevel != "":
		dst["reasoning_effort"] = jsonValue(strings.ToLower(t.ThinkingLevel))
	case t.ThinkingBudget == nil, *t.ThinkingBudget == -1:
	case *t.ThinkingBudget == 0:
		dst["reasoning_effort"] = jsonValue("none")
	case *t.ThinkingBudget > 0:
		dst["reasoning"] = jsonValue(requestObject{"max_tokens": jsonValue(*t.ThinkingBudget)})
	default:
		return GeminiOptions{}, fmt.Errorf("%w: thinkingBudget %d is not valid", ErrInvalid, *t.ThinkingBudget)
	}
	return GeminiOptions{Thoughts: t.IncludeThoughts}, nil
}
