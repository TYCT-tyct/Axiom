-- name: InsertName :exec
INSERT INTO catalog.name (name, norm, visibility, model_id) VALUES (@name, @norm, @visibility, @model_id);

-- name: GetNameByNorm :one
SELECT * FROM catalog.name WHERE norm = @norm;

-- name: ListNamesOfModel :many
SELECT * FROM catalog.name WHERE model_id = @model_id;

-- name: ListNamesForSnapshot :many
SELECT n.* FROM catalog.name n JOIN catalog.model m ON m.id = n.model_id WHERE m.status IN ('listed', 'retired');
