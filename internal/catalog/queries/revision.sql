-- name: GetRevision :one
SELECT n FROM catalog.revision;

-- name: BumpRevision :exec
UPDATE catalog.revision SET n = n + 1;
