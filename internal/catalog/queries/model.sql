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
