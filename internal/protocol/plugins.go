package protocol

import (
	"fmt"
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

// NoWebSearch says what is missing for OpenRouter-side web search. Every way a request asks for it — the deprecated
// web plugin, the :online variant and the openrouter:web_search server tool — is refused with this one sentence, so
// the three cannot drift apart while no service is configured (docs/domains/gateway.md「插件与提示词变换」).
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
	var id string
	enabled := true
	if s.dec.PeekKind() != '{' {
		return s.mismatch("a plugin object")
	}
	err := s.eachMember(func() error {
		switch string(s.name) {
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
		default:
			return fmt.Errorf("plugin option %.64q is not supported", s.name)
		}
	})
	if err != nil {
		return err
	}
	return s.applyPlugin(id, enabled)
}

func (s *scanner) applyPlugin(id string, enabled bool) error {
	switch id {
	case "response-healing", "context-compression":
		if enabled && s.r.Format != Chat {
			return fmt.Errorf("plugin %q is not supported for this format", id)
		}
	case "web":
		if enabled {
			return fmt.Errorf("plugin %q is not supported: %s", id, NoWebSearch)
		}
	case "file-parser":
		if enabled {
			return fmt.Errorf("plugin %q is not supported: external services are not configured", id)
		}
	default:
		return fmt.Errorf("plugin %q is not supported", id)
	}
	if id == "response-healing" {
		s.r.ResponseHealing = enabled
	}
	if id == "context-compression" {
		s.compressionSet = true
		s.r.Compression = CompressionDisabled
		if enabled {
			s.r.Compression = CompressionEnabled
		}
	}
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
