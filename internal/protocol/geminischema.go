package protocol

import (
	"fmt"
	"strings"

	"axiom/internal/platform/jsonx"
)

// jsonSchemaOf turns a schema in Gemini's OpenAPI subset into JSON Schema: types in capitals become lower case, and
// nullable becomes the null type beside the other; the rest is the same keyword.
func jsonSchemaOf(v jsonx.Raw) (jsonx.Raw, error) {
	var s map[string]jsonx.Raw
	if err := jsonx.Unmarshal(v, &s); err != nil {
		return nil, fmt.Errorf("%w: a Gemini schema is not an object", ErrInvalid)
	}
	out := requestObject{}
	for k, v := range s {
		converted, err := schemaKeyword(k, v)
		if err != nil {
			return nil, err
		}
		if converted != nil {
			out[k] = converted
		}
	}
	if string(s["nullable"]) == "true" && !isNull(out["type"]) {
		out["type"] = jsonValue([]jsonx.Raw{out["type"], jsonValue("null")})
	}
	return jsonValue(out), nil
}

// schemaKeyword is one keyword's value in JSON Schema, nil for one JSON Schema has no use for.
func schemaKeyword(k string, v jsonx.Raw) (jsonx.Raw, error) {
	switch k {
	case "type":
		return jsonx.Raw(strings.ToLower(string(v))), nil
	case "nullable", "propertyOrdering":
		return nil, nil
	case "items":
		return jsonSchemaOf(v)
	case "properties":
		var props map[string]jsonx.Raw
		if err := jsonx.Unmarshal(v, &props); err != nil {
			return nil, fmt.Errorf("%w: Gemini schema properties are not an object", ErrInvalid)
		}
		converted := requestObject{}
		for name, p := range props {
			inner, err := jsonSchemaOf(p)
			if err != nil {
				return nil, err
			}
			converted[name] = inner
		}
		return jsonValue(converted), nil
	case "anyOf":
		var alts []jsonx.Raw
		if err := jsonx.Unmarshal(v, &alts); err != nil {
			return nil, fmt.Errorf("%w: a Gemini schema's anyOf is not a list", ErrInvalid)
		}
		for i, a := range alts {
			inner, err := jsonSchemaOf(a)
			if err != nil {
				return nil, err
			}
			alts[i] = inner
		}
		return jsonValue(alts), nil
	}
	return v, nil
}
