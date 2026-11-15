package main

import (
	"net/http"
	"strings"
	"testing"

	"axiom/internal/platform/consoleapi"
)

func TestConsoleInvitations(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	var body consoleapi.Invitations
	rec := f.do(http.MethodGet, "/invitations", "", as(s), atHost(strings.ToUpper(consoleHost)), header("Origin", "https://untrusted.example"))
	decode(t, rec, &body)
	if body.Code == nil || body.Link == nil || *body.Link != consoleOrigin+"/console/register?invite="+*body.Code || body.Invitees == nil || len(*body.Invitees) != 0 {
		t.Fatalf("invitations: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("last page cursor: %s", rec.Body)
	}
	checkConsoleInvitationPages(t, f, s, *body.Code)
	expect(t, "no session", f.do(http.MethodGet, "/invitations", ""), http.StatusUnauthorized, consoleapi.ErrorCodeUnauthenticated)
	expect(t, "bad cursor", f.do(http.MethodGet, "/invitations?cursor=bad", "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	expect(t, "oversized page", f.do(http.MethodGet, "/invitations?limit=201", "", as(s)), http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
}

func checkConsoleInvitationPages(t *testing.T, f consoleFixture, s session, code string) {
	t.Helper()
	var body consoleapi.Invitations
	for _, email := range []string{"alice@gmail.com", "bob@example.com"} {
		registration := `{"invite_code":"` + code + `","email":"` + email + `","password":"` + ownerPassword + `"}`
		expect(t, "registration", f.do(http.MethodPost, "/auth/register", registration), http.StatusCreated, "")
	}
	decode(t, f.do(http.MethodGet, "/invitations?limit=1", "", as(s)), &body)
	if body.NextCursor == nil || body.Invitees == nil || len(*body.Invitees) != 1 || *(*body.Invitees)[0].Display != "b***@e***.com" {
		t.Fatalf("first page: %+v", body)
	}
	var last consoleapi.Invitations
	response := f.do(http.MethodGet, "/invitations?limit=1&cursor="+*body.NextCursor, "", as(s))
	decode(t, response, &last)
	if last.NextCursor != nil || last.Invitees == nil || len(*last.Invitees) != 1 || *(*last.Invitees)[0].Display != "a***@g***.com" || strings.Contains(response.Body.String(), "alice@gmail.com") {
		t.Fatalf("last page: %s", response.Body)
	}
}
