package protocol

import (
	"bytes"
	"fmt"
	"math"

	"axiom/internal/catalog"
)

// grammar is what Inspect reads in one format (README.md「请求摘要」 has the sources). Messages, items and content parts
// share the part members: a message or item holds its parts in content or output, and a Messages tool_result holds
// more parts in its content, as does a document's source of type content, so following those members reaches every
// part and nothing else. Members such as a
// tool's input schema or a tool call's arguments are never entered, so a key there named cache_control or type is
// the client's data, not ours.
type grammar struct {
	top, part, tool fields
	parts           map[string]Capabilities           // content part types that need a capability
	servers         []serverTool                      // tool types billed per call; other tools are Tools
	tiers           map[string]catalog.ServiceTierSet // service_tier values; 0 is the vendor's default
	input           catalog.ItemSet                   // what input is billed as before any cache marker
	compaction      string                            // the type, or prefix of the type, of a context_management entry that compacts
}

type serverTool struct {
	prefix      string // Anthropic versions its tool types, as in web_search_20260209
	kind        Capabilities
	unsupported bool
}

var grammars = [...]grammar{
	Messages: {
		top: withProvider(fields{
			"model": (*scanner).model, "models": (*scanner).models, "stream": (*scanner).stream,
			"max_tokens": (*scanner).maxOutput, "system": (*scanner).content, "messages": (*scanner).content, "tools": (*scanner).tools,
			"thinking": (*scanner).reasoning, "output_config": func(s *scanner) error { return s.object(messagesOutputFields) }, "output_format": (*scanner).format,
			"service_tier": (*scanner).serviceTier, "speed": (*scanner).speed, "cache_control": (*scanner).cacheControl,
			"context_management": func(s *scanner) error { return s.object(editsHolder) },
			"mcp_servers":        (*scanner).mcpServers,
		}),
		part: fields{
			"type": (*scanner).partType, "content": (*scanner).result, "cache_control": (*scanner).cacheControl,
			"source": (*scanner).source,
		},
		tool:    fields{"type": (*scanner).toolType, "max_uses": (*scanner).maxUses, "cache_control": (*scanner).cacheControl},
		parts:   map[string]Capabilities{"image": Images, "document": Documents},
		servers: []serverTool{{"web_search_", WebSearch, false}, {"code_execution_", CodeExecution, false}, {"web_fetch_", 0, true}, {"mcp_toolset", 0, true}, {"tool_search_tool_", 0, true}, {"advisor_", 0, true}},
		tiers:   map[string]catalog.ServiceTierSet{"auto": 0, "standard_only": 1 << catalog.ServiceTierStandard},
		input:   1 << catalog.ItemInput,
		// compact_20260112 is the one that compacts; clear_tool_uses and clear_thinking only drop content.
		compaction: "compact_",
	},
	Responses: {
		top: withProvider(fields{
			"model": (*scanner).model, "models": (*scanner).models, "stream": (*scanner).stream,
			"max_output_tokens": (*scanner).maxOutput, "input": (*scanner).content,
			"tools": (*scanner).tools, "max_tool_calls": (*scanner).maxToolCalls,
			"reasoning": (*scanner).reasoning, "text": (*scanner).withFormat, "service_tier": (*scanner).serviceTier,
			"previous_response_id": (*scanner).previous, "context_management": (*scanner).edits,
		}),
		part: fields{
			"type": (*scanner).partType, "content": (*scanner).result, "output": (*scanner).result,
			"image_url": (*scanner).imageURL, "file_data": (*scanner).fileData,
		},
		tool: fields{
			"type":        (*scanner).toolType,
			"execution":   func(s *scanner) (err error) { s.p.clientSearch, err = s.is("client"); return err },
			"environment": func(s *scanner) error { return s.object(shellEnvironmentFields) },
		},
		parts:   map[string]Capabilities{"input_image": Images, "computer_screenshot": Images, "input_file": Documents},
		servers: []serverTool{{"web_search", WebSearch, false}, {"code_interpreter", CodeExecution, false}, {"mcp", 0, true}, {"file_search", 0, true}, {"image_generation", 0, true}, {"programmatic_tool_calling", 0, true}},
		tiers:   openAITiers,
		input:   openAIInput, compaction: "compaction",
	},
	Chat: {
		top: withProvider(fields{
			"model": (*scanner).model, "models": (*scanner).models, "stream": (*scanner).stream,
			"max_tokens": (*scanner).maxOutput, "max_completion_tokens": (*scanner).maxOutput, "n": (*scanner).choices, "messages": (*scanner).content,
			"tools": (*scanner).tools, "functions": (*scanner).tools, "web_search_options": (*scanner).webSearchOptions,
			"function_call":    func(s *scanner) error { s.r.legacy = s.r.legacy || s.dec.PeekKind() != 'n'; return s.skip() },
			"tool_choice":      func(s *scanner) error { s.r.modernTools = s.r.modernTools || s.dec.PeekKind() != 'n'; return s.skip() },
			"reasoning_effort": (*scanner).reasoningEffort, "reasoning": (*scanner).reasoning,
			"response_format": (*scanner).format, "service_tier": (*scanner).serviceTier,
		}),
		part: fields{
			"function_call": func(s *scanner) error {
				// Root object, messages array, message object; content parts are caller data.
				if s.dec.StackDepth() == 3 {
					s.r.legacy = s.r.legacy || s.dec.PeekKind() != 'n'
				}
				return s.skip()
			},
			"role": func(s *scanner) error {
				if s.dec.StackDepth() != 3 || s.dec.PeekKind() != '"' {
					return s.skip()
				}
				old, err := s.is("function")
				s.r.legacy = s.r.legacy || old
				return err
			},
			"type": (*scanner).partType, "content": (*scanner).content, "cache_control": (*scanner).cacheControl,
			"image_url": func(s *scanner) error { return s.object(chatImageFields) },
			"file":      func(s *scanner) error { return s.object(chatFileFields) },
		},
		tool:  fields{"type": (*scanner).toolType},
		parts: map[string]Capabilities{"image_url": Images, "file": Documents},
		tiers: openAITiers,
		input: openAIInput,
	},
}

