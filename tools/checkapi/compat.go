package main

import (
	"fmt"
	"slices"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
)

// reasonsExt maps a property a schema newly requires to why that is safe, e.g. every node serving the old contract is
// gone. It sits on the schema, not the property: OpenAPI 3.0 ignores a $ref's siblings.
const reasonsExt = "x-axiom-newly-required"

// newlyRequired compares each contract with its base: a property that a schema of the base did not require stays
// optional. During a rolling release a new page reads old nodes, which omit the field, and old pages write to new
// nodes without it; the generated types then force both sides to handle its absence. A schema that is new in this
// change may require what it likes: no old node serves it and no old page sends it.
func newlyRequired(base, files map[string][]byte) []string {
	var problems []string
	for _, name := range sortedKeys(files) {
		if base[name] == nil {
			continue
		}
		before, err := openapi3.NewLoader().LoadFromData(base[name])
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: the base contract does not load: %v", name, err))
			continue
		}
		if after, err := openapi3.NewLoader().LoadFromData(files[name]); err == nil { // check reports one that does not load
			problems = append(problems, compare(name, objects(before), objects(after))...)
		}
	}
	return problems
}

func compare(file string, before, after map[string]*openapi3.Schema) []string {
	var problems []string
	for _, at := range sortedKeys(after) {
		was := before[at]
		if was == nil {
			continue
		}
		reasons, _ := after[at].Extensions[reasonsExt].(map[string]any)
		for _, field := range after[at].Required {
			if !slices.Contains(was.Required, field) && reasons[field] == nil {
				problems = append(problems, fmt.Sprintf("%s: %s: %s is newly required; an old node omits it and an old page does "+
					"not send it during a rolling release: keep it optional, or say why it is safe in %s", file, at, field, reasonsExt))
			}
		}
	}
	return problems
}

// objects returns every schema of the contract by where it is: the component schemas by name, the bodies each
// operation defines inline, and the schemas nested in them.
func objects(doc *openapi3.T) map[string]*openapi3.Schema {
	found := map[string]*openapi3.Schema{}
	for name, ref := range doc.Components.Schemas {
		if ref.Value != nil {
			walk(found, name, ref.Value)
		}
	}
	for at, ref := range inlineBodies(doc) {
		nested(found, at, ref)
	}
	return found
}

// walk records s at at, then the schemas nested in it.
func walk(found map[string]*openapi3.Schema, at string, s *openapi3.Schema) {
	found[at] = s
	for _, name := range sortedKeys(s.Properties) {
		nested(found, at+"."+name, s.Properties[name])
	}
	nested(found, at+"[]", s.Items)
	nested(found, at+"{}", s.AdditionalProperties.Schema)
	for kind, members := range map[string]openapi3.SchemaRefs{"allOf": s.AllOf, "oneOf": s.OneOf, "anyOf": s.AnyOf} {
		for i, member := range members {
			nested(found, at+"/"+kind+"/"+strconv.Itoa(i), member)
		}
	}
}

// nested walks a schema written in place; a $ref stops the walk, since the schema it names is visited under its own name.
func nested(found map[string]*openapi3.Schema, at string, ref *openapi3.SchemaRef) {
	if ref != nil && ref.Ref == "" && ref.Value != nil {
		walk(found, at, ref.Value)
	}
}

// inlineBodies returns the request and response bodies' schemas the operations write in place, by operation, status
// and media type.
func inlineBodies(doc *openapi3.T) map[string]*openapi3.SchemaRef {
	bodies := map[string]*openapi3.SchemaRef{}
	add := func(at string, content openapi3.Content) {
		for media, m := range content {
			bodies[at+" "+media] = m.Schema
		}
	}
	for _, o := range operations(doc) {
		where := o.method + " " + o.path
		if body := o.op.RequestBody; body != nil && body.Ref == "" && body.Value != nil {
			add(where+" request", body.Value.Content)
		}
		for status, response := range o.op.Responses.Map() {
			if response.Ref == "" && response.Value != nil {
				add(where+" "+status, response.Value.Content)
			}
		}
	}
	return bodies
}
