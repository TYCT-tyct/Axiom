-- name: InsertModel :one
INSERT INTO catalog.model (author, slug, version_basis, price_source, spec)
VALUES (@author, @slug, @version_basis, @price_source, @spec)
RETURNING *;

-- Every change to a model starts by locking its row, so two operations on one model take turns and the second sees
-- the first's rev.
-- name: LockModel :one
SELECT * FROM catalog.model WHERE id = @id FOR UPDATE;

-- name: UpdateModel :exec
UPDATE catalog.model SET status = @status, price_source = @price_source, spec = @spec, rev = rev + 1 WHERE id = @id;

-- name: ListModelsForSnapshot :many
SELECT * FROM catalog.model WHERE status IN ('listed', 'retired');

-- A dataset sync follows the models that take their prices from it, drafts too: a draft's first price is how it gets listed.
-- name: ListModelsForSync :many
SELECT * FROM catalog.model WHERE status IN ('draft', 'listed') AND price_source = @price_source ORDER BY id;

-- name: ListModelsOfAuthors :many
SELECT * FROM catalog.model WHERE author = ANY(@authors::text[]) AND status <> 'retired' ORDER BY slug;

-- name: GetModel :one
SELECT * FROM catalog.model WHERE id = @id;
