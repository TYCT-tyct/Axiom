-- What the admin API reads and writes of suppliers, in the platform scope (internal/suppliers/README.md「管理接口」).

-- name: ListSuppliersPage :many
-- Newest first: an ID is a UUIDv7, so its order is the order of connecting, and a page starts after the last ID of the
-- one before. The unit value is the latest confirmed for this currency and native unit. A missing balance, threshold
-- or unit value is the empty text, which no number is.
SELECT s.id, s.name, s.kind, s.origin, s.upstream_account, s.generation, s.detected_version, s.native_unit,
    s.settlement_currency, s.status, s.status_reason, coalesce(s.balance_native::text, '')::text AS balance_native, s.balance_observed_at,
    coalesce(s.low_balance_threshold::text, '')::text AS low_balance_threshold,
    coalesce((SELECT u.value::text FROM suppliers.unit_values u WHERE u.supplier_id = s.id AND u.currency = s.settlement_currency AND u.native_unit = s.native_unit ORDER BY u.id DESC LIMIT 1), '')::text AS unit_value
FROM suppliers.suppliers s
WHERE (sqlc.narg(status)::text IS NULL OR s.status = sqlc.narg(status))
    AND (sqlc.narg(after)::uuid IS NULL OR s.id < sqlc.narg(after))
ORDER BY s.id DESC
LIMIT @page_size::int;

-- name: GetSupplierView :many
SELECT s.id, s.name, s.kind, s.origin, s.upstream_account, s.generation, s.detected_version, s.native_unit,
    s.settlement_currency, s.status, s.status_reason, coalesce(s.balance_native::text, '')::text AS balance_native, s.balance_observed_at,
    coalesce(s.low_balance_threshold::text, '')::text AS low_balance_threshold,
    coalesce((SELECT u.value::text FROM suppliers.unit_values u WHERE u.supplier_id = s.id AND u.currency = s.settlement_currency AND u.native_unit = s.native_unit ORDER BY u.id DESC LIMIT 1), '')::text AS unit_value
FROM suppliers.suppliers s WHERE s.id = @id;

-- name: ListGroupsOf :many
SELECT id, upstream_ref, name, ratio::text AS ratio, facts, selected, codename, present, key_eligible, key_error,
    pool_accounts, pool_failed, pool_usable, pool_headroom, pool_capacity, COALESCE(pool_low, false) AS pool_low,
    pool_observed_at
FROM suppliers.supplier_groups g WHERE g.supplier_id = @supplier_id ORDER BY g.name, g.id;

-- name: GetGroupOf :many
SELECT id, upstream_ref, name, ratio::text AS ratio, facts, selected, codename, present, key_eligible, key_error,
    pool_accounts, pool_failed, pool_usable, pool_headroom, pool_capacity, COALESCE(pool_low, false) AS pool_low,
    pool_observed_at
FROM suppliers.supplier_groups g WHERE g.supplier_id = @supplier_id AND g.id = @id;

-- name: ListCredentialsOf :many
SELECT id, supplier_group_id, upstream_key_ref, status, cooldown_until, expires_at, max_concurrency, requests_per_minute
FROM suppliers.credentials WHERE supplier_id = @supplier_id ORDER BY id;

-- name: ListEndpointsOf :many
SELECT id, supplier_group_id, model_id, upstream_name, formats, cost_state, enabled, quantization
FROM suppliers.endpoints WHERE supplier_id = @supplier_id ORDER BY upstream_name, id;

-- name: GetEndpointOf :many
SELECT id, supplier_group_id, model_id, upstream_name, formats, cost_state, enabled, quantization
FROM suppliers.endpoints WHERE supplier_id = @supplier_id AND id = @id;

-- name: SetEndpointQuantization :exec
-- The level an admin recorded for this endpoint. The sync never writes it; NULL is "not recorded", which is not the
-- level unknown.
UPDATE suppliers.endpoints SET quantization = sqlc.narg(quantization)
WHERE supplier_id = @supplier_id AND id = @id;

-- name: GetCredentialOf :many
-- One credential of one supplier: a credential of another supplier is not this one, and is no credential of the request.
SELECT id, supplier_group_id, upstream_key_ref, status, cooldown_until, expires_at, max_concurrency, requests_per_minute
FROM suppliers.credentials WHERE supplier_id = @supplier_id AND id = @id;

-- name: SetCredentialLimits :execrows
-- What the admin set: the calls the key serves at once, and the account's rolling-minute ceiling for selecting it. The
-- count itself is each node's own, in memory (internal/suppliers/README.md「并发与速率」).
UPDATE suppliers.credentials SET max_concurrency = @max_concurrency, requests_per_minute = @requests_per_minute
WHERE supplier_id = @supplier_id AND id = @id;

-- name: LockSupplierForChange :many
-- The supplier's turn: its changes are applied one after the other, each against the state the one before left.
SELECT id, kind, status, config_gen FROM suppliers.suppliers WHERE id = @id FOR UPDATE;

-- name: SetSupplierStatusBumpingConfigGen :exec
-- A change of status raises the configuration generation, so a round of the sync that began before is discarded.
UPDATE suppliers.suppliers SET status = @status, status_reason = sqlc.narg(status_reason), config_gen = config_gen + 1
WHERE id = @id;

