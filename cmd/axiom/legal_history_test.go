package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pgtest"
)

// TestLegalDocumentHistoryPages publishes more privacy versions than one page and one terms version. Every version must
// be reachable through next_cursor, including the terms version that a single kind-ordered page would hide.
//
//nolint:cyclop,gocognit // one publish-and-page journey: each step is the pagination contract under test.
func TestLegalDocumentHistoryPages(t *testing.T) {
	t.Parallel()
	c, _ := ownerFixture.get(t, buildOwnerDatabase)
	clock := &testClock{now: time.Now()}
	admin := startAdminNode(c, clock)
	if _, err := admin.user("", "grant-platform", "--email", ownerEmail, "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	site := admin.siteID()
	if err := pgtest.Run(t.Context(), c.database.Superuser, `INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
		SELECT (SELECT id FROM accounts.sites WHERE is_main), 'privacy', 'zh', n,
			'Placeholder privacy policy version ' || n
		FROM generate_series(1, 201) AS n`); err != nil {
		t.Fatal(err)
	}
	if err := pgtest.Run(t.Context(), c.database.Superuser, `INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
		SELECT (SELECT id FROM accounts.sites WHERE is_main), 'terms', 'zh', 1,
			'Placeholder terms of service for tests: this is not a real agreement.'`); err != nil {
		t.Fatal(err)
	}

	operator := admin.owner()
	base := "/sites/" + site + "/legal-documents"
	rec := admin.do(http.MethodGet, base+"?cursor=not-a-cursor", "", as(operator))
	expectAdmin(t, "a malformed legal document cursor", rec, http.StatusBadRequest, "")
	rec = admin.do(http.MethodGet, base+"?cursor=privacy:zh:2147483648", "", as(operator))
	expectAdmin(t, "a legal document cursor beyond int32", rec, http.StatusBadRequest, "")
	rec = admin.do(http.MethodGet, base+"?cursor=privacy:fr:1", "", as(operator))
	expectAdmin(t, "a legal document cursor with an unknown locale", rec, http.StatusBadRequest, "")
	// Both cursor forms are accepted: the old page emits kind:version, while a new page may carry the transitional
	// kind:locale:version. They name the same position while versions remain unique across locales.
	for _, cursor := range []string{"privacy:1", "privacy:zh:1"} {
		rec = admin.do(http.MethodGet, base+"?cursor="+url.QueryEscape(cursor), "", as(operator))
		expectAdmin(t, "a legal document cursor "+cursor, rec, http.StatusOK, "")
		var page adminapi.LegalDocumentHistory
		decode(t, rec, &page)
		if len(page.Documents) != 1 || page.Documents[0].Kind != adminapi.LegalDocumentKind("terms") {
			t.Fatalf("cursor %s: %+v", cursor, page.Documents)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		path := base + "?limit=50"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := admin.do(http.MethodGet, path, "", as(operator))
		expectAdmin(t, "list legal documents page", rec, http.StatusOK, "")
		var page adminapi.LegalDocumentHistory
		decode(t, rec, &page)
		if len(page.Documents) == 0 {
			t.Fatalf("page %d is empty", pages+1)
		}
		for _, document := range page.Documents {
			locale := "zh"
			if document.Locale != nil {
				locale = string(*document.Locale)
			}
			key := fmt.Sprintf("%s:%s:%d", document.Kind, locale, document.Version)
			if seen[key] {
				t.Fatalf("version %s appears twice", key)
			}
			seen[key] = true
		}
		pages++
		if page.NextCursor == nil {
			break
		}
		if *page.NextCursor == cursor {
			t.Fatal("next_cursor does not move")
		}
		if strings.Count(*page.NextCursor, ":") != 1 {
			t.Fatalf("next_cursor %q is not the legacy kind:version form", *page.NextCursor)
		}
		cursor = *page.NextCursor
		if pages > 10 {
			t.Fatal("the history did not end")
		}
	}
	if len(seen) != 202 {
		t.Fatalf("%d versions read, want 202", len(seen))
	}
	if !seen["terms:zh:1"] {
		t.Error("the terms version is missing")
	}
	if pages != 5 {
		t.Errorf("%d pages, want 5", pages)
	}
}
