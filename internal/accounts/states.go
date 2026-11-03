package accounts

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// SetAccountSuspended pauses calls without preventing the owner from signing in.
func (*Service) SetAccountSuspended(ctx context.Context, tx pg.Tx, actor audit.Actor, id AccountID, suspended bool, reason string) error {
	var site uuid.UUID
	var before string
	if err := tx.QueryRow(ctx, `SELECT site_id,status FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`, uuid.UUID(id)).Scan(&site, &before); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock account status: %w", err)
	}
	after := "active"
	if suspended {
		after = "disabled"
	}
	if before == after {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.accounts SET status=$2 WHERE id=$1`, uuid.UUID(id), after); err != nil {
		return fmt.Errorf("set account status: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "account.status_change", Reason: reason, Target: audit.Target{Module: "accounts", Kind: "account", ID: id.TypeID(), Site: site, Account: uuid.UUID(id)}, Detail: map[string]any{"before": before, "after": after}}); err != nil {
		return err
	}
	sev := "info"
	if suspended {
		sev = "critical"
	}
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{
		Kind: "accounts.account_suspended", Key: id.TypeID(), Severity: sev, Resolved: !suspended,
		Fields: map[string]string{"site": site.String()},
	})
}

// SetAccountTier moves an account to another tier of its site. The account row is locked, so two changes to one account
// queue; the same tier changes nothing and writes no audit entry, and a tier of another site is a *SettingError.
func (*Service) SetAccountTier(ctx context.Context, tx pg.Tx, actor audit.Actor, id AccountID, tier TierID, reason string) error {
	// Lock the account on its own before reading the tier names. Locking through a join can pair the account row a
	// waiter rechecks after another transaction commits with the tier row it read before waiting; the join then finds
	// no row and an account that exists looks missing.
	var site, beforeID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT site_id,tier_id FROM accounts.accounts WHERE id=$1 FOR NO KEY UPDATE`,
		uuid.UUID(id)).Scan(&site, &beforeID); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock account tier: %w", err)
	}
	if TierID(beforeID) == tier {
		return nil
	}
	var after string
	if err := tx.QueryRow(ctx, `SELECT name FROM accounts.tiers WHERE site_id=$1 AND id=$2`,
		site, uuid.UUID(tier)).Scan(&after); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return &SettingError{"tier_id", "must be one of the account's site's tiers"}
		}
		return fmt.Errorf("read the tier: %w", err)
	}
	var before string
	if err := tx.QueryRow(ctx, `SELECT name FROM accounts.tiers WHERE site_id=$1 AND id=$2`,
		site, beforeID).Scan(&before); err != nil {
		return fmt.Errorf("read the current tier: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.accounts SET tier_id=$2 WHERE id=$1`, uuid.UUID(id), uuid.UUID(tier)); err != nil {
		return fmt.Errorf("set account tier: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "account.tier_change", Reason: reason, Target: audit.Target{Module: "accounts", Kind: "account", ID: id.TypeID(), Site: site, Account: uuid.UUID(id)}, Detail: map[string]any{"before": before, "after": after}})
}

// SetUserSignInDisabled revokes all sessions under the same user lock as session issuance.
// Restoring sign-in preserves factors and never restores a revoked session.
func (*Service) SetUserSignInDisabled(ctx context.Context, tx pg.Tx, actor audit.Actor, id UserID, disabled bool, reason string) error {
	var site, account uuid.UUID
	var before string
	if err := tx.QueryRow(ctx, `SELECT site_id,account_id,status FROM accounts.users WHERE id=$1 FOR UPDATE`, uuid.UUID(id)).Scan(&site, &account, &before); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock user status: %w", err)
	}
	after := "active"
	if disabled {
		after = "disabled"
	}
	if before == after {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts.users SET status=$2 WHERE id=$1`, uuid.UUID(id), after); err != nil {
		return fmt.Errorf("set user status: %w", err)
	}
	if disabled {
		if _, err := tx.Exec(ctx, `UPDATE accounts.sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, uuid.UUID(id)); err != nil {
			return fmt.Errorf("revoke disabled user's sessions: %w", err)
		}
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "user.sign_in_change", Reason: reason, Target: userTarget(site, account, uuid.UUID(id)), Detail: map[string]any{"before": before, "after": after}})
}