var (
	openAITiers = map[string]catalog.ServiceTierSet{
		"auto": 0, "default": 1 << catalog.ServiceTierStandard,
		"flex": 1 << catalog.ServiceTierFlex, "priority": 1 << catalog.ServiceTierPriority,
	}
	// OpenAI caches a prefix without being asked, and from GPT-5.6 on bills the write at 1.25 times input (OpenAI
	// 《Prompt caching》, read 2026-10-14). Whether a model bills writes is its schedule's business.
	openAIInput    = catalog.ItemSet(1<<catalog.ItemInput | 1<<catalog.ItemCacheWrite)
	anthropicSpeed = map[string]catalog.ServiceTierSet{"standard": 0, "fast": 1 << catalog.ServiceTierFast}

	shellEnvironmentFields = fields{"type": func(s *scanner) (err error) { s.p.localShell, err = s.is("local"); return err }}

	sourceFields = fields{
		"type": func(s *scanner) (err error) { s.p.binary, err = s.is("base64"); return err },
		"data": (*scanner).data,
		// A document whose source is of type content holds parts, which count as the request's own.
		"content": (*scanner).content,
	}
	cacheFields = fields{"ttl": func(s *scanner) error {
		long, err := s.is("1h")
		if long {
			s.r.InputItems.Add(catalog.ItemCacheWrite1h)
		}
		return err
	}}
	reasoningFields = fields{
		"type": func(s *scanner) error {
			v, err := s.str()
			s.p.off = s.p.off || string(v) == "disabled"
			s.p.on = s.p.on || string(v) == "adaptive" || string(v) == "enabled"
			return err
		}, // Messages' thinking
		"effort": func(s *scanner) error {
			v, err := s.str()
			s.p.off = s.p.off || string(v) == "none"
			s.p.on = s.p.on || len(v) > 0
			return err
		}, // OpenAI, OpenRouter
		"display": func(s *scanner) error { return s.when("omitted", &s.p.hidden) }, // Messages' thinking
		"enabled": func(s *scanner) error {
			k, err := s.flag()
			s.p.off = s.p.off || k == 'f'
			s.p.on = s.p.on || k == 't'
			return err
		},
		"max_tokens":    func(s *scanner) error { n, err := s.count(); s.p.on = s.p.on || n > 0; return err },
		"budget_tokens": func(s *scanner) error { n, err := s.count(); s.p.on = s.p.on || n > 0; return err },
		"exclude":       func(s *scanner) error { k, err := s.flag(); s.p.hidden = s.p.hidden || k == 't'; return err },
	}
	editsHolder = fields{"edits": (*scanner).edits}
	editFields  = fields{"type": func(s *scanner) error {
		v, err := s.str()
		if v != nil && hasPrefix(v, s.g.compaction) {
			s.r.Compacts = true
		}
		return err
	}}
	chatImageFields      = fields{"url": (*scanner).imageURL}
	chatFileFields       = fields{"file_data": (*scanner).fileData}
	messagesOutputFields = fields{"format": (*scanner).format, "effort": (*scanner).reasoningEffort}
	formatHolder         = fields{"format": (*scanner).format}
	formatFields         = fields{"type": func(s *scanner) error {
		v, err := s.str()
		if string(v) == "json_schema" || string(v) == "json_object" {
			s.r.Needs |= StructuredOutput
		}
		return err
	}}
)

