package accounts

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// The funding orders a call may use (docs/domains/billing.md「资金来源」). The first is the code default and the
// site default until an administrator chooses another.
const (
	BillingSubscriptionFirst = "subscription_first"
	BillingWalletFirst       = "wallet_first"
	BillingSubscriptionOnly  = "subscription_only"
	BillingWalletOnly        = "wallet_only"
)

// ErrBillingPreference is an unknown funding order.
var ErrBillingPreference = errors.New("accounts: unknown billing preference")

// ErrBillingPreferenceRollout means a non-default order cannot be selected while a node of the previous release,
// which does not read it, is still live.
var ErrBillingPreferenceRollout = errors.New("accounts: billing preference waits for every gateway node to support it")

// ValidBillingPreference reports whether p is one of the four orders.
func ValidBillingPreference(p string) bool {
	switch p {
	case BillingSubscriptionFirst, BillingWalletFirst, BillingSubscriptionOnly, BillingWalletOnly:
		return true
	}
	return false
}

// BillingPreference is the order in force for an account and whether the account chose it itself. Own is false when
// the site default, or the code default, applies.
type BillingPreference struct {
	Preference  string
	Own         bool
	SiteDefault string
}

// GetBillingPreference reads the order in the account's scope.
func GetBillingPreference(ctx context.Context, tx pg.Tx, account AccountID) (BillingPreference, error) {
	row, err := New().BillingPreference(ctx, tx, uuid.UUID(account))
	if err != nil {
		return BillingPreference{}, err
	}
	return BillingPreference(row), nil
}

// SetBillingPreference writes the account's own order; nil clears it so the site default applies again.
func SetBillingPreference(ctx context.Context, tx pg.Tx, account AccountID, preference *string) error {
	if preference != nil && !ValidBillingPreference(*preference) {
		return ErrBillingPreference
	}
	return New().SetBillingPreference(ctx, tx, SetBillingPreferenceParams{AccountID: uuid.UUID(account), Preference: preference})
}

// BillingPreference reads the order in force for the signed-in user's account.
func (s *Service) BillingPreference(ctx context.Context, p Principal) (out BillingPreference, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return out, ErrNoSession
	}
	err = s.db.ReadTx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		out, err = GetBillingPreference(ctx, tx, p.account)
		return err
	})
	return out, err
}

// UpdateBillingPreference sets the account's own order (nil clears it) and returns the order in force. The account
// comes from the principal, never from the request.
func (s *Service) UpdateBillingPreference(ctx context.Context, p Principal, preference *string) (out BillingPreference, err error) {
	if p.user == (UserID{}) || p.Pending() || p.surface != Console {
		return out, ErrNoSession
	}
	if preference != nil && !ValidBillingPreference(*preference) {
		return out, ErrBillingPreference
	}
	err = s.db.Tx(ctx, pg.AccountScope(uuid.UUID(p.account)), func(tx pg.Tx) error {
		if err := requireBillingPreferenceReady(ctx, tx, preference); err != nil {
			return err
		}
		if err := SetBillingPreference(ctx, tx, p.account, preference); err != nil {
			return err
		}
		if out, err = GetBillingPreference(ctx, tx, p.account); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: audit.User(uuid.UUID(p.user)), Action: "user.billing_preference",
			Target: userTarget(uuid.UUID(p.site), uuid.UUID(p.account), uuid.UUID(p.user)),
		})
	})
	return out, err
}

// requireBillingPreferenceReady refuses a non-default choice while a node of the previous release is live; clearing,
// or choosing the default, is always safe.
func requireBillingPreferenceReady(ctx context.Context, tx pg.Tx, preference *string) error {
	if preference == nil || *preference == BillingSubscriptionFirst {
		return nil
	}
	ready, err := New().BillingPreferenceReady(ctx, tx)
	if err != nil {
		return err
	}
	if !ready {
		return ErrBillingPreferenceRollout
	}
	return nil
}