// PlatformGrant is the most recent grant of a role, including its revocation state.
type PlatformGrant struct {
	Role    string `json:"role"`
	Revoked bool   `json:"revoked"`
}

// UserPlatformGrants reads the latest grant of each role; an unknown user is ErrNotFound.
func (*Service) UserPlatformGrants(ctx context.Context, tx pg.Tx, id UserID) ([]PlatformGrant, error) {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM accounts.users WHERE id=$1)`, uuid.UUID(id)).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON(r.name) r.name,g.revoked_at IS NOT NULL FROM accounts.platform_grants g JOIN accounts.roles r ON r.id=g.role_id WHERE g.user_id=$1 ORDER BY r.name,(g.revoked_at IS NULL) DESC,g.id DESC`, uuid.UUID(id))
	if err != nil {
		return nil, fmt.Errorf("read platform grants: %w", err)
	}
	defer rows.Close()
	grants := []PlatformGrant{}
	for rows.Next() {
		var g PlatformGrant
		if err := rows.Scan(&g.Role, &g.Revoked); err != nil {
			return nil, err
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// SetPlatformGrantRevoked revokes a held grant, or restores a historical one by inserting
// a new row. Restoration cannot grant permissions that the operator does not hold.
func (*Service) SetPlatformGrantRevoked(ctx context.Context, tx pg.Tx, p Principal, id UserID, role string, revoked bool, reason string) error {
	var site, account uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT site_id,account_id FROM accounts.users WHERE id=$1 FOR UPDATE`, uuid.UUID(id)).Scan(&site, &account); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock grant user: %w", err)
	}
	var grant, roleID uuid.UUID
	var before bool
	if err := tx.QueryRow(ctx, `SELECT g.id,g.role_id,g.revoked_at IS NOT NULL FROM accounts.platform_grants g JOIN accounts.roles r ON r.id=g.role_id WHERE g.user_id=$1 AND r.name=$2 ORDER BY (g.revoked_at IS NULL) DESC,g.id DESC LIMIT 1`, uuid.UUID(id), role).Scan(&grant, &roleID, &before); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read grant: %w", err)
	}
	if before == revoked {
		return nil
	}
	if revoked {
		if _, err := tx.Exec(ctx, `UPDATE accounts.platform_grants SET revoked_at=now() WHERE id=$1`, grant); err != nil {
			return fmt.Errorf("revoke platform grant: %w", err)
		}
	} else {
		var subset bool
		if err := tx.QueryRow(ctx, `SELECT (r.permissions || r.additional_permissions) <@ ARRAY(
            SELECT DISTINCT unnest(held.permissions || held.additional_permissions)
            FROM accounts.platform_grants g JOIN accounts.roles held ON held.id=g.role_id
            WHERE g.user_id=$1 AND g.revoked_at IS NULL)
            FROM accounts.roles r WHERE r.id=$2`, uuid.UUID(p.user), roleID).Scan(&subset); err != nil {
			return fmt.Errorf("check restored grant permissions: %w", err)
		}
		if !subset {
			return ErrForbidden
		}
		inserted, err := tx.Exec(ctx, `INSERT INTO accounts.platform_grants(id,site_id,account_id,user_id,role_id,granted_by) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(user_id,role_id) WHERE revoked_at IS NULL DO NOTHING`, uuid.NewV7(), site, account, uuid.UUID(id), roleID, uuid.UUID(p.user))
		if err != nil {
			return fmt.Errorf("restore platform grant: %w", err)
		}
		if inserted.RowsAffected() == 0 {
			return nil
		}
	}
	return audit.Record(ctx, tx, audit.Event{Actor: audit.User(uuid.UUID(p.user)), Action: "platform_grant.status_change", Reason: reason, Target: userTarget(site, account, uuid.UUID(id)), Detail: map[string]any{"role": role, "before_revoked": before, "after_revoked": revoked}})
}
