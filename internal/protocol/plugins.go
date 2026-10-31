package protocol

import (
	"fmt"
	"strings"
)

// Compression is the request's context-compression policy. Default follows OpenRouter's small-context default.
type Compression uint8

// The three request policies distinguish absence from an explicit opt-out.
const (
	CompressionDefault Compression = iota
	CompressionEnabled
	CompressionDisabled
)

// Compresses reports whether middle-out is enabled for this context window.
func (r *Request) Compresses(context int64) bool {
	return r.Format == Chat && (r.Compression == CompressionEnabled || r.Compression == CompressionDefault && context <= 8192)
}

// NoWebSearch says what is missing for OpenRouter-side web search. It is the sentence an unconfigured web plugin or
// :online variant is refused with, and the server tool's refusal even when a backend is configured: the tool needs the
// model-side loop, which this gateway does not run (docs/domains/gateway.md「服务端工具」).
const NoWebSearch = "no outside search service is configured"

// ExtensionError is a rejected OpenRouter plugin or transform. It preserves the numeric OpenRouter error code.
type ExtensionError struct{ Message string }

func (e *ExtensionError) Error() string { return e.Message }
func (*ExtensionError) Unwrap() error   { return ErrInvalid }

func (s *scanner) plugins() (err error) {
	defer func() {
		if err != nil {
			err = &ExtensionError{Message: bodyFault(err)}
		}
	}()
	return s.array((*scanner).plugin)
}

func (s *scanner) plugin() error {
	var (
		id      string
		enabled = true
		web     WebPlugin
		option  string // the first option written, for the error an id other than web gets
	)
	if s.dec.PeekKind() != '{' {
		return s.mismatch("a plugin object")
	}
	err := s.eachMember(func() error {
		name := string(s.name)
		switch name {
		case "id":
			v, err := s.str()
			if len(v) > 64 {
				return fmt.Errorf("plugin id is not supported")
			}
			id = string(v)
			return err
		case "enabled":
			k, err := s.flag()
			enabled = k != 'f'
			return err
		}
		if option == "" {
			option = name
		}
		return s.webOption(&web, name)
	})
	if err != nil {
		return err
	}
	if id != "web" && option != "" {
		return fmt.Errorf("plugin option %q is not supported", option)
	}
	return s.applyPlugin(id, enabled, &web)
}

// webOption reads one of the web plugin's options. It is apart from plugin so only a web plugin is read into WebPlugin.
func (s *scanner) webOption(web *WebPlugin, name string) error {
	switch name {
	case "max_results":
		n, err := s.count()
		web.MaxResults = int(n)
		return err
	case "search_prompt":
		v, err := s.str()
		if err == nil {
			web.SearchPrompt = string(v)
		}
		return err
	case "include_domains":
		return s.domains(&web.IncludeDomains)
	case "exclude_domains":
		return s.domains(&web.ExcludeDomains)
	case "engine", "mode":
		// A value here would pick among OpenRouter's engines; this deployment has one configured backend, so the
		// option is refused rather than silently ignored.
		if err := s.skip(); err != nil {
			return err
		}
		return fmt.Errorf("plugin option %q is not supported: this deployment searches through its configured backend", name)
	default:
		return fmt.Errorf("plugin option %.64q is not supported", s.name)
	}
}

// domains reads an array of host names for include_domains or exclude_domains, in lower case so one domain has one
// spelling in the query. The shape is checked here, at the boundary: a client's value must not write search operators.
func (s *scanner) domains(dst *[]string) error {
	return s.array(func(s *scanner) error {
		v, err := s.str()
		if err != nil {
			return err
		}
		d := strings.ToLower(string(v))
		if !ValidSearchDomain(d) {
			return fmt.Errorf("a domain in %s is not a plain host name", s.name)
		}
		*dst = append(*dst, d)
		return nil
	})
}

func (s *scanner) applyPlugin(id string, enabled bool, web *WebPlugin) error {
	switch id {
	case "web":
		return s.applyWeb(id, enabled, web)
	case "response-healing", "context-compression":
		if enabled && s.r.Format != Chat {
			return fmt.Errorf("plugin %q is not supported for this format", id)
		}
		if id == "response-healing" {
			s.r.ResponseHealing = enabled
			return nil
		}
		s.compressionSet = true
		s.r.Compression = CompressionDisabled
		if enabled {
			s.r.Compression = CompressionEnabled
		}
	case "file-parser":
		if enabled {
			return fmt.Errorf("plugin %q is not supported: external services are not configured", id)
		}
	default:
		return fmt.Errorf("plugin %q is not supported", id)
	}
	return nil
}

// applyWeb records a web plugin, which only Chat can carry: its results are injected into the messages and its reply
// carries the citations.
func (s *scanner) applyWeb(id string, enabled bool, web *WebPlugin) error {
	if !enabled {
		return nil
	}
	if s.r.Format != Chat {
		return fmt.Errorf("plugin %q is not supported for this format", id)
	}
	if err := CheckWebSearch(web); err != nil {
		return err
	}
	s.r.Web = web
	return nil
}

func (s *scanner) transforms() (err error) {
	defer func() {
		if err != nil {
			err = &ExtensionError{Message: bodyFault(err)}
		}
	}()
	if s.dec.PeekKind() == 'n' {
		return s.token()
	}
	s.transformsSet = true
	return s.array(func(s *scanner) error {
		v, err := s.str()
		if err != nil {
			return err
		}
		if string(v) != "middle-out" || s.r.Format != Chat {
			return fmt.Errorf("transform %.64q is not supported", v)
		}
		s.middleOut = true
		return nil
	})
}
