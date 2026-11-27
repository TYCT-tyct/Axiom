-- A group's claim on making its key at the supplier (docs/domains/suppliers.md「一个分组只建一把」). Every change but
-- taking a claim compares its id, so a holder that was taken over, or whose claim was released, writes nothing.

-- name: LockGroupClaim :many
-- The group by its id or its supplier's ref, locked after its supplier, with its claim, whether it holds a credential,
-- the key Axiom made that one of them is (empty when none is), and the keys of its erased credentials.
SELECT g.id, g.upstream_ref, g.present, g.key_claim_id, g.key_claimed_at, g.key_claim_sent_at, g.key_claim_ref,
    g.key_claim_listing,
    EXISTS (SELECT FROM suppliers.credentials c WHERE c.supplier_group_id = g.id AND c.status <> 'erased')::boolean AS keyed,
    COALESCE((SELECT c.upstream_key_ref FROM suppliers.credentials c
        WHERE c.supplier_group_id = g.id AND c.status <> 'erased' AND c.source = 'made' ORDER BY c.id LIMIT 1), '')::text AS made_ref,
    COALESCE((SELECT array_agg(c.upstream_key_ref ORDER BY c.id) FROM suppliers.credentials c
        WHERE c.supplier_group_id = g.id AND c.status = 'erased'), '{}')::text[] AS erased_refs
FROM suppliers.supplier_groups g
WHERE g.supplier_id = @supplier_id AND (g.id = sqlc.narg(id) OR g.upstream_ref = sqlc.narg(upstream_ref))
FOR UPDATE OF g;

-- name: TakeKeyClaim :exec
-- A new holder, with nothing sent; a search by name a holder before it left stopped stays, to go on from.
UPDATE suppliers.supplier_groups SET key_claim_id = @claim_id, key_claimed_at = @claimed_at
WHERE id = @id AND key_claim_sent_at IS NULL;

-- name: SendKeyClaim :execrows
-- The holder is about to send the create: from now on its result is unknown until the key's id is recorded.
UPDATE suppliers.supplier_groups SET key_claim_sent_at = @sent_at, key_claim_listing = NULL
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NULL;

-- name: ParkKeyClaim :execrows
-- Where a held claim's search by name stopped.
UPDATE suppliers.supplier_groups SET key_claim_listing = @listing
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NULL;

-- name: KnowKeyClaim :execrows
-- The supplier's id of the key a sent claim made.
UPDATE suppliers.supplier_groups SET key_claim_ref = @ref
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NOT NULL
  AND (key_claim_ref IS NULL OR key_claim_ref = @ref);

-- name: DropKeyClaim :execrows
-- The claim ends: nothing was made, its key is gone, a person released it, or the key it led to is stored.
UPDATE suppliers.supplier_groups
SET key_claim_id = NULL, key_claimed_at = NULL, key_claim_sent_at = NULL, key_claim_ref = NULL, key_claim_listing = NULL
WHERE id = @id AND key_claim_id = @claim_id;

-- name: MadeCredentialByRef :many
-- The credential the supplier's key is stored as, of any status.
SELECT id, supplier_group_id, status FROM suppliers.credentials
WHERE supplier_id = @supplier_id AND upstream_key_ref = @upstream_key_ref;

-- name: SentKeyClaimsOf :many
-- The supplier's groups whose create was sent and whose key was never stored: the id of the key when it is known.
SELECT upstream_ref, key_claim_ref FROM suppliers.supplier_groups
WHERE supplier_id = @supplier_id AND key_claim_sent_at IS NOT NULL ORDER BY upstream_ref;
