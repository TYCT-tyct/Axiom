-- A group's claim on making its key at the supplier (docs/domains/suppliers.md「一个分组只建一把」). Every change but
-- taking a claim compares its id, so a holder that was taken over, or whose claim was released, writes nothing.

-- name: LockGroupClaim :many
-- The group by its id or its supplier's ref, locked after its supplier, with its claim, whether it holds a credential,
-- and the key Axiom made that one of them is (empty when none is).
SELECT g.id, g.upstream_ref, g.present, g.key_claim_id, g.key_claimed_at, g.key_claim_sent_at, g.key_claim_ref,
    EXISTS (SELECT FROM suppliers.credentials c WHERE c.supplier_group_id = g.id AND c.status <> 'erased')::boolean AS keyed,
    COALESCE((SELECT c.upstream_key_ref FROM suppliers.credentials c
        WHERE c.supplier_group_id = g.id AND c.status <> 'erased' AND c.source = 'made' ORDER BY c.id LIMIT 1), '')::text AS made_ref
FROM suppliers.supplier_groups g
WHERE g.supplier_id = @supplier_id AND (g.id = sqlc.narg(id) OR g.upstream_ref = sqlc.narg(upstream_ref))
FOR UPDATE OF g;

-- name: TakeKeyClaim :exec
-- A new holder: nothing sent yet, unless it takes over a create the supplier replays (sent_at is the first one's).
UPDATE suppliers.supplier_groups
SET key_claim_id = @claim_id, key_claimed_at = @claimed_at, key_claim_sent_at = sqlc.narg(sent_at), key_claim_ref = NULL
WHERE id = @id;

-- name: ResendKeyClaim :execrows
-- The holder of a sent claim whose result is unknown, taking it over to send the create again.
UPDATE suppliers.supplier_groups SET key_claim_id = @new_id, key_claimed_at = @claimed_at
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NOT NULL AND key_claim_ref IS NULL;

-- name: SendKeyClaim :execrows
-- The holder is about to send the create: from now on its result is unknown until the key's id is recorded.
UPDATE suppliers.supplier_groups SET key_claim_sent_at = @sent_at
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NULL;

-- name: KnowKeyClaim :execrows
-- The supplier's id of the key a sent claim made.
UPDATE suppliers.supplier_groups SET key_claim_ref = @ref
WHERE id = @id AND key_claim_id = @claim_id AND key_claim_sent_at IS NOT NULL
  AND (key_claim_ref IS NULL OR key_claim_ref = @ref);

-- name: DropKeyClaim :execrows
UPDATE suppliers.supplier_groups
SET key_claim_id = NULL, key_claimed_at = NULL, key_claim_sent_at = NULL, key_claim_ref = NULL
WHERE id = @id AND key_claim_id = @claim_id;

-- name: SettleKeyClaim :exec
-- The group's key is stored: its claim is done, unless it names another key the supplier made.
UPDATE suppliers.supplier_groups
SET key_claim_id = NULL, key_claimed_at = NULL, key_claim_sent_at = NULL, key_claim_ref = NULL
WHERE id = @id AND key_claim_id IS NOT NULL AND (key_claim_ref IS NULL OR key_claim_ref = @ref);

-- name: MadeCredentialByRef :many
-- The credential the supplier's key is stored as, of any status.
SELECT id, supplier_group_id, status FROM suppliers.credentials
WHERE supplier_id = @supplier_id AND upstream_key_ref = @upstream_key_ref;