func (s *scanner) model() error {
	v, err := s.str()
	s.r.Model = string(v)
	return err
}

// maxModels bounds the alternates a request lists, so what Inspect allocates for them does not grow with the body.
const maxModels = 8

func (s *scanner) models() error {
	return s.array(func(s *scanner) error {
		if len(s.r.Models) == maxModels {
			return fmt.Errorf("models lists more than %d models", maxModels)
		}
		v, err := s.str()
		s.r.Models = append(s.r.Models, string(v))
		return err
	})
}

func (s *scanner) stream() error {
	k, err := s.flag()
	s.r.Stream = k == 't'
	return err
}

// maxOutput reads an output cap. Chat has two names for it; the larger bounds the reply either way.
func (s *scanner) maxOutput() error {
	n, err := s.count()
	s.r.MaxOutput = max(s.r.MaxOutput, n)
	return err
}

func (s *scanner) choices() error {
	n, err := s.count()
	s.r.Choices = max(1, n)
	return err
}

func (s *scanner) previous() error {
	v, err := s.str()
	s.r.Previous = string(v)
	return err
}

// maxToolCalls reads Responses' cap on all built-in tool calls together, which Inspect applies to each at the end.
func (s *scanner) maxToolCalls() (err error) {
	s.callCap, err = s.count()
	return err
}

// content reads a message's content or a tool result's output: a string, which TextBytes already counts, or parts.
func (s *scanner) content() error {
	if s.dec.PeekKind() == '"' {
		return s.skip()
	}
	return s.array((*scanner).contentPart)
}

// result reads a Messages or Responses part's content or output, which may also be one part, as a server tool's
// result or a computer call's screenshot is. Members come in any order, so the value's shape tells which, not the
// enclosing type.
func (s *scanner) result() error {
	if s.dec.PeekKind() == '{' {
		return s.contentPart()
	}
	return s.content()
}

func (s *scanner) contentPart() error {
	if s.r.Format == Chat && s.dec.StackDepth() == 2 {
		s.r.MessageCount++
	}
	outer := s.p
	s.p = part{}
	err := s.object(s.g.part)
	p := s.p
	s.p = outer
	if err != nil {
		return err
	}
	s.r.Needs |= p.kind
	if p.kind == Images {
		s.r.Images++
	}
	if p.kind == Documents {
		s.r.Documents++
	}
	if p.binary {
		s.r.TextBytes -= p.payload
	}
	return nil
}

func (s *scanner) partType() error {
	v, err := s.str()
	s.p.kind = s.g.parts[string(v)]
	return err
}

func (s *scanner) data() error {
	v, err := s.raw()
	s.p.payload = int64(len(v))
	return err
}