-- name: BumpConfigGen :exec
UPDATE suppliers.suppliers SET config_gen = config_gen + 1 WHERE id = @id;

-- name: ReplaceSupplierLogin :exec
UPDATE suppliers.supplier_logins SET kind = @kind, ciphertext = @ciphertext, nonce = @nonce, wrapped_dek = @wrapped_dek,
    key_version = @key_version, version = version + 1, expires_at = sqlc.narg(expires_at)
WHERE supplier_id = @supplier_id;

-- name: DeleteSupplierLogin :exec
-- Removing a supplier: the sealed login goes. Nothing refers to the row.
DELETE FROM suppliers.supplier_logins WHERE supplier_id = @supplier_id;

-- name: EraseCredentialsOf :exec
-- Removing a supplier: the sealed keys go, and the rows stay, because attempts that were made with them point at the row.
UPDATE suppliers.credentials SET ciphertext = NULL, nonce = NULL, wrapped_dek = NULL, key_version = NULL, status = 'erased'
WHERE supplier_id = @supplier_id;

-- name: InsertUnitValue :exec
INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES (@supplier_id, @value::numeric, @actor, @reason);

-- name: LockGroup :many
SELECT id, upstream_ref, name, ratio::text AS ratio, facts, selected, codename, present
FROM suppliers.supplier_groups WHERE id = @id AND supplier_id = @supplier_id FOR UPDATE;

-- name: SelectGroup :exec
-- The code name is made when the group is first selected and never changes after (the table's trigger holds it).
UPDATE suppliers.supplier_groups SET selected = @selected, codename = coalesce(codename, sqlc.narg(codename)) WHERE id = @id;

-- name: InsertBindingCredential :exec
-- The key's text is read from the supplier by the sync, which seals it and moves the credential on.
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, 'binding', @max_concurrency);

-- name: InsertAlias :one
INSERT INTO suppliers.supplier_aliases (supplier_id, supplier_group_id, upstream_name, model_id, formats, set_by)
VALUES (@supplier_id, sqlc.narg(supplier_group_id), @upstream_name, @model_id, @formats, 'user')
RETURNING id;

-- name: CurrentAlias :many
SELECT a.id, COALESCE(r.model_id, a.model_id) AS model_id, COALESCE(r.revision, 1)::bigint AS revision, a.formats
FROM suppliers.supplier_aliases a
LEFT JOIN LATERAL (SELECT model_id, revision FROM suppliers.supplier_alias_revisions r WHERE r.alias_id = a.id ORDER BY revision DESC LIMIT 1) r ON true
WHERE a.supplier_id = @supplier_id AND a.supplier_group_id IS NOT DISTINCT FROM sqlc.narg(supplier_group_id)::uuid
    AND a.upstream_name = @upstream_name;

-- name: InsertAliasRevision :exec
INSERT INTO suppliers.supplier_alias_revisions (alias_id, supplier_id, revision, previous_model_id, model_id, actor, reason)
VALUES (@alias_id, @supplier_id, @revision, @previous_model_id, @model_id, @actor, @reason);

-- name: UpdateAliasFormats :exec
UPDATE suppliers.supplier_aliases SET formats = @formats WHERE id = @id;

-- name: PendingMappingsPage :many
-- The upstream names the catalog could not recognise (no model) that no alias of the supplier or of the group covers,
-- oldest first, a page starting after the last row of the one before.
SELECT m.supplier_id, m.supplier_group_id, g.codename, m.upstream_name, m.formats, m.first_seen_at
FROM suppliers.supplier_models m
JOIN suppliers.supplier_groups g ON g.id = m.supplier_group_id
JOIN suppliers.suppliers s ON s.id = m.supplier_id
WHERE m.model_id IS NULL AND m.present AND g.present AND s.status <> 'removed'
    AND (sqlc.narg(supplier_id)::uuid IS NULL OR m.supplier_id = sqlc.narg(supplier_id))
    AND NOT EXISTS (
        SELECT FROM suppliers.supplier_aliases a
        WHERE a.supplier_id = m.supplier_id AND a.upstream_name = m.upstream_name
            AND (a.supplier_group_id IS NULL OR a.supplier_group_id = m.supplier_group_id))
    AND (sqlc.narg(after_seen)::timestamptz IS NULL
        OR (m.first_seen_at, m.supplier_group_id, m.upstream_name) > (sqlc.narg(after_seen), sqlc.narg(after_group)::uuid, sqlc.narg(after_name)::text))
ORDER BY m.first_seen_at, m.supplier_group_id, m.upstream_name
LIMIT @page_size::int;

-- name: TopUpBasis :many
-- What a top-up proposal is computed from: who owns the supplier, its units and the unit value now in force, the
-- empty text when there is none.
SELECT s.id, s.name, s.owner_scope, s.site_id, s.status, s.native_unit, s.settlement_currency,
    coalesce(u.id::text, '')::text AS unit_value_id, coalesce(u.value::text, '')::text AS unit_value
FROM suppliers.suppliers s
LEFT JOIN LATERAL (SELECT v.id, v.value FROM suppliers.unit_values v
    WHERE v.supplier_id = s.id AND v.currency = s.settlement_currency AND v.native_unit = s.native_unit
    ORDER BY v.id DESC LIMIT 1) u ON true
WHERE s.id = @id;
