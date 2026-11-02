package protocol

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

func (s *scanner) fallbacks() error {
	s.fallbacksSet = s.dec.PeekKind() != 'n'
	return s.array(func(s *scanner) error {
		if len(s.fallbackModels) == 3 {
			return fmt.Errorf("fallbacks lists more than 3 models")
		}
		raw, err := s.dec.ReadValue()
		if err != nil {
			return err
		}
		var entry struct {
			Model string `json:"model"`
		}
		if jsonx.UnmarshalKnown(raw, &entry) != nil || strings.TrimSpace(entry.Model) == "" {
			return fmt.Errorf("fallbacks entries accept only a nonempty model")
		}
		model := entry.Model
		s.fallbackModels = append(s.fallbackModels, model)
		return nil
	})
}

// PresetReference resolves the three OpenRouter spellings without choosing between conflicting references.
// The model returned is a request override for the combined spelling; an empty one uses the stored model.
func PresetReference(r *Request) (slug, model string, err error) {
	model = r.Model
	if at := strings.Index(model, "@preset/"); at >= 0 {
		slug, model = model[at+len("@preset/"):], model[:at]
	}
	if field, ok := strings.CutPrefix(r.Preset, "@preset/"); ok {
		if slug != "" && slug != field {
			return "", "", fmt.Errorf("%w: conflicting preset references", ErrInvalid)
		}
		slug = field
	}
	if (strings.Contains(r.Model, "@preset/") || strings.HasPrefix(r.Preset, "@preset/")) && !presetSlug.MatchString(slug) {
		return "", "", fmt.Errorf("%w: invalid preset reference", ErrInvalid)
	}
	return slug, model, nil
}

var presetSlug = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// PresetCost bounds both decoded member maps, merged tools and encoded output while they coexist.
func PresetCost(body, config []byte) int64 {
	return 2 * (nativeRequestCost(body) + nativeRequestCost(config))
}

// MergePreset shallowly overlays the request, preserving message and signature values.
// Tools are the documented exception: identity union, with the request taking precedence.
func MergePreset(f Format, body, config []byte, model string) ([]byte, error) {
	merged, err := mergePreset(f, body, config, model)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot merge preset: %s", ErrInvalid, strings.TrimPrefix(bodyFault(err), ErrInvalid.Error()+": "))
	}
	return merged, nil
}

func mergePreset(f Format, body, config []byte, model string) ([]byte, error) {
	var request, defaults map[string]jsonx.Raw
	if err := jsonx.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	if err := jsonx.Unmarshal(config, &defaults); err != nil {
		return nil, err
	}
	if err := presetDefaults(f, request, defaults, model); err != nil {
		return nil, err
	}
	if a, b := defaults["tools"], request["tools"]; len(a) > 0 && len(b) > 0 && !bytes.Equal(b, []byte("null")) {
		tools, err := mergeTools(f, a, b)
		if err != nil {
			return nil, err
		}
		request["tools"] = tools
	}
	for name, value := range defaults {
		if _, ok := request[name]; !ok {
			request[name] = value
		}
	}
	if err := presetPrimary(request); err != nil {
		return nil, err
	}
	return presetObject(request), nil
}

func presetDefaults(f Format, request, defaults map[string]jsonx.Raw, model string) error {
	if err := presetModel(request, model); err != nil {
		return err
	}
	delete(request, "preset")
	// Cache settings are the gateway's, not the model's: they must not reach an upstream that would reject or ignore them.
	delete(defaults, "cache_enabled")
	delete(defaults, "cache_ttl_seconds")
	if f == Messages && !isNull(request["fallbacks"]) {
		// Resolve the configured primary before replacing its alternate-model defaults.
		if err := presetPrimary(defaults); err != nil {
			return err
		}
		delete(defaults, "models")
	}
	return presetSystem(f, request, defaults)
}

