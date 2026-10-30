-- A snapshot reads one revision per alias: the highest. The table is append-only, so a change never rewrites a row
-- another snapshot may be reading.
-- name: ListAliasesForSnapshot :many
SELECT DISTINCT ON (name) name, model_id FROM catalog.alias ORDER BY name, rev DESC;

-- name: GetAlias :one
SELECT * FROM catalog.alias WHERE name = @name ORDER BY rev DESC LIMIT 1;

-- name: InsertAlias :exec
INSERT INTO catalog.alias (name, rev, model_id) VALUES (@name, @rev, @model_id);

-- name: ListAliases :many
SELECT DISTINCT ON (a.name) a.name, a.rev, a.model_id, a.set_at
FROM catalog.alias a ORDER BY a.name, a.rev DESC;

-- Every alias write takes this transaction's advisory lock, so two first revisions of one name cannot race.
-- name: LockAliases :exec
SELECT pg_advisory_xact_lock(hashtextextended('catalog.alias', 0));
