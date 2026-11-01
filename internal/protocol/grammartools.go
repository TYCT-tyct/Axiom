package protocol

import (
	"bytes"
	"fmt"
	"math"
)

// serverParams reads a tool's parameters. Other tools' members of that name are their own data: once type is known it
// skips them; when parameters come first, it keeps them until tool() has read type. Only web_search parses them.
func (s *scanner) serverParams() error {
	// The common order has type before parameters: parse or skip now and allocate nothing. Only when parameters come
	// first are they kept until tool() has read type; then a client tool's large schema is still its own.
	if s.p.typeSeen {
		if s.p.toolKind != "openrouter:web_search" {
			return s.skip()
		}
		v, err := s.dec.ReadValue()
		if err != nil {
			return err
		}
		if len(v) > MaxServerToolBytes {
			return fmt.Errorf("the web search parameters are larger than %d bytes", MaxServerToolBytes)
		}
		s.p.server, err = parseServerParams(v)
		return err
	}
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	if len(v) > MaxServerToolBytes {
		s.p.serverRawBig = true
		return nil
	}
	s.p.serverRaw = bytes.Clone(v)
	return nil
}

//nolint:cyclop,gocognit // one branch per tool type and its execution claim
func (s *scanner) tool() error {
	s.p = part{kind: Tools}
	if err := s.object(s.g.tool); err != nil {
		return err
	}
	if s.p.toolKind == "openrouter:web_search" {
		if s.p.serverRawBig {
			return fmt.Errorf("the web search parameters are larger than %d bytes", MaxServerToolBytes)
		}
		if s.p.server == nil {
			s.p.server = &ServerWebSearch{MaxResults: DefaultWebSearchResults}
			if s.p.serverRaw != nil {
				t, err := parseServerParams(s.p.serverRaw)
				if err != nil {
					return err
				}
				s.p.server = t
			}
		}
	}
	switch {
	case s.p.toolKind == "shell" && !s.p.localShell, s.p.toolKind == "tool_search" && !s.p.clientSearch:
		return fmt.Errorf("the %s server tool is not supported yet", s.p.toolKind)
	}
	if s.p.server != nil {
		if s.r.ServerSearch != nil {
			return fmt.Errorf("openrouter:web_search is declared more than once")
		}
		s.r.ServerSearch = s.p.server
		s.p.kind |= WebSearch
	} else if s.p.kind&WebSearch != 0 {
		s.r.NativeWeb = true
	}
	if s.p.kind&(WebSearch|CodeExecution|FileSearch) != 0 {
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
	switch s.p.kind {
	case CodeExecution:
		n = &s.r.CodeExecutions
	case FileSearch:
		n = &s.r.FileSearches
	case WebSearch, Tools, Images, Documents, Reasoning, PromptCache, StructuredOutput:
		// WebSearch keeps the default count above; the rest add no calls, and addCalls is not reached for them.
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
// model decides when to call one and the provider executes it; this gateway runs web_search's loop and refuses the
// rest, whose fetch, sandbox and image jobs would need services or sandboxes here. The namespace is recognized in every
// format so that a client's tool is refused by name before admission, not forwarded as a function.
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
	s.p.typeSeen = true
	v, err := s.str()
	if hasPrefix(v, "openrouter:") {
		if string(v) == "openrouter:web_search" {
			s.p.toolKind = string(v)
			return err
		}
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
