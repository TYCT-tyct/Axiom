package main

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// contract is a small valid contract for a surface: a public operation, a paged list and a change, with idempotency
// keys in force. Each case breaks one thing in it.
func contract(surface string) string {
	return strings.ReplaceAll(`openapi: 3.0.3
info: {title: t, version: v1}
paths:
  /api/@/v1/version:
    get:
      operationId: getVersion
      tags: [Session]
      summary: s
      security: []
      responses:
        "200": {description: ok}
        default: {$ref: "#/components/responses/Failure"}
  /api/@/v1/things:
    get:
      operationId: listThings
      tags: [Things]
      summary: s
      security: [{session: []}]
      x-axiom-level: mfa
      x-axiom-permission: things.view
      parameters:
        - $ref: "#/components/parameters/Cursor"
        - $ref: "#/components/parameters/Limit"
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Page"}
        "401": {$ref: "#/components/responses/Failure"}
        default: {$ref: "#/components/responses/Failure"}
    post:
      operationId: makeThing
      tags: [Things]
      summary: s
      security: [{session: [], csrf: []}]
      x-axiom-level: mfa
      x-axiom-permission: things.manage
      parameters:
        - $ref: "#/components/parameters/IdempotencyKey"
      requestBody:
        required: true
        content:
          application/json:
            schema: {$ref: "#/components/schemas/Make"}
      responses:
        "201": {description: made}
        "401": {$ref: "#/components/responses/Failure"}
        "403": {$ref: "#/components/responses/Failure"}
        default: {$ref: "#/components/responses/Failure"}
components:
  securitySchemes:
    session: {type: apiKey, in: cookie, name: s}
    csrf: {type: apiKey, in: header, name: X-CSRF-Token}
  parameters:
    Cursor: {name: cursor, in: query, schema: {type: string}}
    Limit: {name: limit, in: query, schema: {type: integer}}
    IdempotencyKey: {name: Idempotency-Key, in: header, required: true, schema: {type: string}}
  responses:
    Failure:
      description: failed
      content:
        application/json:
          schema: {$ref: "#/components/schemas/Error"}
  schemas:
    Error:
      type: object
      properties: {error: {type: string}}
    Page:
      type: object
      properties: {next_cursor: {type: string, nullable: true}}
    Make:
      type: object
      required: [reason]
      properties: {reason: {type: string}}
`, "@", surface)
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"a valid contract", map[string]string{"console.yaml": contract("console")}, nil},
		{
			"not OpenAPI",
			map[string]string{"console.yaml": strings.Replace(contract("console"), `"200": {description: ok}`, `"200": {}`, 1)},
			[]string{"console.yaml: invalid paths: invalid path /api/console/v1/version: invalid operation GET: a short description of the response is required"},
		},
		{
			"the path is on another surface",
			map[string]string{"console.yaml": contract("admin")},
			[]string{
				"console.yaml: GET /api/admin/v1/things: the path must start with /api/console/v1/",
				"console.yaml: GET /api/admin/v1/version: the path must start with /api/console/v1/",
				"console.yaml: POST /api/admin/v1/things: the path must start with /api/console/v1/",
			},
		},
		{
			"security is not declared",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "      security: []\n", "", 1)},
			[]string{"console.yaml: GET /api/console/v1/version: security is not declared (write security: [] for a public operation)"},
		},
		{
			"a public operation names a permission",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "      security: []\n", "      security: []\n      x-axiom-permission: none\n", 1)},
			[]string{"console.yaml: GET /api/console/v1/version: a public operation has no x-axiom-level or x-axiom-permission"},
		},
		{
			"no permission",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "      x-axiom-permission: things.view\n", "", 1)},
			[]string{"console.yaml: GET /api/console/v1/things: x-axiom-permission is missing (none for no permission)"},
		},
		{
			"an unknown level",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "x-axiom-level: mfa", "x-axiom-level: strong", 1)},
			[]string{"console.yaml: GET /api/console/v1/things: x-axiom-level must be one of [any pending_mfa signed_in mfa]"},
		},
		{
			"a change without the CSRF token",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "[{session: [], csrf: []}]", "[{session: []}]", 1)},
			[]string{"console.yaml: POST /api/console/v1/things: security must be the one requirement csrf + session"},
		},
		{
			"a change without a 403",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "        \"403\": {$ref: \"#/components/responses/Failure\"}\n", "", 1)},
			[]string{"console.yaml: POST /api/console/v1/things: no 403 response (a change is refused for its Origin or CSRF token)"},
		},
		{
			"a failure that is not the error body",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "        \"401\": {$ref: \"#/components/responses/Failure\"}\n        default: {$ref: \"#/components/responses/Failure\"}\n    post", "        \"401\": {description: no}\n        default: {$ref: \"#/components/responses/Failure\"}\n    post", 1)},
			[]string{"console.yaml: GET /api/console/v1/things: response 401 must be the Error body"},
		},
		{
			"a list without the cursor",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "        - $ref: \"#/components/parameters/Cursor\"\n", "", 1)},
			[]string{"console.yaml: GET /api/console/v1/things: a list takes the Cursor parameter"},
		},
		{
			"a change without an idempotency key",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "        - $ref: \"#/components/parameters/IdempotencyKey\"\n", "", 1)},
			[]string{"console.yaml: POST /api/console/v1/things: a change takes the IdempotencyKey parameter (or is marked x-axiom-safe: true)"},
		},
		{
			"a change that gives no reason",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "      required: [reason]\n", "", 1)},
			[]string{"console.yaml: POST /api/console/v1/things: the request body must require a reason"},
		},
		{
			"a safe operation with a key",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "      x-axiom-permission: things.manage\n", "      x-axiom-permission: things.manage\n      x-axiom-safe: true\n", 1)},
			[]string{"console.yaml: POST /api/console/v1/things: an x-axiom-safe operation changes nothing, so it takes no Idempotency-Key"},
		},
		{
			"the same operation twice",
			map[string]string{"console.yaml": strings.Replace(contract("console"), "operationId: makeThing", "operationId: listThings", 1)},
			[]string{`console.yaml: invalid paths: operations "GET /api/console/v1/things" and "POST /api/console/v1/things" have the same operation id "listThings"`},
		},
		{
			"two contracts agree", map[string]string{"console.yaml": contract("console"), "admin.yaml": contract("admin")}, nil,
		},
		{
			"two contracts define a schema differently",
			map[string]string{"console.yaml": contract("console"), "admin.yaml": strings.Replace(contract("admin"), "properties: {error: {type: string}}", "properties: {error: {type: integer}}", 1)},
			[]string{"schema Error differs between admin.yaml and console.yaml"},
		},
		{
			"two contracts define an operation differently",
			map[string]string{"console.yaml": contract("console"), "admin.yaml": strings.Replace(contract("admin"), "x-axiom-permission: things.view", "x-axiom-permission: things.list", 1)},
			[]string{"operation listThings differs between admin.yaml and console.yaml"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string][]byte{}
			for name, text := range c.files {
				files[name] = []byte(text)
			}
			if diff := cmp.Diff(c.want, check(files)); diff != "" {
				t.Errorf("problems (-want +got):\n%s", diff)
			}
		})
	}
}