// fileData reads a file's contents, base64 with or without a data URL around it.
func (s *scanner) fileData() error {
	s.p.binary = true
	return s.data()
}

// imageURL reads an image's URL, which carries the image itself when it is a data URL. An escaped "data:" counts
// as text, which only raises the bound.
func (s *scanner) imageURL() error {
	v, err := s.raw()
	s.p.payload = int64(len(v))
	s.p.binary = hasPrefix(v, `"data:`)
	return err
}

func (s *scanner) cacheControl() error {
	if s.dec.PeekKind() != 'n' {
		s.r.Needs |= PromptCache
		s.r.InputItems.Add(catalog.ItemCacheWrite)
	}
	return s.object(cacheFields)
}

// source reads a Messages part's source: an object for images and documents, a string for a search_result, whose
// source names where the text came from (Anthropic《Search results》). The shape tells which, as in result.
func (s *scanner) source() error {
	if s.dec.PeekKind() == '"' {
		return s.skip()
	}
	return s.object(sourceFields)
}

// edits reads the entries of a context_management list, each of which may compact the context.
func (s *scanner) edits() error {
	return s.array(func(s *scanner) error { return s.object(editFields) })
}

func (s *scanner) tools() error {
	legacy := string(s.name) == "functions"
	return s.array(func(s *scanner) error {
		if s.r.Format == Chat {
			s.r.legacy = s.r.legacy || legacy
			s.r.modernTools = s.r.modernTools || !legacy
		}
		return s.tool()
	})
}

func (s *scanner) tool() error {
	s.p = part{kind: Tools}
	if err := s.object(s.g.tool); err != nil {
		return err
	}
	switch {
	case s.p.toolKind == "shell" && !s.p.localShell, s.p.toolKind == "tool_search" && !s.p.clientSearch:
		return fmt.Errorf("the %s server tool is not supported yet", s.p.toolKind)
	}
	if s.p.kind&(WebSearch|CodeExecution) != 0 {
		if err := s.addCalls(); err != nil {
			return err
		}
	}
	s.r.Needs |= s.p.kind
	return nil
}

// addCalls adds a server tool's limit to its kind's total. Limits of two tools of a kind add up, and a tool without
// one leaves the total to the model's default. A total past int64 is refused: wrapped, it would bound nothing.
func (s *scanner) addCalls() error {
	n := &s.r.WebSearches
	if s.p.kind == CodeExecution {
		n = &s.r.CodeExecutions
	}
	switch {
	case s.r.Needs.Has(s.p.kind) && (*n == 0 || s.p.max == 0):
		*n = 0
	case s.p.max > math.MaxInt64-*n:
		return fmt.Errorf("max_uses add up past %d", int64(math.MaxInt64))
	default:
		*n += s.p.max
	}
	return nil
}

// openrouterTools are the server tool types OpenRouter documents (OpenRouter《Server Tools》, read 2026-10-29). The
// model decides when to call one and the provider executes it, resampling the model with the result: a loop this
// gateway does not run, and one whose search, fetch, sandbox and image jobs would also need a service or a sandbox
// here. The namespace is recognized in every format so that a client's tool is refused by name before admission,
// rather than forwarded as a function the upstream would ignore (docs/domains/gateway.md「服务端工具」).
var openrouterTools = map[string]struct{}{
	"openrouter:web_search": {}, "openrouter:web_fetch": {}, "openrouter:datetime": {},
	"openrouter:image_generation": {}, "openrouter:apply_patch": {}, "openrouter:shell": {},
	"openrouter:bash": {}, "openrouter:fusion": {}, "openrouter:advisor": {}, "openrouter:subagent": {},
	"openrouter:experimental__search_models": {}, "openrouter:tool_search": {}, "openrouter:files": {},
}

// openrouterTool refuses one tool of OpenRouter's namespace. A documented type says what is missing; a name the
// namespace does not document is told so, because OpenRouter adds tools and a request may carry a name from a newer
// provider release.
func openrouterTool(name string) error {
	if _, ok := openrouterTools[name]; !ok {
		return fmt.Errorf("%.64q is not an OpenRouter server tool", name)
	}
	if name == "openrouter:web_search" {
		return fmt.Errorf("the %s server tool is not supported: %s", name, NoWebSearch)
	}
	return fmt.Errorf("the %s server tool is not supported yet: this gateway runs no OpenRouter-side tool loop", name)
}

