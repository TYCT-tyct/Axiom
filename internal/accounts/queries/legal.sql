-- The versioned legal documents of a site and the versions its users accepted (README.md「注册同意」). The documents
-- are public data, readable in any scope; the acceptances are account data. A document has one version stream per
-- kind and locale, and an acceptance names the exact locale and version that was shown.

-- name: CurrentLegalDocuments :many
-- The latest version of each kind in each locale, which is what a registration or a page may show.
SELECT d.kind, d.locale, d.version, d.body, d.published_at
FROM accounts.legal_documents d
WHERE d.site_id = @site_id
    AND d.version = (SELECT max(x.version) FROM accounts.legal_documents x
        WHERE x.site_id = d.site_id AND x.kind = d.kind AND x.locale = d.locale)
ORDER BY d.kind, d.locale;

-- name: LegalDocumentHistory :many
-- Every version of every kind and locale, newest first within a pair, after the cursor position
-- (kind, locale, version). The reads are the admin's, in the platform scope.
SELECT kind, locale, version, body, published_at
FROM accounts.legal_documents
WHERE site_id = @site_id
    AND (
        @cursor_kind::text = ''
        OR kind > @cursor_kind
        OR (kind = @cursor_kind AND locale > @cursor_locale)
        OR (kind = @cursor_kind AND locale = @cursor_locale AND version < @cursor_version)
    )
ORDER BY kind, locale, version DESC
LIMIT @page_size;

-- name: NextLegalVersion :one
-- The next version of a kind and locale, read under the site's lock so two publications cannot choose the same one.
SELECT (coalesce(max(version), 0) + 1)::integer AS version
FROM accounts.legal_documents
WHERE site_id = @site_id AND kind = @kind AND locale = @locale;

-- name: InsertLegalDocument :one
INSERT INTO accounts.legal_documents (site_id, kind, locale, version, body)
VALUES (@site_id, @kind, @locale, @version, @body)
RETURNING published_at;

-- name: AcceptedLegalVersions :many
-- The newest version each user accepted per kind and locale. Versions only grow, so the newest one is what the user
-- holds in that locale.
SELECT kind, locale, max(version)::integer AS version
FROM accounts.legal_acceptances
WHERE site_id = @site_id AND user_id = @user_id
GROUP BY kind, locale;

-- name: InsertLegalAcceptance :exec
-- A retried acceptance of the same locale and version changes nothing: the first time is the time the person accepted it.
INSERT INTO accounts.legal_acceptances (id, site_id, account_id, user_id, kind, locale, version)
VALUES (@id, @site_id, @account_id, @user_id, @kind, @locale, @version)
ON CONFLICT (user_id, kind, locale, version) DO NOTHING;
