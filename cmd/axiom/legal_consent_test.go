package main

import (
	"net/http"
	"testing"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
)

// Placeholder legal text for tests. It is deliberately not a real agreement.
const (
	testTerms   = "Placeholder terms of service for tests: this is not a real agreement."
	testPrivacy = "Placeholder privacy policy for tests: this is not a real policy."
)

// TestLegalConsentHTTP is the whole mechanism through both listeners: an admin publishes versioned documents, the
// public console route shows them, an existing user is refused until accepting and again after a new version, and an
// invite registration records the versions it accepted. The two fixtures share one database, as two listeners of one
// deployment do.
//
//nolint:cyclop,gocognit // one publish-to-reaccept flow: each step is the mechanism under test.
func TestLegalConsentHTTP(t *testing.T) {
	t.Parallel()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	clock := &testClock{now: time.Now()}
	admin := startAdminNode(c, clock)
	if _, err := admin.user("", "grant-platform", "--email", ownerEmail, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	const memberEmail, memberPassword = "member@example.com", "another horse battery"
	if _, err := admin.user(memberPassword+"\n", "create", "--email", memberEmail); err != nil {
		t.Fatal(err)
	}
	console := startNode(c, clock, 0)
	site := admin.siteID()
	path := "/sites/" + site + "/legal-documents"

	// A site that publishes nothing requires nothing and shows an empty list.
	rec := console.do(http.MethodGet, "/legal", "")
	expect(t, "legal documents before publishing", rec, http.StatusOK, "")
	var public consoleapi.LegalDocuments
	decode(t, rec, &public)
	if len(public.Documents) != 0 {
		t.Fatalf("documents before publishing: %+v", public.Documents)
	}

	operator := admin.owner()
	publish := func(kind, body string) adminapi.LegalDocument {
		t.Helper()
		rec := admin.change(operator, http.MethodPost, path,
			`{"kind":"`+kind+`","body":"`+body+`","reason":"legal test"}`, newKey())
		expectAdmin(t, "publish "+kind, rec, http.StatusCreated, "")
		var document adminapi.LegalDocument
		decode(t, rec, &document)
		return document
	}
	terms1 := publish("terms", testTerms)
	privacy1 := publish("privacy", testPrivacy)
	if terms1.Version != 1 || privacy1.Version != 1 || terms1.Kind != adminapi.LegalDocumentKind("terms") {
		t.Fatalf("first versions: %+v %+v", terms1, privacy1)
	}

	rec = admin.do(http.MethodGet, path, "", as(operator))
	expectAdmin(t, "list legal documents", rec, http.StatusOK, "")
	var history adminapi.LegalDocumentHistory
	decode(t, rec, &history)
	if len(history.Documents) != 2 || history.Documents[0].Body != testPrivacy {
		t.Fatalf("history: %+v", history.Documents)
	}
	if got := admin.auditCount("site_legal_document.publish"); got != "2" {
		t.Errorf("%s publication audit entries, want 2", got)
	}

	rec = console.do(http.MethodGet, "/legal", "")
	expect(t, "legal documents after publishing", rec, http.StatusOK, "")
	decode(t, rec, &public)
	if len(public.Documents) != 2 || public.Documents[0].Kind != consoleapi.LegalDocumentKind("privacy") ||
		public.Documents[1].Body != testTerms || public.Documents[1].PublishedAt.IsZero() {
		t.Fatalf("public documents: %+v", public.Documents)
	}

	// An existing user, made before the documents existed, must accept before the console does anything else.
	owner := console.signIn(memberEmail, memberPassword)
	rec = console.do(http.MethodGet, "/session", "", as(owner))
	expect(t, "session before accepting", rec, http.StatusOK, "")
	var session consoleapi.Session
	decode(t, rec, &session)
	if session.Consent == nil || len(session.Consent.Required) != 2 {
		t.Fatalf("the session's pending consent: %+v", session.Consent)
	}
	expect(t, "a key list before accepting", console.do(http.MethodGet, "/keys", "", as(owner)), http.StatusForbidden, consoleapi.ErrorCodeConsentRequired)
	expect(t, "a stale acceptance", console.do(http.MethodPost, "/legal/accept",
		`{"consent":[{"kind":"terms","version":2},{"kind":"privacy","version":1}]}`, as(owner)),
		http.StatusForbidden, consoleapi.ErrorCodeConsentRequired)
	expect(t, "accepting both documents", console.do(http.MethodPost, "/legal/accept",
		`{"consent":[{"kind":"terms","version":1},{"kind":"privacy","version":1}]}`, as(owner)), http.StatusNoContent, "")
	expect(t, "a key list after accepting", console.do(http.MethodGet, "/keys", "", as(owner)), http.StatusOK, "")

	terms2 := publish("terms", testTerms+" Version two.")
	if terms2.Version != 2 {
		t.Fatalf("second terms version: %+v", terms2)
	}
	expect(t, "a key list after a new version", console.do(http.MethodGet, "/keys", "", as(owner)), http.StatusForbidden, consoleapi.ErrorCodeConsentRequired)
	rec = console.do(http.MethodGet, "/session", "", as(owner))
	expect(t, "session after a new version", rec, http.StatusOK, "")
	decode(t, rec, &session)
	if session.Consent == nil || len(session.Consent.Required) != 1 || session.Consent.Required[0].Version != 2 {
		t.Fatalf("the session's pending consent after a new version: %+v", session.Consent)
	}
	expect(t, "accepting the new version", console.do(http.MethodPost, "/legal/accept",
		`{"consent":[{"kind":"terms","version":2},{"kind":"privacy","version":1}]}`, as(owner)), http.StatusNoContent, "")
	expect(t, "a key list after accepting the new version", console.do(http.MethodGet, "/keys", "", as(owner)), http.StatusOK, "")

	// An invite registration records the versions with the user it makes.
	rec = console.do(http.MethodPost, "/auth/register",
		`{"invite_code":"`+session.User.InviteCode+`","email":"consented@example.com","password":"another horse battery"}`)
	expect(t, "an invite registration without consent", rec, http.StatusForbidden, consoleapi.ErrorCodeConsentRequired)
	rec = console.do(http.MethodPost, "/auth/register",
		`{"invite_code":"`+session.User.InviteCode+`","email":"consented@example.com","password":"another horse battery",`+
			`"consent":[{"kind":"terms","version":2},{"kind":"privacy","version":1}]}`)
	expect(t, "an invite registration with consent", rec, http.StatusCreated, "")
	if got := admin.text(`SELECT string_agg(a.kind || ' ' || a.version, ',' ORDER BY a.kind) FROM accounts.legal_acceptances a
		JOIN accounts.users u ON u.id = a.user_id WHERE u.email = 'consented@example.com'`); got != "privacy 1,terms 2" {
		t.Errorf("the registration's acceptances: %q", got)
	}
}
