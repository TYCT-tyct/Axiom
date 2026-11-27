package bedrock

import (
	"errors"
	"strings"

	"axiom/internal/platform/jsonx"
)

// A model on Bedrock that is not Anthropic's is called through Converse
// (https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_Converse.html), which the gateway reaches in
// Chat: converse turns a Chat Completions body into Converse's, and converseanswer.go turns the answer back. A Chat
// member that cannot change the answer is dropped and one Converse can say is said; one that could change it and that
// Converse cannot say is refused, never dropped, and another endpoint is tried (Sign), as protocol refuses the same
// members in Messages: a body that would lose what the client asked of the answer is not sent.

// errUnconvertible is a Chat body Converse cannot say.
var errUnconvertible = errors.New("bedrock: the Chat body has no Converse form")

// chatRequest is a Chat Completions body as far as Converse can say it; jsonx.UnmarshalKnown refuses any other member,
// at any depth.
type chatRequest struct {
	Messages            []chatMessage `json:"messages"`
	MaxTokens           *int64        `json:"max_tokens"`
	MaxCompletionTokens *int64        `json:"max_completion_tokens"`
	Temperature         *float64      `json:"temperature"`
	TopP                *float64      `json:"top_p"`
	Stop                jsonx.Raw     `json:"stop"`
	Stream              bool          `json:"stream"`
	Tools               []chatTool    `json:"tools"`
	ToolChoice          jsonx.Raw     `json:"tool_choice"`
	ParallelToolCalls   *bool         `json:"parallel_tool_calls"`
	ReasoningEffort     *string       `json:"reasoning_effort"`
	// Read and refused unless absent or Chat's default (inert): Converse has no member for them, and any other value
	// asks for an answer it cannot give: several choices, token probabilities, a seed, a penalty or bias in sampling,
	// audio, a tier, a verbosity or a reply format.
	N                jsonx.Raw `json:"n"`
	Seed             jsonx.Raw `json:"seed"`
	Logprobs         jsonx.Raw `json:"logprobs"`
	FrequencyPenalty jsonx.Raw `json:"frequency_penalty"`
	PresencePenalty  jsonx.Raw `json:"presence_penalty"`
	LogitBias        jsonx.Raw `json:"logit_bias"`
	Modalities       jsonx.Raw `json:"modalities"`
	ServiceTier      jsonx.Raw `json:"service_tier"`
	Verbosity        jsonx.Raw `json:"verbosity"`
	ResponseFormat   jsonx.Raw `json:"response_format"`
	// Read and not sent, as they cannot change the answer: the model is in the path, a Converse stream always ends with
	// its usage, the identifiers are the gateway's opaque ones, top_logprobs says nothing without logprobs, and storing
	// and its metadata are OpenAI's own.
	Model            jsonx.Raw `json:"model"`
	StreamOptions    jsonx.Raw `json:"stream_options"`
	User             jsonx.Raw `json:"user"`
	SafetyIdentifier jsonx.Raw `json:"safety_identifier"`
	PromptCacheKey   jsonx.Raw `json:"prompt_cache_key"`
	TopLogprobs      jsonx.Raw `json:"top_logprobs"`
	Store            jsonx.Raw `json:"store"`
	Metadata         jsonx.Raw `json:"metadata"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Name       string     `json:"name"`
	Content    jsonx.Raw  `json:"content"`
	ToolCalls  []chatCall `json:"tool_calls"`
	ToolCallID string     `json:"tool_call_id"`
	// Read and not sent: an earlier answer's reasoning, as the answer gave it (converseanswer.go), has no signature to
	// replay it with, and a model reasons afresh each turn.
	ReasoningContent jsonx.Raw `json:"reasoning_content"`
}

type chatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string    `json:"name"`
		Description string    `json:"description"`
		Parameters  jsonx.Raw `json:"parameters"`
		Strict      bool      `json:"strict"`
	} `json:"function"`
}

// converseRequest is Converse's body.
type converseRequest struct {
	Messages        []converseMessage `json:"messages"`
	System          []textBlock       `json:"system,omitzero"`
	InferenceConfig inferenceConfig   `json:"inferenceConfig,omitzero"`
	ToolConfig      toolConfig        `json:"toolConfig,omitzero"`
	Additional      additional        `json:"additionalModelRequestFields,omitzero"`
}

type converseMessage struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// block is a content block, one member set; the answer's blocks are read into it too.
type block struct {
	Text             *string     `json:"text,omitzero"`
	Image            *image      `json:"image,omitzero"`
	ToolUse          *toolUse    `json:"toolUse,omitzero"`
	ToolResult       *toolResult `json:"toolResult,omitzero"`
	ReasoningContent *struct {
		ReasoningText *struct {
			Text string `json:"text"`
		} `json:"reasoningText"`
	} `json:"reasoningContent,omitzero"`
}

type textBlock struct {
	Text string `json:"text"`
}

type image struct {
	Format string `json:"format"`
	Source struct {
		Bytes string `json:"bytes"`
	} `json:"source"`
}

type toolUse struct {
	ToolUseID string    `json:"toolUseId"`
	Name      string    `json:"name"`
	Input     jsonx.Raw `json:"input"`
}

type toolResult struct {
	ToolUseID string      `json:"toolUseId"`
	Content   []textBlock `json:"content"`
}

type inferenceConfig struct {
	MaxTokens     *int64   `json:"maxTokens,omitzero"`
	Temperature   *float64 `json:"temperature,omitzero"`
	TopP          *float64 `json:"topP,omitzero"`
	StopSequences []string `json:"stopSequences,omitzero"`
}

type toolConfig struct {
	Tools      []tool     `json:"tools"`
	ToolChoice toolChoice `json:"toolChoice,omitzero"`
}

type tool struct {
	ToolSpec toolSpec `json:"toolSpec"`
}

type toolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description,omitzero"`
	InputSchema struct {
		JSON jsonx.Raw `json:"json"`
	} `json:"inputSchema"`
}

