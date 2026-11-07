package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"axiom/internal/platform/jsonx"
)

// The MCP bridge's tools (docs/domains/operations.md「MCP 映射」): one per operation of the operations contract, named by
// its x-mcp-name, whose input is the operation's parameters and body as one object, and whose call is the operation's
// request.

// headerArguments names the contract's header parameters as tool arguments. A header the bridge does not know stops the
// start, so a new one is named here instead of being dropped.
var headerArguments = map[string]string{"Idempotency-Key": "idempotency_key", "X-Axiom-Run-ID": "run_id"}

// argBody is where a body property goes, beside the contract's parameter locations.
const argBody = "body"

// maxSchemaDepth bounds inputSchema's descent, which only a schema that refers to itself would reach.
const maxSchemaDepth = 16

// bridgeTool is one operation of the contract as a tool: its MCP description and how a call becomes the request.
type bridgeTool struct {
	tool   *mcp.Tool
	method string
	path   string               // the contract's template, such as /api/operations/v1/models/{model_id}/status
	args   map[string]bridgeArg // by argument name
	body   bool                 // whether the operation takes a JSON body, which then carries the body arguments
	rules  *agentRules
}

// bridgeArg is where an argument goes: a path, query or header parameter, or a property of the body, by its name there.
type bridgeArg struct{ in, name string }

// bridgeTools reads every operation of doc into a tool, ordered by name. An operation the bridge cannot describe
// completely (an unknown header, a name used twice, a schema keyword it does not carry) stops the start: a tool that
// describes less than the core checks would leave the model guessing.
func bridgeTools(doc *openapi3.T) ([]bridgeTool, error) {
	var tools []bridgeTool
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if offered, ok := op.Extensions["x-mcp"].(bool); ok && !offered {
				continue
			}
			t, err := bridgeToolOf(method, path, op, item.Parameters)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			tools = append(tools, t)
		}
	}
	slices.SortFunc(tools, func(a, b bridgeTool) int { return strings.Compare(a.tool.Name, b.tool.Name) })
	for i := 1; i < len(tools); i++ {
		if tools[i].tool.Name == tools[i-1].tool.Name {
			return nil, fmt.Errorf("two operations are the tool %s", tools[i].tool.Name)
		}
	}
	return tools, nil
}

func bridgeToolOf(method, path string, op *openapi3.Operation, pathParameters openapi3.Parameters) (bridgeTool, error) {
	rules, err := agentRulesOf(op)
	if err != nil {
		return bridgeTool{}, err
	}
	in := toolInput{args: map[string]bridgeArg{}, properties: map[string]any{}}
	if err := in.addParameters(append(slices.Clone(pathParameters), op.Parameters...)); err != nil {
		return bridgeTool{}, err
	}
	if op.RequestBody != nil {
		if err := in.addBody(op.RequestBody.Value); err != nil {
			return bridgeTool{}, err
		}
	}
	description := op.Summary
	if op.Description != "" {
		description += "\n\n" + op.Description
	}
	_, keyed := in.args["idempotency_key"]
	tool := &mcp.Tool{
		Name: rules.tool, Title: op.Summary, Description: description, InputSchema: in.schema(),
		Annotations: annotations(op.Summary, method == http.MethodGet, keyed),
	}
	return bridgeTool{tool: tool, method: method, path: path, args: in.args, body: op.RequestBody != nil, rules: rules}, nil
}

// annotations are a tool's hints: a read changes nothing, a write may change configuration or money and repeats safely
// under its key, and no tool reaches beyond Axiom. The core enforces the rules; the hints only inform the client.
func annotations(title string, read, keyed bool) *mcp.ToolAnnotations {
	a := &mcp.ToolAnnotations{Title: title, ReadOnlyHint: read, OpenWorldHint: new(false)}
	if !read {
		a.DestructiveHint, a.IdempotentHint = new(true), keyed
	}
	return a
}

// toolInput gathers a tool's arguments and the input schema that describes them.
type toolInput struct {
	args       map[string]bridgeArg
	properties map[string]any
	required   []string
}

func (in *toolInput) add(arg string, where bridgeArg, ref *openapi3.SchemaRef, description string, need bool) error {
	if _, taken := in.args[arg]; taken {
		return fmt.Errorf("the argument %s is named twice", arg)
	}
	schema, err := inputSchema(ref, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", arg, err)
	}
	if description != "" {
		schema["description"] = description
	}
	in.args[arg], in.properties[arg] = where, schema
	if need {
		in.required = append(in.required, arg)
	}
	return nil
}

