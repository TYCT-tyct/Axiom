-- What the admin API lists and reads of the catalog, in the platform scope: drafts included.

-- name: ListAuthors :many
SELECT * FROM catalog.author ORDER BY slug;

-- name: ListModelsPage :many
-- By slug, a page starting after the slug the page before ended with. The prefix is matched literally (the caller escapes
-- % and _).
SELECT * FROM catalog.model
WHERE (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
    AND (sqlc.narg(author)::text IS NULL OR author = sqlc.narg(author))
    AND (sqlc.narg(slug_prefix)::text IS NULL OR slug LIKE sqlc.narg(slug_prefix) || '%')
    AND (sqlc.narg(after)::text IS NULL OR slug > sqlc.narg(after))
ORDER BY slug
LIMIT @page_size::int;