type toolChoice struct {
	Auto *struct{} `json:"auto,omitzero"`
	Any  *struct{} `json:"any,omitzero"`
	Tool *named    `json:"tool,omitzero"`
}

type named struct {
	Name string `json:"name"`
}

// noParameters is the schema of a function that declares none: Converse requires one.
var noParameters = jsonx.Raw(`{"type":"object","properties":{}}`)

// converse is the Converse body of a Chat body for model, and whether it asks for a stream. parallel_tool_calls may
// only be true when there are tools: Converse cannot hold a model to one call at a time.
func converse(body []byte, model string) ([]byte, bool, error) {
	var in chatRequest
	if jsonx.UnmarshalKnown(body, &in) != nil || !inert(in) {
		return nil, false, errUnconvertible
	}
	if in.ParallelToolCalls != nil && !*in.ParallelToolCalls && len(in.Tools) > 0 {
		return nil, false, errUnconvertible
	}
	var out converseRequest
	var err error
	if out.System, out.Messages, err = conversation(in.Messages); err != nil {
		return nil, false, err
	}
	if out.InferenceConfig, err = inference(in); err != nil {
		return nil, false, err
	}
	if out.ToolConfig, err = tools(in.Tools, in.ToolChoice); err != nil {
		return nil, false, err
	}
	if out.Additional, err = reasoning(model, in.ReasoningEffort, out.InferenceConfig); err != nil {
		return nil, false, err
	}
	b, err := jsonx.MarshalCanonical(out)
	return b, in.Stream, err
}

// conversation is the system prompt and the turns: the system and developer messages that lead are the system prompt,
// and a later one, which has no place in Converse, is refused (turn).
func conversation(messages []chatMessage) ([]textBlock, []converseMessage, error) {
	var system []textBlock
	for len(messages) > 0 && (messages[0].Role == "system" || messages[0].Role == "developer") {
		texts, err := textsOf(messages[0].parts())
		if err != nil {
			return nil, nil, err
		}
		system, messages = append(system, texts...), messages[1:]
	}
	turns, err := turnsOf(messages)
	return system, turns, err
}

// turnsOf is the messages as Converse's turns: a tool's result is the user's turn, and the messages of one role in a
// row are one turn, as Converse has turns alternate.
func turnsOf(messages []chatMessage) ([]converseMessage, error) {
	var turns []converseMessage
	for _, m := range messages {
		role, blocks, err := turn(m)
		if err != nil {
			return nil, err
		}
		if n := len(turns); n > 0 && turns[n-1].Role == role {
			turns[n-1].Content = append(turns[n-1].Content, blocks...)
			continue
		}
		turns = append(turns, converseMessage{Role: role, Content: blocks})
	}
	if len(turns) == 0 {
		return nil, errUnconvertible
	}
	return turns, nil
}