// A models-only configuration is one ordered route, not an absent primary plus unrelated defaults.
func presetPrimary(request map[string]jsonx.Raw) error {
	if _, present := request["model"]; present {
		return nil
	}
	models, err := requestArray(request["models"])
	if err != nil || len(models) == 0 {
		return err
	}
	request["model"] = models[0]
	request["models"] = presetArray(models[1:])
	return nil
}

func presetModel(request map[string]jsonx.Raw, model string) error {
	var original string
	if raw, ok := request["model"]; ok {
		if err := jsonx.Unmarshal(raw, &original); err != nil {
			return err
		}
	}
	if model == "" && strings.Contains(original, "@preset/") {
		delete(request, "model")
	} else if model != "" {
		encoded, err := jsonx.MarshalCanonical(model)
		if err != nil {
			return err
		}
		request["model"] = encoded
	}
	return nil
}

func presetSystem(f Format, request, defaults map[string]jsonx.Raw) error {
	system, ok := defaults["system"]
	if !ok {
		return nil
	}
	if f == Messages {
		return nil
	}
	delete(defaults, "system")
	if f == Responses {
		defaults["instructions"] = system
		return nil
	}
	var messages []jsonx.Raw
	if err := jsonx.Unmarshal(request["messages"], &messages); err != nil {
		return err
	}
	for _, raw := range messages {
		var msg struct {
			Role string `json:"role"`
		}
		if err := jsonx.Unmarshal(raw, &msg); err != nil {
			return err
		}
		if msg.Role == "system" || msg.Role == "developer" {
			return nil
		}
	}
	msg := presetObject(map[string]jsonx.Raw{"role": jsonx.Raw(`"system"`), "content": system})
	request["messages"] = presetArray(append([]jsonx.Raw{msg}, messages...))
	return nil
}

func mergeTools(f Format, a, b []byte) (jsonx.Raw, error) {
	var base, over []jsonx.Raw
	if err := jsonx.Unmarshal(a, &base); err != nil {
		return nil, err
	}
	if err := jsonx.Unmarshal(b, &over); err != nil {
		return nil, err
	}
	indices := map[string]int{}
	for i, tool := range base {
		id, err := toolIdentity(f, tool)
		if err != nil {
			return nil, err
		}
		indices[id] = i
	}
	for _, tool := range over {
		id, err := toolIdentity(f, tool)
		if err != nil {
			return nil, err
		}
		if i, ok := indices[id]; ok {
			base[i] = tool
		} else {
			indices[id] = len(base)
			base = append(base, tool)
		}
	}
	return presetArray(base), nil
}

func toolIdentity(f Format, raw []byte) (string, error) {
	var tool struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Parameters struct {
			Name string `json:"name"`
		} `json:"parameters"`
	}
	if err := jsonx.Unmarshal(raw, &tool); err != nil {
		return "", err
	}
	switch {
	case tool.Type == "openrouter:advisor" || tool.Type == "openrouter:subagent":
		return tool.Type + ":" + tool.Parameters.Name, nil
	case tool.Type == "function" || f == Messages && tool.Type == "":
		name := tool.Name
		if f == Chat {
			name = tool.Function.Name
		}
		if name == "" {
			return "", fmt.Errorf("%w: preset function tool has no name", ErrInvalid)
		}
		return "function:" + name, nil
	default:
		if tool.Name != "" && !strings.HasPrefix(tool.Type, "openrouter:") {
			return tool.Type + ":" + tool.Name, nil
		}
		return tool.Type, nil
	}
}

// Values were strictly validated on input; copy them without normalizing opaque strings or numeric schemas.
func presetObject(fields map[string]jsonx.Raw) []byte {
	out := []byte{'{'}
	for i, key := range slices.Sorted(maps.Keys(fields)) {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(appendQuoted(out, key), ':')
		out = append(out, fields[key]...)
	}
	return append(out, '}')
}

func presetArray(values []jsonx.Raw) []byte {
	out := []byte{'['}
	for i, value := range values {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, value...)
	}
	return append(out, ']')
}
