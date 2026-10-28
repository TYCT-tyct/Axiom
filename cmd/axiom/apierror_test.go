package main

import (
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
)

// TestErrorCodesAreTheContractsCodes: the codes the guard and the handlers answer with are exactly the closed list both
// contracts give, so a code a client does not know is never sent and none the contract names is unreachable by
// mistake.
func TestErrorCodesAreTheContractsCodes(t *testing.T) {
	t.Parallel()
	ours := []string{}
	for _, c := range []errorCode{
		codeInvalidRequest, codeUnauthenticated, codeWrongLogin, codeInvalidCode, codeForbidden, codeMfaRequired,
		codeOriginRejected, codeNotFound, codeConflict, codeEmailTaken, codeNameTaken, codeTooManyKeys, codeStaleRevision, codeIdempotencyReuse,
		codeInvalidInvite, codeWeakPassword, codeNotReady, codeUnprocessable, codeRateLimited, codeInternal,
		codeNotImplemented, codeUnavailable,
	} {
		ours = append(ours, string(c))
	}
	slices.Sort(ours)
	for name, spec := range map[string]func() (*openapi3.T, error){"console": consoleapi.GetSpec, "admin": adminapi.GetSpec} {
		doc, err := spec()
		if err != nil {
			t.Fatal(err)
		}
		var theirs []string
		for _, v := range doc.Components.Schemas["ErrorCode"].Value.Enum {
			theirs = append(theirs, v.(string))
		}
		slices.Sort(theirs)
		if !slices.Equal(ours, theirs) {
			t.Errorf("the %s contract's codes %v, the program's %v", name, theirs, ours)
		}
	}
}