// turn is a message's role in Converse and its blocks. An empty text is left out, which Converse refuses; a message
// left with no block is refused.
func turn(m chatMessage) (string, []block, error) {
	switch {
	case m.Role == "user" && m.ToolCalls == nil && m.ToolCallID == "":
		blocks, err := userBlocks(m.parts())
		return "user", blocks, err
	case m.Role == "tool" && m.ToolCalls == nil && m.ToolCallID != "":
		texts, err := textsOf(m.parts())
		if err != nil {
			return "", nil, err
		}
		return "user", []block{{ToolResult: &toolResult{ToolUseID: m.ToolCallID, Content: texts}}}, nil
	case m.Role == "assistant" && m.ToolCallID == "":
		blocks, err := assistantBlocks(m)
		return "assistant", blocks, err
	}
	return "", nil, errUnconvertible
}

// assistantBlocks is what the assistant said, then the calls it made.
func assistantBlocks(m chatMessage) ([]block, error) {
	texts, err := textsOf(m.parts())
	if err != nil {
		return nil, err
	}
	var blocks []block
	for _, t := range texts {
		if t.Text != "" {
			blocks = append(blocks, block{Text: &t.Text})
		}
	}
	for _, c := range m.ToolCalls {
		use, err := toolUseOf(c)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block{ToolUse: use})
	}
	if len(blocks) == 0 {
		return nil, errUnconvertible
	}
	return blocks, nil
}