// addParameters adds the path, query and header parameters, a header under its argument name.
func (in *toolInput) addParameters(parameters openapi3.Parameters) error {
	for _, p := range parameters {
		v, arg := p.Value, p.Value.Name
		switch v.In {
		case openapi3.ParameterInPath, openapi3.ParameterInQuery:
		case openapi3.ParameterInHeader:
			if arg = headerArguments[v.Name]; arg == "" {
				return fmt.Errorf("the header %s has no argument name", v.Name)
			}
		default:
			return fmt.Errorf("a %s parameter cannot be an argument", v.In)
		}
		if err := in.add(arg, bridgeArg{v.In, v.Name}, v.Schema, v.Description, v.Required); err != nil {
			return err
		}
	}
	return nil
}

// addBody adds each property of the JSON body as an argument of its own.
func (in *toolInput) addBody(body *openapi3.RequestBody) error {
	media := body.Content.Get("application/json")
	if media == nil || media.Schema == nil || media.Schema.Value.Properties == nil {
		return errors.New("the body is not a JSON object with properties")
	}
	object := media.Schema.Value
	for name, ref := range object.Properties {
		if err := in.add(name, bridgeArg{argBody, name}, ref, "", slices.Contains(object.Required, name)); err != nil {
			return err
		}
	}
	return nil
}

// schema is the input schema: one object of the arguments, nothing else.
func (in *toolInput) schema() map[string]any {
	schema := map[string]any{"type": "object", "properties": in.properties, "additionalProperties": false}
	if len(in.required) > 0 {
		schema["required"] = slices.Sorted(slices.Values(in.required))
	}
	return schema
}

// inputSchema is ref as JSON Schema with every reference inlined, so a client that does not resolve references still
// sees the whole input. A keyword that would read differently there (not, discriminator, OpenAPI 3.0's exclusive-bound
// flags) stops the start instead of being dropped from what the model is told.
func inputSchema(ref *openapi3.SchemaRef, depth int) (map[string]any, error) {
	s := ref.Value
	switch {
	case depth > maxSchemaDepth:
		return nil, errors.New("the schema nests too deep or refers to itself")
	case s.Not != nil || s.Discriminator != nil || s.ExclusiveMin.IsSet() || s.ExclusiveMax.IsSet():
		return nil, errors.New("not, discriminator and exclusive bounds are not carried into an input schema")
	}
	out := schemaKeywords(s)
	children, err := childSchemas(s, depth+1)
	if err != nil {
		return nil, err
	}
	maps.Copy(out, children)
	return out, nil
}

// schemaKeywords are the keywords s holds itself, in JSON Schema's spelling; one s leaves unset is absent.
func schemaKeywords(s *openapi3.Schema) map[string]any {
	out := map[string]any{}
	for key, value := range map[string]any{
		"title": s.Title, "format": s.Format, "description": s.Description, "pattern": s.Pattern, "enum": s.Enum,
		"default": s.Default, "required": s.Required, "minLength": s.MinLength, "maxLength": s.MaxLength,
		"minimum": s.Min, "maximum": s.Max, "multipleOf": s.MultipleOf, "minItems": s.MinItems, "maxItems": s.MaxItems,
		"uniqueItems": s.UniqueItems,
	} {
		if v := reflect.ValueOf(value); v.IsValid() && !v.IsZero() {
			out[key] = value
		}
	}
	if s.Type != nil && len(*s.Type) == 1 {
		out["type"] = (*s.Type)[0]
		if s.Nullable {
			out["type"] = []string{(*s.Type)[0], "null"}
		}
	}
	if s.AdditionalProperties.Has != nil {
		out["additionalProperties"] = *s.AdditionalProperties.Has
	}
	return out
}

