// Command checkapi fails when an API contract in api/ is not valid OpenAPI 3.0.3 or breaks the conventions of
// internal/platform/README.md「接口约定」.
//
// Every api/<surface>.yaml is loaded and validated with kin-openapi, then each operation is held to the conventions
// the generated code and the two sides cannot see: its security is declared and says what the session must have
// proved and which permission it needs, a request that changes data is protected against forgery and refused with a 403,
// every failure has the one error body, a list takes the cursor and limit, and a contract that has idempotency keys
// asks for one on every change. The surfaces share the session operations and a few schemas by being written twice, so
// what two contracts both define under one name must be the same definition.
//
// usage: go run -C tools ./checkapi <repository root>
package main

import (
	"context"
	"encoding/json/v2" //nolint:depguard // tools is its own module, which cannot import axiom/internal
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const (
	errorSchema    = "#/components/schemas/Error"
	idempotencyRef = "#/components/parameters/IdempotencyKey"
	sessionTag     = "Session"
)

// levels are the values of x-axiom-level: what a session must have proved.
var levels = []string{"any", "pending_mfa", "signed_in", "mfa"}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: checkapi <repository root>")
		os.Exit(2)
	}
	files, err := readContracts(filepath.Join(os.Args[1], "api"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	problems := check(files)
	for _, problem := range problems {
		fmt.Println(problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

// readContracts returns the text of every api/*.yaml by file name.
func readContracts(dir string) (map[string][]byte, error) {
	root := os.DirFS(dir)
	names, err := fs.Glob(root, "*.yaml")
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf("no api/*.yaml under %s: %w", dir, err)
	}
	files := map[string][]byte{}
	for _, name := range names {
		if files[name], err = fs.ReadFile(root, name); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// check returns the problems of the contracts, by file name, in a fixed order.
func check(files map[string][]byte) []string {
	var problems []string
	docs := map[string]*openapi3.T{}
	for _, name := range sortedKeys(files) {
		doc, err := openapi3.NewLoader().LoadFromData(files[name])
		if err == nil {
			err = doc.Validate(context.Background())
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		docs[name] = doc
		problems = append(problems, conventions(name, doc)...)
	}
	problems = append(problems, sameNameSameDefinition(docs)...)
	sort.Strings(problems)
	return problems
}

type operation struct {
	path, method string
	op           *openapi3.Operation
}

// operations returns the operations of the contract, by path and method.
func operations(doc *openapi3.T) []operation {
	var ops []operation
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			ops = append(ops, operation{path, method, op})
		}
	}
	return ops
}

func (o operation) mutating() bool { return o.method != http.MethodGet && o.method != http.MethodHead }

func (o operation) public() bool { return o.op.Security != nil && len(*o.op.Security) == 0 }

// flag reads a boolean extension; an absent one is false.
func (o operation) flag(name string) bool {
	v, _ := o.op.Extensions[name].(bool)
	return v
}

func conventions(file string, doc *openapi3.T) []string {
	var problems []string
	prefix := "/api/" + strings.TrimSuffix(file, ".yaml") + "/v1/"
	for _, o := range operations(doc) {
		where := fmt.Sprintf("%s: %s %s", file, o.method, o.path)
		var found []string
		if !strings.HasPrefix(o.path, prefix) {
			found = append(found, "the path must start with "+prefix)
		}
		found = append(found, identity(o)...)
		found = append(found, security(file, o)...)
		found = append(found, responses(o)...)
		found = append(found, requests(doc, o)...)
		for _, f := range found {
			problems = append(problems, where+": "+f)
		}
	}
	return problems
}

// identity: the generated code and the clients name an operation by its ID, and the docs by its tag and summary.
func identity(o operation) []string {
	var found []string
	if o.op.OperationID == "" {
		found = append(found, "no operationId")
	}
	if len(o.op.Tags) == 0 {
		found = append(found, "no tag")
	}
	if o.op.Summary == "" {
		found = append(found, "no summary")
	}
	return found
}

// platformOperation: every admin operation outside the Session tag acts on the platform, so it takes the strongest
// proof (internal/platform/README.md「接口约定」), and a downgrade of one is a security regression.
func platformOperation(file string, o operation) bool {
	return file == "admin.yaml" && !slices.Contains(o.op.Tags, sessionTag)
}

// security: the session, the CSRF token for a change, the level and the permission, all written down.
func security(file string, o operation) []string {
	level, permission := o.op.Extensions["x-axiom-level"], o.op.Extensions["x-axiom-permission"]
	platform := platformOperation(file, o)
	switch {
	case o.op.Security == nil:
		return []string{"security is not declared (write security: [] for a public operation)"}
	case o.public():
		return publicProblems(platform, level != nil || permission != nil)
	}
	want := []string{"session"}
	if o.mutating() {
		want = append(want, "csrf")
	}
	var found []string
	if reqs := *o.op.Security; len(reqs) != 1 || !sameKeys(reqs[0], want) {
		found = append(found, "security must be the one requirement "+strings.Join(want, " + "))
	}
	if l, _ := level.(string); !slices.Contains(levels, l) {
		found = append(found, fmt.Sprintf("x-axiom-level must be one of %v", levels))
	}
	p, _ := permission.(string)
	if p == "" {
		found = append(found, "x-axiom-permission is missing (none for no permission)")
	}
	if platform {
		found = append(found, platformProof(level, p)...)
	}
	return found
}

func publicProblems(platform, proofs bool) []string {
	switch {
	case platform:
		return []string{"an admin operation outside the Session tag needs a session"}
	case proofs:
		return []string{"a public operation has no x-axiom-level or x-axiom-permission"}
	}
	return nil
}

func platformProof(level any, permission string) []string {
	var found []string
	if level != "mfa" {
		found = append(found, "an admin operation outside the Session tag needs x-axiom-level: mfa")
	}
	if permission == "none" {
		found = append(found, "an admin operation outside the Session tag needs a permission name")
	}
	return found
}

func sameKeys(req openapi3.SecurityRequirement, want []string) bool {
	got := make([]string, 0, len(req))
	for k := range req {
		got = append(got, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

// responses: every failure has the Error body, a default exists, and the 401 and 403 that security can cause are there.
func responses(o operation) []string {
	found := errorBodies(o)
	if o.op.Responses.Value("default") == nil {
		found = append(found, "no default response")
	}
	if o.op.Security != nil && !o.public() && o.op.Responses.Value("401") == nil {
		found = append(found, "no 401 response")
	}
	if o.mutating() && o.op.Responses.Value("403") == nil {
		found = append(found, "no 403 response (a change is refused for its Origin or CSRF token)")
	}
	return found
}

// errorBodies: every response that is not a success carries the one error body.
func errorBodies(o operation) []string {
	var found []string
	for code, r := range o.op.Responses.Map() {
		failure := code == "default" || !strings.HasPrefix(code, "2") && !strings.HasPrefix(code, "3")
		if media := r.Value.Content.Get("application/json"); failure && (media == nil || media.Schema.Ref != errorSchema) {
			found = append(found, "response "+code+" must be the Error body")
		}
	}
	return found
}

// requests: a list takes the cursor and limit, a body is JSON, and a contract with idempotency keys asks for one.
func requests(doc *openapi3.T, o operation) []string {
	refs := map[string]bool{}
	for _, p := range o.op.Parameters {
		refs[p.Ref] = true
	}
	found := pageParameters(o, refs)
	if body := o.op.RequestBody; body != nil && (len(body.Value.Content) != 1 || body.Value.Content.Get("application/json") == nil) {
		found = append(found, "a request body is application/json only")
	}
	if hasIdempotencyKeys(doc) && o.mutating() && !slices.Contains(o.op.Tags, sessionTag) {
		found = append(found, idempotency(o, refs[idempotencyRef])...)
	}
	return found
}

func hasIdempotencyKeys(doc *openapi3.T) bool {
	return doc.Components != nil && doc.Components.Parameters["IdempotencyKey"] != nil
}

// pageParameters: an operation that answers a page takes the cursor and the limit.
func pageParameters(o operation, refs map[string]bool) []string {
	var found []string
	if ok := o.op.Responses.Value("200"); ok != nil && listsPage(ok.Value) {
		for _, p := range []string{"Cursor", "Limit"} {
			if !refs["#/components/parameters/"+p] {
				found = append(found, "a list takes the "+p+" parameter")
			}
		}
	}
	return found
}

// listsPage reports whether a response is a page, which is an object with a next_cursor.
func listsPage(r *openapi3.Response) bool {
	media := r.Content.Get("application/json")
	return media != nil && media.Schema != nil && media.Schema.Value.Properties["next_cursor"] != nil
}

// idempotency: a change takes an Idempotency-Key and says why in a reason; a safe POST takes neither.
func idempotency(o operation, hasKey bool) []string {
	switch {
	case o.flag("x-axiom-safe") && hasKey:
		return []string{"an x-axiom-safe operation changes nothing, so it takes no Idempotency-Key"}
	case o.flag("x-axiom-safe"):
		return nil
	case !hasKey:
		return []string{"a change takes the IdempotencyKey parameter (or is marked x-axiom-safe: true)"}
	case o.op.RequestBody == nil || !hasReason(o.op.RequestBody.Value.Content.Get("application/json")):
		return []string{"the request body must require a reason"}
	}
	return nil
}

func hasReason(media *openapi3.MediaType) bool {
	if media == nil || media.Schema == nil {
		return false
	}
	s := media.Schema.Value
	return s.Properties["reason"] != nil && slices.Contains(s.Required, "reason")
}

// definitions returns what a contract defines by a name that tells the kind: its components and its operations.
func definitions(doc *openapi3.T) map[string]any {
	defs := map[string]any{}
	if c := doc.Components; c != nil {
		for name, v := range c.Schemas {
			defs["schema "+name] = v
		}
		for name, v := range c.Parameters {
			defs["parameter "+name] = v
		}
		for name, v := range c.Headers {
			defs["header "+name] = v
		}
		for name, v := range c.Responses {
			defs["response "+name] = v
		}
		for name, v := range c.SecuritySchemes {
			defs["security scheme "+name] = v
		}
	}
	for _, o := range operations(doc) {
		defs["operation "+o.op.OperationID] = o.op
	}
	return defs
}

// sameNameSameDefinition: the contracts repeat what they share, and a repeat that differs would be one copy changed
// and the other forgotten.
func sameNameSameDefinition(docs map[string]*openapi3.T) []string {
	encoded := map[string]map[string]string{} // what is defined -> file -> its definition
	var problems []string
	for file, doc := range docs {
		for what, v := range definitions(doc) {
			text, err := json.Marshal(v, json.Deterministic(true))
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %s: %v", file, what, err))
				continue
			}
			if encoded[what] == nil {
				encoded[what] = map[string]string{}
			}
			encoded[what][file] = string(text)
		}
	}
	for what, byFile := range encoded {
		files := sortedKeys(byFile)
		for _, f := range files[1:] {
			if byFile[f] != byFile[files[0]] {
				problems = append(problems, fmt.Sprintf("%s differs between %s and %s", what, files[0], f))
			}
		}
	}
	return problems
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
