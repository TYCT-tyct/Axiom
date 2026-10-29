package accounts

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// SystemSettings is the site's public name and its currently supported registration policy.
type SystemSettings struct {
	Name         string `json:"name"`
	Registration string `json:"registration"`
}

// ReadSystemSettings reads the public settings within the authorized site scope.
func ReadSystemSettings(ctx context.Context, tx pg.Tx, site SiteID) (SystemSettings, error) {
	var s SystemSettings
	err := tx.QueryRow(ctx, `SELECT name,registration FROM accounts.sites WHERE id=$1`, uuid.UUID(site)).Scan(&s.Name, &s.Registration)
	return s, err
}

// SetSystemSettings keeps the change and its audit in the same site-row turn.
func SetSystemSettings(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, s SystemSettings, reason string) error {
	if strings.TrimSpace(s.Name) == "" || utf8.RuneCountInString(s.Name) > 100 || strings.ContainsRune(s.Name, 0) {
		return &SettingError{"name", "must have 1 to 100 characters without NUL"}
	}
	if s.Registration != "invite" && s.Registration != "closed" {
		return &SettingError{"registration", "must be invite or closed"}
	}
	if _, err := New().LockSite(ctx, tx, uuid.UUID(site)); err != nil {
		return err
	}
	before, err := ReadSystemSettings(ctx, tx, site)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE accounts.sites SET name=$2,registration=$3 WHERE id=$1`, uuid.UUID(site), s.Name, s.Registration); err != nil {
		return fmt.Errorf("save system settings: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "site_system.update", Reason: reason, Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)}, Detail: map[string]any{"before": before, "after": s}})
}
