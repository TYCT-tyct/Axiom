package suppliers

import (
	"fmt"
	"uuid"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/store"
)

// A snapshot's credentials: what Reload makes of each credential row, its key opened and its settings checked.

// upstreamAccount is the account at the supplier a credential's requests count against: a relay's keys all draw on the
// one account Axiom logs in as, and every key pasted for a provider's own API, or a cloud region, is an account of its
// own (docs/domains/suppliers.md「凭证」).
func upstreamAccount(r store.ListRoutableRow) uuid.UUID {
	if r.Mode == relayMode {
		return r.SupplierID
	}
	return r.CredentialID
}

// credentialOf is the row's credential; wallet says it is a key whose own balance is its funds.
func (s *Service) credentialOf(r store.ListRoutableRow, wallet bool) (*credential, error) {
	if r.KeyVersion == nil {
		return nil, fmt.Errorf("credential %s is active without a key", r.CredentialID)
	}
	at := secret.Location{Table: "suppliers.credentials", RowID: r.CredentialID.String(), Purpose: "key"}
	key, err := s.cfg.Keys.Open(secret.Sealed{
		Ciphertext: r.Ciphertext, Nonce: r.Nonce, WrappedDEK: r.WrappedDek,
		KeyVersion: uint32(*r.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
	}, at)
	if err != nil {
		return nil, fmt.Errorf("credential %s: its key does not open: %w", r.CredentialID, err)
	}
	settings, err := s.settingsOf(r)
	if err != nil {
		return nil, err
	}
	c := &credential{
		id: r.CredentialID, key: key, region: deref(r.Region), call: r.CallName, settings: settings,
		max: r.MaxConcurrency, perMinute: ceiling(r.RequestsPerMinute),
		configuredMax: r.MaxConcurrency, configuredMinute: ceiling(r.RequestsPerMinute),
		expires: r.ExpiresAt, rt: s.runtimeOf(r.CredentialID, upstreamAccount(r), r.MaxConcurrency, ceiling(r.RequestsPerMinute)),
	}
	if wallet {
		if c.funds, err = walletFunds(r); err != nil {
			return nil, err
		}
		c.recover(r.KeyBalanceObservedAt)
	}
	return c, nil
}

// settingsOf is a credential's settings, which must be ones its type could have written: a call never goes out with a
// setting its type does not send, so a supplier with such a row is left out. A relay's type has no form, so its keys
// have none.
func (s *Service) settingsOf(r store.ListRoutableRow) (adapter.Settings, error) {
	settings := settingsOf(r.Settings)
	if r.Settings != nil && settings == nil {
		return nil, fmt.Errorf("credential %s: its settings are not an object of strings", r.CredentialID)
	}
	a, _ := s.registry.Get(adapter.Kind(r.Kind)) // endpointOf has refused a kind without an adapter
	if !registry.TypeOf(a).Valid(settings) {
		return nil, fmt.Errorf("credential %s: its settings are not ones its type %s writes", r.CredentialID, r.Kind)
	}
	return settings, nil
}
