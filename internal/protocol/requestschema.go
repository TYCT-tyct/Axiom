package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"slices"

	"axiom/internal/platform/jsonx"
)

// Responses normalizes omitted strict; Chat defaults to best effort. Requiring
// the caller to choose explicitly outside this subset avoids guessing the
// provider's fallback (OpenAI Function calling, Strict mode, 2026-10-20).
func responsesDefaultSchema(raw jsontext.Value) (jsontext.Value, error) {
	if isNull(raw) {
		raw = jsontext.Value(`{"type":"object"}`)
	}
	var schema responseToolSchema
	if err := jsonx.Unmarshal(raw, &schema); err != nil {
		return nil, shapeError("tool parameters must be a schema object")
	}
	if isNull(schema.Type) {
		schema.Type = jsontext.Value(`"object"`)
	}
	if err := schema.normalize(); err != nil {
		return nil, err
	}
	return jsonValue(&schema), nil
}

// Decode the tree once: raw scalar constraints keep integers above 2^53 exact,
// and deeply nested schemas do not copy every descendant at every level.
type responseToolSchema struct {
	Type        jsontext.Value                 `json:"type,omitzero"`
	Properties  map[string]*responseToolSchema `json:"properties,omitzero"`
	Defs        map[string]*responseToolSchema `json:"$defs,omitzero"`
	Definitions map[string]*responseToolSchema `json:"definitions,omitzero"`
	AnyOf       []*responseToolSchema          `json:"anyOf,omitzero"`
	Items       *responseToolSchema            `json:"items,omitzero"`
	Constraints map[string]jsontext.Value      `json:",unknown"`
}

var normalizableSchemaKeywords = map[string]bool{
	"title": true, "description": true, "enum": true, "const": true,
	"required": true, "additionalProperties": true, "$ref": true,
	"format": true, "pattern": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "exclusiveMinimum": true, "exclusiveMaximum": true,
	"multipleOf": true, "minItems": true, "maxItems": true,
}

func (s *responseToolSchema) normalize() error {
	if s == nil {
		return &NotSupported{Reason: "a null schema cannot be normalized to strict"}
	}
	for key := range s.Constraints {
		if !normalizableSchemaKeywords[key] {
			return &NotSupported{Reason: "implicit strict tool schema contains " + key + "; set strict explicitly"}
		}
	}
	object, err := schemaObjectType(s.Type)
	if err != nil {
		return err
	}
	if object || s.Properties != nil {
		required := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			required = append(required, name)
		}
		slices.Sort(required)
		if s.Constraints == nil {
			s.Constraints = map[string]jsontext.Value{}
		}
		s.Constraints["required"], s.Constraints["additionalProperties"] = jsonValue(required), jsontext.Value("false")
	}
	return s.normalizeChildren()
}

func (s *responseToolSchema) normalizeChildren() error {
	for _, children := range []map[string]*responseToolSchema{s.Properties, s.Defs, s.Definitions} {
		for _, child := range children {
			if err := child.normalize(); err != nil {
				return err
			}
		}
	}
	for _, child := range s.AnyOf {
		if err := child.normalize(); err != nil {
			return err
		}
	}
	if s.Items != nil {
		return s.Items.normalize()
	}
	return nil
}

func schemaObjectType(raw jsontext.Value) (bool, error) {
	if isNull(raw) {
		return false, nil
	}
	var types []string
	if raw.Kind() == '[' {
		if err := jsonx.Unmarshal(raw, &types); err != nil {
			return false, shapeError("schema type must name types")
		}
		return slices.Contains(types, "object"), nil
	}
	kind, err := requestString(raw)
	return kind == "object", err
}
