-- name: InsertAuditEntry :exec
INSERT INTO platform.audit_log (actor_kind, actor_id, request_id, action, module, target_kind, target_id, site_id,
    account_id, reason, detail)
VALUES (@actor_kind, @actor_id, nullif(@request_id::text, '')::uuid, @action, @module, @target_kind, @target_id,
    nullif(@site_id::text, '')::uuid, nullif(@account_id::text, '')::uuid, nullif(@reason::text, ''), @detail);
