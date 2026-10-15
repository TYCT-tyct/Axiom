-- name: InsertAuditEntry :exec
INSERT INTO platform.audit_log (actor_kind, actor_id, request_id, action, module, target_kind, target_id, reason, detail)
VALUES (@actor_kind, @actor_id, nullif(@request_id::text, '')::uuid, @action, @module, @target_kind, @target_id,
    nullif(@reason::text, ''), @detail);
