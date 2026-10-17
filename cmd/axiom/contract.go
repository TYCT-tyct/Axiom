package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
)

// consoleContract is the console's contract as the generated code embeds it, with the rules read out of it.
func consoleContract() (*contract, error) {
	doc, err := consoleapi.GetSpec()
	if err != nil {
		return nil, fmt.Errorf("the console contract: %w", err)
	}
	c, err := loadContract(doc)
	if err != nil {
		return nil, fmt.Errorf("the console contract: %w", err)
	}
	return c, nil
}

// The levels an operation's `x-axiom-level` names (internal/platform/README.md「接口约定」).
const (
	levelAny        = "any"
	levelPendingMFA = "pending_mfa"
	levelSignedIn   = "signed_in"
	levelMFA        = "mfa"
)

// rules is what the contract says an operation needs before its handler runs, read from the contract at start.
type rules struct {
	operation string
	// session is the operation's need of a session, with the level it must have proved; public operations have none.
	session bool
	level   string
	// permission is what the session needs on its own account; nil for `none`.
	permission *accounts.Permission
	// changes data: such a request must come from the site's origin as JSON, and with a session carry its CSRF token.
	changes bool
	// params are the query and header parameters; a path parameter is not checked (a malformed ID is a 404).
	params []*openapi3.Parameter
	body   *openapi3.RequestBody
}

// contract is the console's API as api/console.yaml declares it: the rules of each operation, by the pattern its
// route is registered under ("GET /api/console/v1/keys/{key_id}"), and the checks of requests against its schemas.
type contract struct {
	byPattern map[string]*rules
}

// loadContract reads the rules out of doc, the contract the generated code embeds (consoleapi.GetSpec), and refuses a
// contract the program cannot enforce: an operation without a security declaration, a session operation without a
// level or with a permission this program does not declare, or a session operation that changes data without the CSRF
// scheme. The rules are the contract's own, so they cannot drift from it.
func loadContract(doc *openapi3.T) (*contract, error) {
	c := &contract{byPattern: map[string]*rules{}}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			r, err := rulesOf(method, op, item.Parameters)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			c.byPattern[method+" "+path] = r
		}
	}
	return c, nil
}

func rulesOf(method string, op *openapi3.Operation, pathParameters openapi3.Parameters) (*rules, error) {
	r := &rules{operation: op.OperationID, changes: method != http.MethodGet && method != http.MethodHead}
	if op.Security == nil {
		return nil, errors.New("no security declaration (an empty one is public)")
	}
	var csrf bool
	for _, requirement := range *op.Security {
		_, session := requirement["session"]
		_, withCSRF := requirement["csrf"]
		r.session, csrf = r.session || session, csrf || withCSRF
	}
	if r.session {
		if err := r.sessionRules(op, csrf); err != nil {
			return nil, err
		}
	}
	for _, p := range append(append(openapi3.Parameters{}, pathParameters...), op.Parameters...) {
		if in := p.Value.In; in == openapi3.ParameterInQuery || in == openapi3.ParameterInHeader {
			r.params = append(r.params, p.Value)
		}
	}
	if op.RequestBody != nil {
		r.body = op.RequestBody.Value
	}
	return r, nil
}

// sessionRules reads the level and permission of an operation that needs a session.
func (r *rules) sessionRules(op *openapi3.Operation, csrf bool) error {
	if r.changes && !csrf {
		return errors.New("changes data with a session but declares no csrf scheme")
	}
	level, _ := op.Extensions["x-axiom-level"].(string)
	switch level {
	case levelAny, levelPendingMFA, levelSignedIn, levelMFA:
		r.level = level
	default:
		return fmt.Errorf("x-axiom-level %q is not any, pending_mfa, signed_in or mfa", level)
	}
	name, _ := op.Extensions["x-axiom-permission"].(string)
	if name == "none" {
		return nil
	}
	permission, ok := accounts.PermissionNamed(name)
	if !ok {
		return fmt.Errorf("x-axiom-permission %q is not declared by this program", name)
	}
	r.permission = &permission
	return nil
}

// validate checks the request's query and header parameters and body against the contract's schemas: patterns, lengths, ranges,
// enums and what is required. Path parameters are not checked here: a malformed ID is a 404 like an unknown one. The
// body is read and put back for the handler. It returns the parts of the request that broke the contract, naming
// none of their values.
func (r *rules) validate(ctx context.Context, req *http.Request) []errorField {
	input := &openapi3filter.RequestValidationInput{
		Request: req, Options: &openapi3filter.Options{MultiError: true, SkipSettingDefaults: true},
	}
	var errs []error
	for _, p := range r.params {
		if err := openapi3filter.ValidateParameter(ctx, input, p); err != nil {
			errs = append(errs, err)
		}
	}
	if r.body != nil {
		if err := openapi3filter.ValidateRequestBody(ctx, input, r.body); err != nil {
			errs = append(errs, err)
		}
	}
	var fields []errorField
	for _, err := range errs {
		fields = appendFields(fields, "", err)
	}
	return fields
}

// appendFields flattens a validation error into the parts of the request it names. A value never appears in a
// message: only the schema's rule does.
func appendFields(fields []errorField, parameter string, err error) []errorField {
	switch e := err.(type) { //nolint:errorlint // the validator's own types, matched one level at a time
	case openapi3.MultiError:
		for _, inner := range e {
			fields = appendFields(fields, parameter, inner)
		}
		return fields
	case *openapi3filter.RequestError:
		if e.Parameter != nil {
			parameter = e.Parameter.Name
		}
		if e.Err == nil {
			return append(fields, errorField{Pointer: parameter, Message: e.Reason})
		}
		return appendFields(fields, parameter, e.Err)
	case *openapi3.SchemaError:
		pointer := parameter
		if parameter == "" {
			pointer = "/" + strings.Join(e.JSONPointer(), "/")
		}
		return append(fields, errorField{Pointer: strings.TrimSuffix(pointer, "/"), Message: e.Reason})
	}
	if errors.Is(err, openapi3filter.ErrInvalidRequired) {
		return append(fields, errorField{Pointer: parameter, Message: "a value is required"})
	}
	return append(fields, errorField{Pointer: parameter, Message: "the value or the request body is malformed"})
}
