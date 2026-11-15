package main

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
)

func TestConsoleProfile(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	s := f.owner()
	for _, body := range []string{`{"name":""}`, `{"name":"  "}`, `{"name":"` + strings.Repeat("界", 101) + `"}`} {
		expect(t, "invalid name", f.do(http.MethodPost, "/profile/name", body, as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	}
	expect(t, "trimmed name", f.do(http.MethodPost, "/profile/name", `{"name":" New name "}`, as(s)), http.StatusNoContent, "")
	body := `{"email":"next@example.com","current_password":"` + ownerPassword + `"}`
	expect(t, "anonymous", f.do(http.MethodPost, "/profile/email", body), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "csrf", f.do(http.MethodPost, "/profile/email", body, as(s), header("X-CSRF-Token", "wrong")), http.StatusForbidden, consoleapi.ErrorCodeOriginRejected)
	expect(t, "wrong password", f.do(http.MethodPost, "/profile/email", strings.Replace(body, ownerPassword, "wrong", 1), as(s)), http.StatusUnauthorized, consoleapi.ErrorCodeInvalidCredentials)
	expect(t, "taken email", f.do(http.MethodPost, "/profile/email", strings.Replace(body, "next@example.com", ownerEmail, 1), as(s)), http.StatusConflict, consoleapi.ErrorCodeEmailTaken)
	expect(t, "request change", f.do(http.MethodPost, "/profile/email", body, as(s)), http.StatusAccepted, "")
	expect(t, "invalid confirmation", f.do(http.MethodPost, "/profile/email/confirm", `{"token":"wrong"}`, as(s)), http.StatusUnprocessableEntity, consoleapi.ErrorCodeInvalidVerification)
}

func TestConsoleDisplayNameReadsBack(t *testing.T) {
	t.Parallel()
	f := passwordConsole(t)
	session := f.owner()
	expect(t, "anonymous read", f.do(http.MethodGet, "/profile/name", ""), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "change name", f.do(http.MethodPost, "/profile/name", `{"name":" New name "}`, as(session)), http.StatusNoContent, "")
	response := f.do(http.MethodGet, "/profile/name", "", as(session))
	expect(t, "read name", response, http.StatusOK, "")
	var body struct {
		Name *string `json:"name"`
	}
	if err := jsonx.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Name == nil || *body.Name != "New name" {
		t.Fatalf("name did not survive refresh: %+v", body.Name)
	}
}
