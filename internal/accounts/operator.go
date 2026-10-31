package accounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

var (
	// ErrSiteExists is a main site made twice: the platform has one.
	ErrSiteExists = errors.New("accounts: the main site already exists")
	// ErrNoSite is a command that needs the main site before `axiom site init` has made it.
	ErrNoSite = errors.New("accounts: there is no main site yet; run axiom site init")
)

// operator is who the command line acts as in the audit log.
var operator = audit.System("accounts.cli")

// Operator is what the command line does to sites and users, in the platform scope cmd/axiom builds (README.md
// 「第一个用户」). Its commands are the only way to make the first site and user, to grant platform roles and, in M1,
// to set a password or reset two-step verification.
type Operator struct {
	db       *pg.DB
	platform pg.Scope
	opened   OpenedFunc
	queries  *Queries
}

// NewOperator is the command line's access to the accounts of db. opened runs for every account it opens, as
// Config.OnAccountOpened does for registrations.
func NewOperator(db *pg.DB, platformScope pg.Scope, opened OpenedFunc) *Operator {
	return &Operator{db: db, platform: platformScope, opened: opened, queries: New()}
}

// mainCurrency is the accounting currency of the main site; only a secondary site chooses its own, and the column
// cannot change once the site exists.
const mainCurrency = "USD"

// SiteSpec is the main site: its name, its accounting currency and the host each surface is served on.
type SiteSpec struct {
	Name      string
	Currency  platform.Currency
	KeyPrefix string
	// Hosts are the Host headers of the surfaces, as requests carry them: lower case, with the port when it is not the
	// scheme's default. The console and the API are required, the admin is optional.
	Hosts map[Surface]string
}

