-- name: InsertChange :one
INSERT INTO catalog.change (op_key, actor, reason, kind, subject, before, after, evidence)
VALUES (@op_key, @actor, @reason, @kind, @subject, @before, @after, @evidence)
RETURNING id;

-- name: GetChangeByOpKey :one
SELECT id, kind, subject FROM catalog.change WHERE op_key = @op_key;
