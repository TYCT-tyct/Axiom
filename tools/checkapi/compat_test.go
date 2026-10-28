package main

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNewlyRequired(t *testing.T) {
	base := contract("console")
	// Page is a response body, Make a request body, both in the base; Thing is not
	withThing := strings.Replace(base, "    Make:\n", "    Thing:\n      type: object\n      required: [id]\n      properties: {id: {type: string}}\n    Make:\n", 1)
	cases := []struct {
		name, after string
		want        []string
	}{
		{"unchanged", base, nil},
		{"a new optional response field", strings.Replace(base, "properties: {next_cursor:", "properties: {total: {type: integer}, next_cursor:", 1), nil},
		{
			"a response field made required",
			strings.Replace(base, "    Page:\n      type: object\n", "    Page:\n      type: object\n      required: [next_cursor]\n", 1),
			[]string{"console.yaml: Page: next_cursor is newly required; an old node omits it and an old page does not send it during a rolling release: keep it optional, or say why it is safe in x-axiom-newly-required"},
		},
		{
			"a request field made required",
			strings.Replace(base, "      required: [reason]\n      properties: {reason: {type: string}}", "      required: [reason, note]\n      properties: {reason: {type: string}, note: {type: string}}", 1),
			[]string{"console.yaml: Make: note is newly required; an old node omits it and an old page does not send it during a rolling release: keep it optional, or say why it is safe in x-axiom-newly-required"},
		},
		{
			"a nested inline object",
			strings.Replace(base, "properties: {next_cursor: {type: string, nullable: true}}",
				"properties: {next_cursor: {type: string, nullable: true}, at: {type: object, required: [x], properties: {x: {type: string}}}}", 1),
			nil, // the field at is new, so its object is new
		},
		{"a new schema may require its fields", withThing, nil},
		{
			"an allOf member added to an existing schema",
			strings.Replace(base, "      required: [reason]\n      properties: {reason: {type: string}}",
				"      required: [reason]\n      properties: {reason: {type: string}, note: {type: string}}\n      allOf: [{type: object, required: [note], properties: {note: {type: string}}}]", 1),
			[]string{"console.yaml: Make: note is newly required; an old node omits it and an old page does not send it during a rolling release: keep it optional, or say why it is safe in x-axiom-newly-required"},
		},
		{
			"a shared response's inline body",
			strings.Replace(base, "          schema: {$ref: \"#/components/schemas/Error\"}", "          schema: {type: object, required: [code], properties: {error: {type: string}, code: {type: string}}}", 1),
			nil, // checked against a base whose Failure body is inline too, below
		},
		{
			"the reason is given",
			strings.Replace(base, "    Page:\n      type: object\n", "    Page:\n      type: object\n      required: [next_cursor]\n      x-axiom-newly-required: {next_cursor: every node serves it since v1.4}\n", 1),
			nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := newlyRequired(map[string][]byte{"console.yaml": []byte(base)}, map[string][]byte{"console.yaml": []byte(c.after)})
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Error(diff)
			}
		})
	}
}

// TestNewlyRequiredSharedBodies: a body in components.responses or components.requestBodies is compared like one
// written in the operation.
func TestNewlyRequiredSharedBodies(t *testing.T) {
	failure := func(required string) string {
		return strings.Replace(contract("console"), "          schema: {$ref: \"#/components/schemas/Error\"}",
			"          schema: {type: object, "+required+"properties: {error: {type: string}, code: {type: string}}}", 1)
	}
	got := newlyRequired(map[string][]byte{"console.yaml": []byte(failure(""))}, map[string][]byte{"console.yaml": []byte(failure("required: [code], "))})
	if len(got) != 1 || !strings.HasPrefix(got[0], "console.yaml: responses/Failure application/json: code is newly required") {
		t.Errorf("got %q", got)
	}
}

// TestNewlyRequiredNested: a schema nested in an existing one is compared too, so a field made required inside an
// existing inline object fails the same way.
func TestNewlyRequiredNested(t *testing.T) {
	nested := func(required string) string {
		return strings.Replace(contract("console"), "properties: {next_cursor: {type: string, nullable: true}}",
			"properties: {next_cursor: {type: string, nullable: true}, at: {type: object, "+required+"properties: {x: {type: string}}}}", 1)
	}
	got := newlyRequired(map[string][]byte{"console.yaml": []byte(nested(""))}, map[string][]byte{"console.yaml": []byte(nested("required: [x], "))})
	if len(got) != 1 || !strings.HasPrefix(got[0], "console.yaml: Page.at: x is newly required") {
		t.Errorf("got %q", got)
	}
}