// hostPattern is a host name or localhost, with an optional port.
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$`)

// origin is the Origin a state-changing request to host must carry: https on the domain itself, plain http for
// localhost in development, as the table's check allows no other.
func origin(host string) string {
	if h, _, _ := strings.Cut(host, ":"); h == "localhost" {
		return "http://" + host
	}
	return "https://" + host
}

func (spec SiteSpec) check() error {
	if n := len([]rune(spec.Name)); n < 1 || n > 100 {
		return errors.New("the site's name must be 1 to 100 characters")
	}
	if spec.Currency.String() != mainCurrency {
		return fmt.Errorf("the main site's currency is %q, want %s: docs/domains/billing.md 「钱的表示」", spec.Currency, mainCurrency)
	}
	if !sitePrefixPattern.MatchString(spec.KeyPrefix) {
		return fmt.Errorf("key prefix %q: want up to 15 lower-case letters, digits and hyphens, then a hyphen", spec.KeyPrefix)
	}
	seen := map[string]Surface{}
	for _, surface := range []Surface{Console, API, Admin} {
		host := spec.Hosts[surface]
		switch {
		case host == "" && surface == Admin:
			continue
		case host == "":
			return fmt.Errorf("the %s host is required", surface)
		case !hostPattern.MatchString(host):
			return fmt.Errorf("the %s host %q: want a lower-case host name, with a port if it is not the default", surface, host)
		case seen[host] != "":
			return fmt.Errorf("the %s and %s hosts are the same", seen[host], surface)
		}
		seen[host] = surface
	}
	return nil
}

// InitSite makes the main site in one transaction: the site, its first tier (the default of new accounts) and the
// domains of its surfaces. It fails with ErrSiteExists when the platform has one.
func (o *Operator) InitSite(ctx context.Context, spec SiteSpec) (SiteID, error) {
	if err := spec.check(); err != nil {
		return SiteID{}, err
	}
	site, tier := uuid.NewV7(), uuid.NewV7()
	err := o.db.Tx(ctx, o.platform, func(tx pg.Tx) error {
		if err := o.insertSite(ctx, tx, spec, site, tier); err != nil {
			return err
		}
		if err := audit.RegisterSite(ctx, tx, site); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{Actor: operator, Action: "site.create", Target: audit.Target{
			Module: "accounts", Kind: "site", ID: SiteID(site).TypeID(), Site: site,
		}, Detail: map[string]any{"name": spec.Name, "currency": spec.Currency.String()}})
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "one_main_site" {
		return SiteID{}, ErrSiteExists
	}
	if err != nil {
		return SiteID{}, fmt.Errorf("make the main site: %w", err)
	}
	return SiteID(site), nil
}

// insertSite writes the site, its first tier, which the site's default tier must be, and the domains of its surfaces.
func (o *Operator) insertSite(ctx context.Context, tx pg.Tx, spec SiteSpec, site, tier uuid.UUID) error {
	if err := o.queries.InsertMainSite(ctx, tx, InsertMainSiteParams{
		ID: site, Name: spec.Name, KeyPrefix: spec.KeyPrefix, DefaultTierID: tier, Currency: spec.Currency.String(),
	}); err != nil {
		return err
	}
	if err := o.queries.InsertTier(ctx, tx, InsertTierParams{ID: tier, SiteID: site, Name: "Default"}); err != nil {
		return err
	}
	for _, surface := range []Surface{Console, API, Admin} {
		host := spec.Hosts[surface]
		if host == "" {
			continue
		}
		if err := o.queries.InsertDomain(ctx, tx, InsertDomainParams{Domain: host, SiteID: site, Surface: string(surface), Origin: origin(host)}); err != nil {
			return err
		}
	}
	// The console domain is the login domain from the start: passkeys bind to it, and providers redirect back to it,
	// so choosing it later would mean remembering which credentials predate the choice.
	domain := spec.Hosts[Console]
	if _, err := o.queries.SetLoginDomain(ctx, tx, SetLoginDomainParams{Domain: &domain, SiteID: site}); err != nil {
		return err
	}
	return nil
}

// mainSite is the main site's ID and the tier new accounts start in.
func (o *Operator) mainSite(ctx context.Context) (MainSiteRow, error) {
	var rows []MainSiteRow
	if err := o.db.ReadTx(ctx, o.platform, func(tx pg.Tx) (err error) {
		rows, err = o.queries.MainSite(ctx, tx)
		return err
	}); err != nil {
		return MainSiteRow{}, fmt.Errorf("find the main site: %w", err)
	}
	if len(rows) == 0 {
		return MainSiteRow{}, ErrNoSite
	}
	return rows[0], nil
}

// CreateUser makes a user of the main site with a personal account, as registration does, but with no inviter and no
// invitation. The password is checked as a registration's is.
func (o *Operator) CreateUser(ctx context.Context, email, password string) (UserID, AccountID, error) {
	email = normalEmail(email)
	if !emailAllowed(email) {
		return UserID{}, AccountID{}, ErrBadEmail
	}
	if !passwordAllowed(password) {
		return UserID{}, AccountID{}, ErrWeakPassword
	}
	site, err := o.mainSite(ctx)
	if err != nil {
		return UserID{}, AccountID{}, err
	}
	role, err := o.role(ctx, "owner")
	if err != nil {
		return UserID{}, AccountID{}, err
	}
	user, account, hash := uuid.NewV7(), uuid.NewV7(), hashPassword(password) // before the transaction: it takes a moment
	err = o.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) error {
		if err := createUser(ctx, tx, o.queries, o.opened, newUser{
			id: user, account: account, site: site.ID, tier: site.DefaultTierID, role: role, email: email, passwordHash: hash,
		}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{Actor: operator, Action: "user.create", Target: userTarget(site.ID, account, user)})
	})
	if constraint, ok := pg.Duplicate(err); ok && constraint == "users_site_id_email_key" {
		return UserID{}, AccountID{}, ErrEmailTaken
	}
	if err != nil {
		return UserID{}, AccountID{}, fmt.Errorf("create a user: %w", err)
	}
	return UserID(user), AccountID(account), nil
}

// role is the ID of the built-in role named name.
func (o *Operator) role(ctx context.Context, name string) (uuid.UUID, error) {
	var ids []uuid.UUID
	if err := o.db.ReadTx(ctx, o.platform, func(tx pg.Tx) (err error) {
		ids, err = o.queries.RoleByName(ctx, tx, name)
		return err
	}); err != nil {
		return uuid.UUID{}, fmt.Errorf("find the role %s: %w", name, err)
	}
	if len(ids) == 0 {
		return uuid.UUID{}, fmt.Errorf("%w: no role named %q", ErrNotFound, name)
	}
	return ids[0], nil
}

// userOf is the main site's user with email, or ErrNotFound.
func (o *Operator) userOf(ctx context.Context, email string) (site uuid.UUID, user UserByEmailRow, err error) {
	main, err := o.mainSite(ctx)
	if err != nil {
		return uuid.UUID{}, UserByEmailRow{}, err
	}
	var rows []UserByEmailRow
	if err := o.db.ReadTx(ctx, o.platform, func(tx pg.Tx) (err error) {
		rows, err = o.queries.UserByEmail(ctx, tx, UserByEmailParams{SiteID: main.ID, Email: normalEmail(email)})
		return err
	}); err != nil {
		return uuid.UUID{}, UserByEmailRow{}, fmt.Errorf("find the user: %w", err)
	}
	if len(rows) == 0 {
		return uuid.UUID{}, UserByEmailRow{}, fmt.Errorf("%w: no user with this email", ErrNotFound)
	}
	return main.ID, rows[0], nil
}

func userTarget(site, account, user uuid.UUID) audit.Target {
	return audit.Target{Module: "accounts", Kind: "user", ID: UserID(user).TypeID(), Site: site, Account: account}
}

// GrantPlatform gives the main site's user with email the built-in platform role roleName. A user who already holds
// it is left as is.
func (o *Operator) GrantPlatform(ctx context.Context, email, roleName string) error {
	site, user, err := o.userOf(ctx, email)
	if err != nil {
		return err
	}
	role, err := o.role(ctx, roleName)
	if err != nil {
		return err
	}
	err = o.db.Tx(ctx, o.platform, func(tx pg.Tx) error {
		granted, err := o.queries.InsertPlatformGrant(ctx, tx, InsertPlatformGrantParams{
			ID: uuid.NewV7(), SiteID: site, AccountID: user.AccountID, UserID: user.ID, RoleID: role,
		})
		if err != nil || granted == 0 {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{
			Actor: operator, Action: "platform_grant.create", Target: userTarget(site, user.AccountID, user.ID),
			Detail: map[string]any{"role": roleName},
		})
	})
	if err != nil {
		return fmt.Errorf("grant a platform role: %w", err)
	}
	return nil
}

// SetPassword replaces the main site's user's password, which raises the factor version: the user's sessions, pending
// sign-ins and unused recovery codes stop working.
func (o *Operator) SetPassword(ctx context.Context, email, password string) error {
	if !passwordAllowed(password) {
		return ErrWeakPassword
	}
	hash := hashPassword(password)
	return o.changeFactor(ctx, email, "user.password_set", func(tx pg.Tx, user uuid.UUID) error {
		return o.queries.SetPassword(ctx, tx, SetPasswordParams{PasswordHash: hash, ID: user})
	})
}

// ResetMFA turns off the main site's user's two-step verification: the secret and the way to sign in with a recovery
// code go, and so does every session. The user can set it up again from the console.
func (o *Operator) ResetMFA(ctx context.Context, email string) error {
	return o.changeFactor(ctx, email, "user.mfa_reset", func(tx pg.Tx, user uuid.UUID) error {
		return o.queries.ResetMFA(ctx, tx, user)
	})
}

// changeFactor locks the user's row first, as every change of factors does (README.md「注册、登录与会话」), runs
// change, which raises the factor version, and records action, all in the user's account scope.
func (o *Operator) changeFactor(ctx context.Context, email, action string, change func(pg.Tx, uuid.UUID) error) error {
	site, user, err := o.userOf(ctx, email)
	if err != nil {
		return err
	}
	err = o.db.Tx(ctx, pg.AccountScope(user.AccountID), func(tx pg.Tx) error {
		if _, err := o.queries.LockUser(ctx, tx, user.ID); err != nil {
			return err
		}
		if err := change(tx, user.ID); err != nil {
			return err
		}
		if err := o.queries.BumpFactorVersion(ctx, tx, user.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{Actor: operator, Action: action, Target: userTarget(site, user.AccountID, user.ID)})
	})
	if err != nil {
		return fmt.Errorf("%s: %w", action, err)
	}
	return nil
}
