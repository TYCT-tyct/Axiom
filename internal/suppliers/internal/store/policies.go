package store

import (
	"context"
	"uuid"
)

// sqlc cannot resolve the columns declared by jsonb_to_recordset.
const projectCredentialPolicies = `WITH policies AS (
    SELECT * FROM jsonb_to_recordset($2::jsonb) AS p(ref text, group_ref text, models text[], expires_at timestamptz)
), desired AS (
    SELECT c.id, CASE WHEN (p.group_ref = '' OR p.group_ref = g.upstream_ref) THEN p.models ELSE ARRAY[]::text[] END AS models, p.expires_at
    FROM suppliers.credentials c
    JOIN suppliers.supplier_groups g ON g.id = c.supplier_group_id
    LEFT JOIN policies p ON p.ref = c.upstream_key_ref
    WHERE c.supplier_id = $1 AND c.status <> 'erased'
)
UPDATE suppliers.credentials c SET allowed_models = d.models, expires_at = d.expires_at
FROM desired d WHERE c.id = d.id
    AND (c.allowed_models IS DISTINCT FROM d.models OR c.expires_at IS DISTINCT FROM d.expires_at);`

// ProjectCredentialPolicies applies a completed upstream listing in the same fenced transaction as its state.
func ProjectCredentialPolicies(ctx context.Context, db DBTX, supplier uuid.UUID, policies []byte) error {
	_, err := db.Exec(ctx, projectCredentialPolicies, supplier, policies)
	return err
}