func (s *scanner) toolType() error {
	v, err := s.str()
	if hasPrefix(v, "openrouter:") {
		return openrouterTool(string(v))
	}
	if s.r.Format == Responses {
		switch string(v) {
		case "shell":
			s.p.toolKind = "shell"
		case "tool_search":
			s.p.toolKind = "tool_search"
		}
	}
	for _, t := range s.g.servers {
		if hasPrefix(v, t.prefix) {
			if t.unsupported {
				return fmt.Errorf("the %s server tool is not supported yet", t.prefix)
			}
			s.p.kind = t.kind
		}
	}
	return err
}

// Even a connector with a tool allowlist can run repeatedly; no complete quote exists for it yet.
func (s *scanner) mcpServers() error {
	return s.array(func(*scanner) error {
		return fmt.Errorf("mcp_servers is not supported yet")
	})
}

func (s *scanner) maxUses() (err error) {
	s.p.max, err = s.count()
	return err
}

// reasoning reads a reasoning setting: on unless it says off, and in the stream unless it says otherwise.
func (s *scanner) reasoning() error {
	if s.dec.PeekKind() == 'n' {
		return s.skip()
	}
	s.p = part{}
	if err := s.object(reasoningFields); err != nil {
		return err
	}
	s.reasoningOff = s.reasoningOff || s.p.off
	if s.p.on && !s.p.off {
		s.r.Needs |= Reasoning
	}
	s.r.HidesReasoning = s.p.hidden
	return nil
}

func (s *scanner) reasoningEffort() error {
	v, err := s.str()
	if v != nil {
		if string(v) == "none" {
			s.reasoningOff = true
		} else {
			s.r.Needs |= Reasoning
		}
	}
	return err
}

func (s *scanner) withFormat() error { return s.object(formatHolder) }

func (s *scanner) format() error { return s.object(formatFields) }

func (s *scanner) webSearchOptions() error {
	if s.dec.PeekKind() != 'n' {
		s.r.Needs |= WebSearch
	}
	return s.skip()
}

func (s *scanner) serviceTier() error { return s.pick(s.g.tiers, &s.tiers) }

func (s *scanner) speed() error { return s.pick(anthropicSpeed, &s.fast) }

// pick reads a service tier value. One no table lists cannot be priced, so the request is refused.
func (s *scanner) pick(values map[string]catalog.ServiceTierSet, dst *catalog.ServiceTierSet) error {
	v, err := s.str()
	if v == nil || err != nil {
		return err
	}
	t, ok := values[string(v)]
	if !ok {
		return fmt.Errorf("%s %q is not supported", s.name, v)
	}
	*dst = t
	return nil
}

// when reads a string and sets flag when it is lit.
func (s *scanner) when(lit string, flag *bool) error {
	is, err := s.is(lit)
	*flag = *flag || is
	return err
}

func hasPrefix(v []byte, prefix string) bool {
	return len(v) >= len(prefix) && string(v[:len(prefix)]) == prefix
}

// withProvider adds the provider member, which the three formats share, to the members of a format's top level.
func withProvider(f fields) fields {
	f["provider"] = (*scanner).provider
	f["plugins"] = (*scanner).plugins
	f["transforms"] = (*scanner).transforms
	return f
}

// maxProvider bounds the provider object a request carries: the gateway reads it whole, and the parameters it holds
// are a handful of short names and numbers.
const maxProvider = 4 << 10

// provider keeps OpenRouter's provider object as written; what it means is for the gateway, which knows the routing.
func (s *scanner) provider() error {
	if ok, err := s.present('{', "an object"); !ok {
		return err
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	if len(v) > maxProvider {
		return fmt.Errorf("%s is larger than %d bytes", s.name, maxProvider)
	}
	s.r.Provider = bytes.Clone(v)
	return nil
}