// childSchemas are the schemas s holds, each inlined: items, additional properties, properties and the compositions.
func childSchemas(s *openapi3.Schema, depth int) (map[string]any, error) {
	out := map[string]any{}
	for key, refs := range map[string]openapi3.SchemaRefs{
		"items": {s.Items}, "additionalProperties": {s.AdditionalProperties.Schema}, "oneOf": s.OneOf, "anyOf": s.AnyOf, "allOf": s.AllOf,
	} {
		refs = slices.DeleteFunc(refs, func(r *openapi3.SchemaRef) bool { return r == nil })
		list, err := inputSchemas(refs, depth)
		switch {
		case err != nil:
			return nil, fmt.Errorf("%s: %w", key, err)
		case len(list) == 0:
		case key == "items" || key == "additionalProperties":
			out[key] = list[0]
		default:
			out[key] = list
		}
	}
	if len(s.Properties) > 0 {
		names := slices.Sorted(maps.Keys(s.Properties))
		refs := make(openapi3.SchemaRefs, len(names))
		for i, name := range names {
			refs[i] = s.Properties[name]
		}
		list, err := inputSchemas(refs, depth)
		if err != nil {
			return nil, err
		}
		properties := map[string]any{}
		for i, name := range names {
			properties[name] = list[i]
		}
		out["properties"] = properties
	}
	return out, nil
}

func inputSchemas(refs openapi3.SchemaRefs, depth int) ([]any, error) {
	list := make([]any, len(refs))
	for i, ref := range refs {
		schema, err := inputSchema(ref, depth)
		if err != nil {
			return nil, err
		}
		list[i] = schema
	}
	return list, nil
}

// request is the HTTP request a call of t with args makes of the core: path, query and header arguments by their
// parameters' names, and the body arguments as the JSON body. An argument the tool does not take, a path argument that
// is missing or would change the path, and a parameter given an object or a list are refused here; every other check is
// the core's, against the same contract.
func (t bridgeTool) request(ctx context.Context, core *url.URL, args []byte) (*http.Request, error) {
	var given map[string]jsonx.Raw
	if len(args) > 0 {
		if err := jsonx.Unmarshal(args, &given); err != nil {
			return nil, errors.New("the arguments must be a JSON object")
		}
	}
	call := toolCall{path: t.path, query: url.Values{}, header: http.Header{}, body: map[string]jsonx.Raw{}}
	for name, value := range given {
		where, ok := t.args[name]
		if !ok {
			return nil, fmt.Errorf("%s takes no argument %q", t.tool.Name, name)
		}
		if err := call.place(where, value); err != nil {
			return nil, fmt.Errorf("the argument %s: %w", name, err)
		}
	}
	if start := strings.IndexByte(call.path, '{'); start >= 0 {
		end := start + strings.IndexByte(call.path[start:], '}')
		return nil, fmt.Errorf("%s needs the argument %s", t.tool.Name, call.path[start+1:end])
	}
	target := core.Scheme + "://" + core.Host + call.path
	if len(call.query) > 0 {
		target += "?" + call.query.Encode()
	}
	var body io.Reader
	if t.body {
		encoded, err := jsonx.MarshalCanonical(call.body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
		call.header.Set("Content-Type", "application/json")
	}
	r, err := http.NewRequestWithContext(ctx, t.method, target, body)
	if err != nil {
		return nil, err
	}
	maps.Copy(r.Header, call.header)
	return r, nil
}

// toolCall is a request as its arguments are placed into it.
type toolCall struct {
	path   string
	query  url.Values
	header http.Header
	body   map[string]jsonx.Raw
}

func (c *toolCall) place(where bridgeArg, value jsonx.Raw) error {
	if where.in == argBody {
		c.body[where.name] = value
		return nil
	}
	text, err := parameterText(value)
	if err != nil {
		return err
	}
	switch where.in {
	case openapi3.ParameterInPath:
		if text == "" || text == "." || text == ".." {
			return errors.New("must name an object")
		}
		c.path = strings.Replace(c.path, "{"+where.name+"}", url.PathEscape(text), 1)
	case openapi3.ParameterInQuery:
		c.query.Set(where.name, text)
	default:
		c.header.Set(where.name, text)
	}
	return nil
}

// parameterText is a path, query or header argument as the text the parameter carries: a string as it is, a number or
// a boolean as written.
func parameterText(value jsonx.Raw) (string, error) {
	var scalar any
	if err := jsonx.Unmarshal(value, &scalar); err != nil {
		return "", err
	}
	switch v := scalar.(type) {
	case string:
		return v, nil
	case float64, bool:
		return string(bytes.TrimSpace(value)), nil
	}
	return "", errors.New("want a string, a number or a boolean")
}
