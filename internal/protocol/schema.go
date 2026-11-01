package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"errors"
	"fmt"
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// anthropicSchema checks that a JSON schema is in the subset Anthropic's structured outputs and strict tools accept
// (Anthropic《Structured outputs》"JSON Schema limitations", read 2026-10-15): no numeric, string or array constraint
// but minItems 0 or 1, additionalProperties false on every object, enums of plain values, formats from its list, and
// $ref only to the schema's own definitions, none of them recursive. A schema outside it is not approximated by
// dropping what it cannot keep: the request goes to an endpoint that can (README.md「跨格式转换」).
func anthropicSchema(raw jsontext.Value, b *budget) error {
	// The schema is read into Go values, whose maps and slices take a few times the schema's own bytes.
	if err := b.draw(8*int64(len(raw)) + 2048); err != nil {
		return err
	}
	var root any
	if err := jsonx.Unmarshal(raw, &root); err != nil {
		return &NotSupported{Reason: "a schema is not JSON: " + err.Error()}
	}
	s, ok := root.(map[string]any)
	if !ok {
		return &NotSupported{Reason: "a schema is not an object"}
	}
	defs := map[string]any{}
	for _, key := range []string{"$defs", "definitions"} {
		if d, ok := s[key].(map[string]any); ok {
			for name, v := range d {
				defs["#/"+key+"/"+name] = v
			}
		}
	}
	if err := checkSchema(s); err != nil {
		return &NotSupported{Reason: "a schema is outside the subset Anthropic accepts: " + err.Error()}
	}
	if err := noRecursion(s, defs); err != nil {
		return &NotSupported{Reason: "a schema is outside the subset Anthropic accepts: " + err.Error()}
	}
	return nil
}

// unsupportedKeywords are the keywords outside the subset.
var unsupportedKeywords = []string{
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "minLength", "maxLength", "maxItems",
	"uniqueItems", "contains", "minContains", "maxContains", "prefixItems", "minProperties", "maxProperties",
	"patternProperties", "propertyNames", "dependentRequired", "dependentSchemas", "if", "then", "else", "not",
	"oneOf", "unevaluatedProperties", "unevaluatedItems",
}

var formats = []string{"date-time", "time", "date", "duration", "email", "hostname", "uri", "ipv4", "ipv6", "uuid"}

// schemaRules each report what of a schema is outside the subset, "" for nothing.
var schemaRules = []func(s map[string]any) string{
	func(s map[string]any) string {
		for _, key := range unsupportedKeywords {
			if _, ok := s[key]; ok {
				return key
			}
		}
		return ""
	},
	func(s map[string]any) string {
		if n, ok := s["minItems"].(float64); ok && n != 0 && n != 1 {
			return fmt.Sprintf("minItems %v", n)
		}
		return ""
	},
	func(s map[string]any) string {
		_, hasProperties := s["properties"]
		ap, ok := s["additionalProperties"]
		if ok && ap != false || (s["type"] == "object" || hasProperties) && !ok {
			return "an object without additionalProperties false"
		}
		return ""
	},
	func(s map[string]any) string {
		if f, ok := s["format"].(string); ok && !slices.Contains(formats, f) {
			return "format " + f
		}
		return ""
	},
	func(s map[string]any) string {
		enum, _ := s["enum"].([]any)
		for _, v := range enum {
			switch v.(type) {
			case map[string]any, []any:
				return "an enum of objects or arrays"
			}
		}
		return ""
	},
	func(s map[string]any) string {
		if ref, ok := s["$ref"].(string); ok && !strings.HasPrefix(ref, "#/") {
			return "$ref " + ref
		}
		return ""
	},
	func(s map[string]any) string {
		all, _ := s["allOf"].([]any)
		for _, v := range all {
			if sub, ok := v.(map[string]any); ok && sub["$ref"] != nil {
				return "allOf with $ref"
			}
		}
		return ""
	},
}

func checkSchema(s map[string]any) error {
	for _, rule := range schemaRules {
		if what := rule(s); what != "" {
			return errors.New(what)
		}
	}
	return eachSubschema(s, checkSchema)
}

// eachSubschema calls check on every schema s holds.
func eachSubschema(s map[string]any, check func(map[string]any) error) error {
	var subs []any
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if m, ok := s[key].(map[string]any); ok {
			for _, v := range m {
				subs = append(subs, v)
			}
		}
	}
	for _, key := range []string{"anyOf", "allOf"} {
		if l, ok := s[key].([]any); ok {
			subs = append(subs, l...)
		}
	}
	subs = append(subs, s["items"])
	for _, v := range subs {
		if sub, ok := v.(map[string]any); ok {
			if err := check(sub); err != nil {
				return err
			}
		}
	}
	return nil
}

// refWalk follows the $refs from a schema to look for recursion. Each definition is walked once: one already walked
// is not walked again, so definitions that share others cost what the schema's size does, and one reached again while
// its own walk is under way is recursion.
type refWalk struct {
	defs map[string]any
	done map[string]bool // by $ref: false while its walk is under way, true once it is over
}

// noRecursion fails when following $ref from s comes back to a definition being walked, or to the root.
func noRecursion(s map[string]any, defs map[string]any) error {
	w := &refWalk{defs: defs, done: map[string]bool{}}
	return w.walk(s)
}

func (w *refWalk) walk(s map[string]any) error {
	if ref, ok := s["$ref"].(string); ok {
		done, seen := w.done[ref]
		switch {
		case ref == "#" || seen && !done:
			return fmt.Errorf("a recursive $ref %s", ref)
		case !seen:
			def, ok := w.defs[ref].(map[string]any)
			if !ok {
				return fmt.Errorf("$ref %s names no definition", ref)
			}
			w.done[ref] = false
			if err := w.walk(def); err != nil {
				return err
			}
			w.done[ref] = true
		}
	}
	return eachSubschema(s, w.walk)
}
