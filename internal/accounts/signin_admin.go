package accounts

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrLoginDomainLocked is a login domain change a site's sign-in assets refuse: passkeys bind to the domain, provider
// applications register its callback, and linked identities were made under it.
var ErrLoginDomainLocked = errors.New("accounts: the site already has sign-in assets on this login domain")

// ErrLoginDomainRequired is passkeys turned on without a login domain, or a domain cleared while passkeys are on.
var ErrLoginDomainRequired = errors.New("accounts: passkeys need a login domain")

// ErrPasskeysInUse is turning passkeys off while someone of the site holds one. The credential would remain on the
// second factor path the sign-in page can no longer offer, so the users must remove their passkeys first.
var ErrPasskeysInUse = errors.New("accounts: users of this site still hold passkeys")

// OAuthProvider is one provider's site-level application as the admin page sees it. The secret is never part of it.
type OAuthProvider struct {
	Provider   string
	ClientID   string
	Configured bool
}

// SiteSignIn is a site's sign-in configuration.
type SiteSignIn struct {
	LoginDomain string
	Passkeys    bool
	Providers   []OAuthProvider
}

// SiteSignInUpdate changes a site's sign-in switches. A nil LoginDomain leaves it as it is; a pointer to "" clears it.
type SiteSignInUpdate struct {
	LoginDomain *string
	Passkeys    bool
}

// SiteSignIn reads a site's login domain, passkey switch and configured providers.
func (s *Service) SiteSignIn(ctx context.Context, tx pg.Tx, site SiteID) (SiteSignIn, error) {
	row, err := s.queries.ReadSiteSignIn(ctx, tx, uuid.UUID(site))
	if errors.Is(err, pg.ErrNoRows) {
		return SiteSignIn{}, ErrNotFound
	}
	if err != nil {
		return SiteSignIn{}, fmt.Errorf("read the site's sign-in settings: %w", err)
	}
	apps, err := s.queries.ListSiteOAuthApps(ctx, tx, uuid.UUID(site))
	if err != nil {
		return SiteSignIn{}, fmt.Errorf("list the site's sign-in providers: %w", err)
	}
	configured := make(map[string]string, len(apps))
	for _, app := range apps {
		configured[app.Provider] = app.ClientID
	}
	out := SiteSignIn{Passkeys: row.PasskeysEnabled}
	if row.LoginDomain != nil {
		out.LoginDomain = *row.LoginDomain
	}
	for _, name := range ProviderNames {
		id, ok := configured[name]
		out.Providers = append(out.Providers, OAuthProvider{Provider: name, ClientID: id, Configured: ok})
	}
	return out, nil
}

