package accounts_test

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/typeid"
)

// TestIDsReadOnlyTheirOwnKind: each kind of ID is written behind its own prefix and read back, and an ID of any
// other kind is refused, so a workspace ID given where an account ID belongs fails instead of naming nothing.
func TestIDsReadOnlyTheirOwnKind(t *testing.T) {
	t.Parallel()
	id := uuid.NewV7()
	// Each parser returns a plain UUID, so the kinds fit in one table.
	kinds := []struct {
		prefix, written string
		parse           func(string) (uuid.UUID, error)
	}{
		{"site_", accounts.SiteID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseSiteID(s)
			return uuid.UUID(v), err
		}},
		{"tier_", accounts.TierID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseTierID(s)
			return uuid.UUID(v), err
		}},
		{"acct_", accounts.AccountID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseAccountID(s)
			return uuid.UUID(v), err
		}},
		{"ws_", accounts.WorkspaceID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseWorkspaceID(s)
			return uuid.UUID(v), err
		}},
		{"user_", accounts.UserID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseUserID(s)
			return uuid.UUID(v), err
		}},
		{"key_", accounts.KeyID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseKeyID(s)
			return uuid.UUID(v), err
		}},
		{"sess_", accounts.SessionID(id).TypeID(), func(s string) (uuid.UUID, error) {
			v, err := accounts.ParseSessionID(s)
			return uuid.UUID(v), err
		}},
	}
	for i, kind := range kinds {
		if !strings.HasPrefix(kind.written, kind.prefix) {
			t.Errorf("%s is not written behind %s", kind.written, kind.prefix)
		}
		for j, other := range kinds {
			got, err := kind.parse(other.written)
			if i == j && (err != nil || got != id) || i != j && !errors.Is(err, typeid.ErrInvalid) {
				t.Errorf("the %s parser read %s as %v, %v", kind.prefix, other.written, got, err)
			}
		}
	}
}
