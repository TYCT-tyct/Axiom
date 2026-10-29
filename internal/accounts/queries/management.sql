-- name: ValidManagementKey :one
-- Mutable authorization stays under the manager's row lock until its operation commits.
SELECT k.id, k.workspace_id, k.kind, k.created_by, k.name, k.prefix, k.last4, k.created_at, k.updated_at, k.expires_at,
    k.status, k.spend_limit, k.limit_reset, k.models, k.ip_allow
FROM accounts.api_keys k
JOIN accounts.accounts a ON a.site_id = k.site_id AND a.id = k.account_id
JOIN accounts.workspaces w ON w.site_id = k.site_id AND w.account_id = k.account_id AND w.id = k.workspace_id
WHERE k.id = @id AND k.kind = 'management' AND k.status = 'active' AND k.deleted_at IS NULL
    AND k.expires_at > statement_timestamp()
    AND accounts.domain_site(@domain::text, 'api') = k.site_id
    AND (k.ip_allow IS NULL OR @client_ip::inet <<= ANY(k.ip_allow))
    AND a.status = 'active' AND w.status = 'active' AND w.deleted_at IS NULL;

-- name: ChargeManagementRequest :one
-- Separate committed transaction: an invalid operation must not return its rate-limit slot.
UPDATE accounts.api_keys SET
    management_requests = CASE WHEN management_window >= date_trunc('minute', statement_timestamp())
        THEN least(management_requests + 1, 61) ELSE 1 END,
    management_window = greatest(management_window, date_trunc('minute', statement_timestamp()))
WHERE id = @id
RETURNING management_requests <= 60 AS allowed,
    ceil(extract(epoch FROM management_window + interval '1 minute' - statement_timestamp()))::bigint AS retry_seconds;

-- name: ListManagedKeys :many
SELECT k.id, k.workspace_id, k.kind, k.created_by, k.name, k.prefix, k.last4, k.created_at, k.updated_at, k.expires_at,
    k.status, k.spend_limit, k.limit_reset, k.models, k.ip_allow
FROM accounts.api_keys k
WHERE k.account_id = @account_id AND k.kind = 'call' AND k.deleted_at IS NULL
    AND (@include_disabled::boolean OR k.status = 'active')
    AND (@include_expired::boolean OR k.expires_at IS NULL OR k.expires_at > statement_timestamp())
    AND k.workspace_id = coalesce(sqlc.narg(workspace)::uuid,
        (SELECT w.id FROM accounts.workspaces w WHERE w.account_id = @account_id AND w.is_default))
ORDER BY k.created_at DESC, k.id DESC LIMIT 100 OFFSET @page_offset::bigint;

-- name: LockKeyKind :one
-- Lock first, then validate in a new statement: a lock wait must not preserve the pre-wait authorization snapshot.
SELECT id FROM accounts.api_keys WHERE id = @id AND kind = @kind AND deleted_at IS NULL FOR UPDATE;

-- name: ManagedWorkspace :one
SELECT id FROM accounts.workspaces WHERE id = @id AND status = 'active' AND deleted_at IS NULL;