// SetSiteSignIn sets the login domain and the passkey switch. A domain that changes after sign-in assets exist is
// refused, and the domain must be one of the site's enabled console domains.
func (s *Service) SetSiteSignIn(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, update SiteSignInUpdate, reason string) error {
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return ErrNotFound
	}
	before, err := s.SiteSignIn(ctx, tx, site)
	if err != nil {
		return err
	}
	domain := before.LoginDomain
	if update.LoginDomain != nil {
		domain = strings.ToLower(strings.TrimSpace(*update.LoginDomain))
	}
	if domain != "" {
		domains, err := s.queries.ListSiteConsoleDomains(ctx, tx, uuid.UUID(site))
		if err != nil {
			return fmt.Errorf("read the site's console domains: %w", err)
		}
		if !slices.ContainsFunc(domains, func(d ListSiteConsoleDomainsRow) bool { return d.Domain == domain }) {
			return &SettingError{Field: "login_domain", Reason: "must be one of the site's enabled console domains"}
		}
	}
	// Setting the first domain is not a change: a provider application may already be stored while the site has no
	// domain yet, and it only becomes reachable once one is chosen. Moving a domain that exists is what would strand
	// passkeys, callbacks and linked identities.
	if before.LoginDomain != "" && domain != before.LoginDomain {
		assets, err := s.queries.CountSiteSignInAssets(ctx, tx, uuid.UUID(site))
		if err != nil {
			return fmt.Errorf("count the site's sign-in assets: %w", err)
		}
		if assets.Passkeys > 0 || assets.Identities > 0 || assets.Providers > 0 {
			return ErrLoginDomainLocked
		}
	}
	if !update.Passkeys && before.Passkeys {
		assets, err := s.queries.CountSiteSignInAssets(ctx, tx, uuid.UUID(site))
		if err != nil {
			return fmt.Errorf("count the site's sign-in assets: %w", err)
		}
		if assets.Passkeys > 0 {
			return ErrPasskeysInUse
		}
	}
	if update.Passkeys && domain == "" {
		return ErrLoginDomainRequired
	}
	var value *string
	if domain != "" {
		value = &domain
	}
	if _, err := s.queries.SetLoginDomain(ctx, tx, SetLoginDomainParams{Domain: value, SiteID: uuid.UUID(site)}); err != nil {
		return fmt.Errorf("write the site's login domain: %w", err)
	}
	if err := s.queries.WriteSitePasskeys(ctx, tx, WriteSitePasskeysParams{SiteID: uuid.UUID(site), Enabled: &update.Passkeys}); err != nil {
		return fmt.Errorf("write the site's passkey switch: %w", err)
	}
	after := SiteSignIn{LoginDomain: domain, Passkeys: update.Passkeys}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_sign_in.update", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{
			"before": map[string]any{"login_domain": before.LoginDomain, "passkeys": before.Passkeys},
			"after":  map[string]any{"login_domain": after.LoginDomain, "passkeys": after.Passkeys},
		},
	})
}

// SetSiteOAuthProvider stores a site's application for one provider, sealed like any other secret. The caller passes
// the secret in the clear only here; it is never read back through this API.
func (s *Service) SetSiteOAuthProvider(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, provider, clientID, clientSecret, reason string) error {
	if !slices.Contains(ProviderNames, provider) {
		return ErrNotFound
	}
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || len(clientID) > 200 {
		return &SettingError{Field: "client_id", Reason: "must be between 1 and 200 characters"}
	}
	if clientSecret == "" || len(clientSecret) > 500 {
		return &SettingError{Field: "client_secret", Reason: "must be between 1 and 500 characters"}
	}
	locked, err := s.queries.LockSite(ctx, tx, uuid.UUID(site))
	if err != nil {
		return fmt.Errorf("lock the site: %w", err)
	}
	if len(locked) == 0 {
		return ErrNotFound
	}
	id := uuid.NewV7()
	existing, err := s.queries.ReadSiteOAuthProvider(ctx, tx, ReadSiteOAuthProviderParams{SiteID: uuid.UUID(site), Provider: provider})
	switch {
	case err == nil:
		id = existing.ID
	case !errors.Is(err, pg.ErrNoRows):
		return fmt.Errorf("read the provider application: %w", err)
	}
	sealed := s.keyring.Seal(secret.New(clientSecret), oauthSecretLocation(id))
	version := int32(sealed.KeyVersion)
	if err := s.queries.UpsertSiteOAuthProvider(ctx, tx, UpsertSiteOAuthProviderParams{
		ID: id, SiteID: uuid.UUID(site), Provider: provider, ClientID: clientID,
		Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: version,
	}); err != nil {
		return fmt.Errorf("write the provider application: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_oauth_provider.set", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"provider": provider, "client_id": clientID},
	})
}

// DeleteSiteOAuthProvider turns a provider off for a site. Linked identities stay: turning the application back on
// makes them usable again.
func (s *Service) DeleteSiteOAuthProvider(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, provider, reason string) error {
	if !slices.Contains(ProviderNames, provider) {
		return ErrNotFound
	}
	removed, err := s.queries.DeleteSiteOAuthProvider(ctx, tx, DeleteSiteOAuthProviderParams{SiteID: uuid.UUID(site), Provider: provider})
	if err != nil {
		return fmt.Errorf("delete the provider application: %w", err)
	}
	if removed != 1 {
		return ErrNotFound
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "site_oauth_provider.delete", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "site", ID: site.TypeID(), Site: uuid.UUID(site)},
		Detail: map[string]any{"provider": provider},
	})
}
