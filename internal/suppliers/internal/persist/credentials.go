package persist

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// projectKeyPolicies keeps policy changes atomic with the completed listing, without changing the legacy state JSON.
func projectKeyPolicies(ctx context.Context, tx pg.Tx, id uuid.UUID, keys *[]adapter.KeyState) error {
	if keys == nil {
		return nil
	}
	type policyRow struct {
		Ref       string     `json:"ref"`
		Group     string     `json:"group_ref"`
		Models    []string   `json:"models,omitzero"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	rows := make([]policyRow, 0, len(*keys))
	for _, k := range *keys {
		policy := k.Policy
		if policy == nil {
			policy = &adapter.KeyPolicy{Models: []string{}} // A listing without policy data cannot prove permission.
		}
		rows = append(rows, policyRow{Ref: string(k.Ref), Group: k.GroupRef, Models: policy.Models, ExpiresAt: policy.ExpiresAt})
	}
	raw, err := jsonx.MarshalCanonical(rows)
	if err != nil {
		return fmt.Errorf("encode credential policies: %w", err)
	}
	if err := store.ProjectCredentialPolicies(ctx, tx, id, raw); err != nil {
		return fmt.Errorf("project credential policies: %w", err)
	}
	return nil
}