// userBlocks is a user message's content: text, and images given inline as a data URL, which Converse takes as bytes;
// an image by address, which Bedrock does not fetch, is refused.
func userBlocks(parts []chatPart, err error) ([]block, error) {
	if err != nil {
		return nil, err
	}
	var blocks []block
	for _, p := range parts {
		b, err := userBlock(p)
		if err != nil {
			return nil, err
		}
		if b != (block{}) {
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return nil, errUnconvertible
	}
	return blocks, nil
}

// userBlock is one part of a user message's content; none for an empty text.
func userBlock(p chatPart) (block, error) {
	switch {
	case p.Type == "text" && p.Text != nil && p.ImageURL == nil:
		if *p.Text == "" {
			return block{}, nil
		}
		return block{Text: p.Text}, nil
	case p.Type == "image_url" && p.ImageURL != nil && p.Text == nil && p.ImageURL.Detail != "low": // no low detail
		img, err := imageOf(p.ImageURL.URL)
		return block{Image: img}, err
	}
	return block{}, errUnconvertible
}

// imageFormats are Converse's image formats by media type.
var imageFormats = map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/gif": "gif", "image/webp": "webp"}

// imageOf is the image a base64 data URL holds.
func imageOf(url string) (*image, error) {
	media, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,")
	format := imageFormats[media]
	if !ok || format == "" || !strings.HasPrefix(url, "data:") {
		return nil, errUnconvertible
	}
	img := &image{Format: format}
	img.Source.Bytes = data
	return img, nil
}

// textsOf is a content that may only be text: a string, or text parts.
func textsOf(parts []chatPart, err error) ([]textBlock, error) {
	if err != nil {
		return nil, err
	}
	texts := make([]textBlock, 0, len(parts))
	for _, p := range parts {
		if p.Type != "text" || p.Text == nil || p.ImageURL != nil {
			return nil, errUnconvertible
		}
		texts = append(texts, textBlock{Text: *p.Text})
	}
	return texts, nil
}

// absent says a member is missing or null.
func absent(v jsonx.Raw) bool { return len(v) == 0 || v.Kind() == 'n' }

// parts is the message's content as parts. Its name, which Converse has no member for, goes before its first text as
// "name: ", or is a text of its own when it has none, as protocol writes a name in Messages; a tool result's name is
// its call's, which its ID already says.
func (m chatMessage) parts() ([]chatPart, error) {
	parts, err := partsOf(m.Content)
	if err != nil || m.Name == "" || m.Role == "tool" {
		return parts, err
	}
	for i, p := range parts {
		if p.Type == "text" && p.Text != nil {
			named := m.Name + ": " + *p.Text
			parts[i].Text = &named
			return parts, nil
		}
	}
	named := m.Name + ": "
	return append([]chatPart{{Type: "text", Text: &named}}, parts...), nil
}

// partsOf is a content as parts: a string is one text part, none or null no part.
func partsOf(content jsonx.Raw) ([]chatPart, error) {
	if absent(content) {
		return nil, nil
	}
	if content.Kind() == '"' {
		var s string
		if jsonx.Unmarshal(content, &s) != nil {
			return nil, errUnconvertible
		}
		return []chatPart{{Type: "text", Text: &s}}, nil
	}
	var parts []chatPart
	if jsonx.UnmarshalKnown(content, &parts) != nil {
		return nil, errUnconvertible
	}
	return parts, nil
}

// toolUseOf is a call the assistant made: its arguments, a JSON object as a string, are the input.
func toolUseOf(c chatCall) (*toolUse, error) {
	input := jsonx.Raw(`{}`)
	if c.Function.Arguments != "" {
		if jsonx.Unmarshal([]byte(c.Function.Arguments), &input) != nil {
			return nil, errUnconvertible
		}
	}
	if c.Type != "function" || c.ID == "" || c.Function.Name == "" || input.Kind() != '{' {
		return nil, errUnconvertible
	}
	return &toolUse{ToolUseID: c.ID, Name: c.Function.Name, Input: input}, nil
}

// inference is the sampling: max_completion_tokens before the max_tokens it replaces, and stop, one string or several,
// as stop sequences.
func inference(in chatRequest) (inferenceConfig, error) {
	c := inferenceConfig{MaxTokens: in.MaxCompletionTokens, Temperature: in.Temperature, TopP: in.TopP}
	if c.MaxTokens == nil {
		c.MaxTokens = in.MaxTokens
	}
	if absent(in.Stop) {
		return c, nil
	}
	if in.Stop.Kind() == '"' {
		c.StopSequences = make([]string, 1)
		if jsonx.Unmarshal(in.Stop, &c.StopSequences[0]) != nil {
			return c, errUnconvertible
		}
		return c, nil
	}
	if jsonx.Unmarshal(in.Stop, &c.StopSequences) != nil {
		return c, errUnconvertible
	}
	return c, nil
}

// tools is the functions the model may call and the choice among them. Converse has no choice of none and no strict
// schema, so both are refused; a function without parameters takes none.
func tools(fns []chatTool, choice jsonx.Raw) (toolConfig, error) {
	tc, err := toolChoiceOf(choice)
	switch {
	case err != nil:
		return toolConfig{}, err
	case len(fns) == 0 && tc != (toolChoice{}):
		return toolConfig{}, errUnconvertible
	case len(fns) == 0:
		return toolConfig{}, nil
	}
	c := toolConfig{Tools: make([]tool, len(fns)), ToolChoice: tc}
	for i, f := range fns {
		params := f.Function.Parameters
		if f.Type != "function" || f.Function.Name == "" || f.Function.Strict || !absent(params) && params.Kind() != '{' {
			return toolConfig{}, errUnconvertible
		}
		if absent(params) {
			params = noParameters
		}
		spec := &c.Tools[i].ToolSpec
		spec.Name, spec.Description, spec.InputSchema.JSON = f.Function.Name, f.Function.Description, params
	}
	return c, nil
}

// toolChoiceOf is Chat's tool_choice as Converse's: auto, required as any, or one function by name; the zero choice
// for none given.
func toolChoiceOf(choice jsonx.Raw) (toolChoice, error) {
	if absent(choice) {
		return toolChoice{}, nil
	}
	if choice.Kind() == '"' {
		var s string
		if jsonx.Unmarshal(choice, &s) != nil {
			return toolChoice{}, errUnconvertible
		}
		switch s {
		case "auto":
			return toolChoice{Auto: &struct{}{}}, nil
		case "required":
			return toolChoice{Any: &struct{}{}}, nil
		}
		return toolChoice{}, errUnconvertible
	}
	var fn struct {
		Type     string `json:"type"`
		Function named  `json:"function"`
	}
	if jsonx.UnmarshalKnown(choice, &fn) != nil || fn.Type != "function" || fn.Function.Name == "" {
		return toolChoice{}, errUnconvertible
	}
	return toolChoice{Tool: &fn.Function}, nil
}
