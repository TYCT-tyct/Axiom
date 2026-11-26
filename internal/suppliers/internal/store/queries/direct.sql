-- A provider's own API reached with pasted keys (docs/domains/suppliers.md「直连接口」).

-- name: InsertDirectSupplier :one
-- A direct API has no balance to read: the column stays empty, and routing goes by its mode instead. Its costs are in
-- the settlement currency itself, so its native unit is money (usd) and its unit value 1. It is in service at once:
-- every key it has passed a check, and there is nothing left to read.
INSERT INTO suppliers.suppliers (owner_scope, name, kind, origin, upstream_account, native_unit, settlement_currency,
    mgmt_egress_id, status, mode, api_format)
VALUES ('platform', @name, @kind, @origin, @upstream_account, 'usd', @settlement_currency, 'direct', 'active', 'direct',
    sqlc.narg(api_format))
RETURNING id;

-- name: InsertDirectCredential :exec
INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce, wrapped_dek,
    key_version, status, max_concurrency, requests_per_minute, allowed_models, call_names, hint, settings, balance_native,
    balance_observed_at)
VALUES (@id, @supplier_id, @supplier_group_id, @upstream_key_ref, @ciphertext, @nonce, @wrapped_dek, @key_version,
    'active', @max_concurrency, sqlc.narg(requests_per_minute), @allowed_models, sqlc.narg(call_names), @hint, sqlc.narg(settings),
    sqlc.narg(balance_native)::text::numeric, sqlc.narg(balance_observed_at));

-- name: StoredKeyRefs :many
-- The pasted keys already stored as a credential that is not erased, by the reference a pasted key is stored under.
SELECT DISTINCT upstream_key_ref FROM suppliers.credentials
WHERE upstream_key_ref = ANY(@refs::text[]) AND status <> 'erased'
ORDER BY upstream_key_ref;

-- name: SetLineRatio :exec
-- A direct or cloud line's ratio, which a person sets (docs/domains/suppliers-direct.md「成本与线路倍率」).
UPDATE suppliers.supplier_groups SET ratio = @ratio WHERE id = @id AND supplier_id = @supplier_id;

-- name: ListPastedLines :many
-- The platform's direct and cloud suppliers in service, each with its one line: what the periodic re-list and
-- re-projection go over (docs/domains/suppliers-direct.md「模型重读与余额」). An account's own key is never one of them.
SELECT s.id, s.kind, s.mode, s.origin, s.settlement_currency, s.api_format, g.id AS group_id, g.ratio::text AS ratio
FROM suppliers.suppliers s
JOIN suppliers.supplier_groups g ON g.supplier_id = s.id
WHERE s.owner_scope = 'platform' AND s.mode IN ('direct', 'cloud') AND s.status = 'active'
ORDER BY s.id;

-- name: ListLineKeys :many
-- The keys of a direct supplier that are in service and hold a key, with their settings.
SELECT id, settings FROM suppliers.credentials
WHERE supplier_id = @supplier_id AND status = 'active' AND ciphertext IS NOT NULL
ORDER BY id;

-- name: SetKeyModels :exec
-- What a re-listed key serves: the names its listing gave, and the line's endpoints it lists by another name.
UPDATE suppliers.credentials SET allowed_models = @allowed_models, call_names = sqlc.narg(call_names)
WHERE id = @id AND supplier_id = @supplier_id;

-- name: SetKeyBalance :exec
UPDATE suppliers.credentials SET balance_native = @balance_native::numeric, balance_observed_at = @observed_at
WHERE id = @id AND supplier_id = @supplier_id;

-- name: LineNames :many
-- Every name the line lists, and the model the catalog has mapped it to; none for one waiting for a mapping.
SELECT upstream_name, model_id FROM suppliers.supplier_models
WHERE supplier_group_id = @supplier_group_id AND present
ORDER BY upstream_name;

-- name: LineEndpointsOf :many
-- Every endpoint of a line, to be priced at the line's ratio.
SELECT id, model_id, upstream_name FROM suppliers.endpoints WHERE supplier_group_id = @supplier_group_id ORDER BY id;
