package accounts_test

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// auditActions is the actions of the state changes in the audit log, oldest first.
func auditActions(t *testing.T, db *pg.DB) []string {
	t.Helper()
	return texts(t, db, platform, `SELECT action FROM platform.audit_log
		WHERE action IN ('account.suspend', 'account.resume', 'user.disable', 'user.enable') ORDER BY id`)
}

// TestSuspendingAnAccountRefusesItsKeys: suspending an account refuses the calls of its keys from the next call on;
// the change is audited, a second suspension writes nothing, and resuming the account admits the keys again.
func TestSuspendingAnAccountRefusesItsKeys(t *testing.T) {
	t.Parallel()
	db, _, s, key := callSetup(t)
	caller, err := s.AuthenticateKey(t.Context(), accounts.SiteID(s1), key)
	if err != nil {
		t.Fatal(err)
	}
	admit := func() error {
		return s.Admit(t.Context(), caller, accounts.AdmitRequest{Domain: apiDomain, ClientIP: client},
			func(pg.Tx, accounts.Admission) error { return nil })
	}
	if err := admit(); err != nil {
		t.Fatalf("before the change: %v", err)
	}
	change := func(to accounts.Status) accounts.AccountDetail {
		t.Helper()
		var detail accounts.AccountDetail
		inPlatform(t, db, func(tx pg.Tx) error {
			var err error
			detail, err = s.SetAccountStatus(t.Context(), tx, system, accounts.AccountID(a1), to, "a test")
			return err
		})
		return detail
	}
	if got := change(accounts.StatusDisabled); got.Status != string(accounts.StatusDisabled) {
		t.Errorf("the account is %q after suspending it, want %q", got.Status, accounts.StatusDisabled)
	}
	if err := admit(); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("a call with the account suspended: %v, want refused", err)
	}
	if got := change(accounts.StatusDisabled); got.Status != string(accounts.StatusDisabled) {
		t.Errorf("the account is %q after suspending it again", got.Status)
	}
	if got := auditActions(t, db); !slices.Equal(got, []string{"account.suspend"}) {
		t.Errorf("the audit log after two suspensions: %v, want one entry", got)
	}
	if got := change(accounts.StatusActive); got.Status != string(accounts.StatusActive) {
		t.Errorf("the account is %q after resuming it, want %q", got.Status, accounts.StatusActive)
	}
	if err := admit(); err != nil {
		t.Errorf("a call with the account resumed: %v", err)
	}
	if got := auditActions(t, db); !slices.Equal(got, []string{"account.suspend", "account.resume"}) {
		t.Errorf("the audit log: %v", got)
	}
	changes := texts(t, db, platform, `SELECT (detail->>'from') || '>' || (detail->>'to') || ' ' || reason
		FROM platform.audit_log WHERE action IN ('account.suspend', 'account.resume') ORDER BY id`)
	if want := []string{"active>disabled a test", "disabled>active a test"}; !slices.Equal(changes, want) {
		t.Errorf("the changes recorded: %v, want %v", changes, want)
	}
	inPlatform(t, db, func(tx pg.Tx) error {
		if _, err := s.SetAccountStatus(t.Context(), tx, system, accounts.AccountID(x), accounts.StatusDisabled, "a test"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("an account that is not there: %v, want not found", err)
		}
		return nil
	})
}

// keyFor makes a call key in p's own account and returns its text.
func (x site) keyFor(t *testing.T, p accounts.Principal) string {
	t.Helper()
	var created accounts.CreatedKey
	x.inAccount(t, p, func(tx pg.Tx) error {
		var err error
		created, err = x.s.CreateKey(t.Context(), tx, system, accounts.KeyRequest{
			Site: accounts.SiteID(s1), Account: p.AccountID(), CreatedBy: p.UserID(), Name: "K",
		})
		return err
	})
	return created.Text.Reveal()
}

// suspend puts p's account in to and returns what the change answered.
func (x site) suspend(t *testing.T, p accounts.Principal, to accounts.Status) accounts.AccountDetail {
	t.Helper()
	var detail accounts.AccountDetail
	inPlatform(t, x.db, func(tx pg.Tx) error {
		var err error
		detail, err = x.s.SetAccountStatus(t.Context(), tx, system, p.AccountID(), to, "a test")
		return err
	})
	return detail
}

// disable puts p's user in to and returns what the change answered.
func (x site) disable(t *testing.T, p accounts.Principal, to accounts.Status) accounts.UserSummary {
	t.Helper()
	var summary accounts.UserSummary
	inPlatform(t, x.db, func(tx pg.Tx) error {
		var err error
		summary, err = x.s.SetUserStatus(t.Context(), tx, system, p.UserID(), to, "a test")
		return err
	})
	return summary
}

// TestSuspendingAnAccountLeavesTheLoginAlone: with the account suspended its keys are refused at the next call and its
// console still signs in, because a suspension stops calls and not people; resuming admits the keys again.
func TestSuspendingAnAccountLeavesTheLoginAlone(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+apiDomain+`', :s1, 'api', 'https://`+apiDomain+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	p := x.signedIn(t)
	key := x.keyFor(t, p)
	callOK := func() error { return call(t, x.s, s1, key, apiDomain) }
	signIn := func() error {
		_, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password, ClientIP: client})
		return err
	}
	if err := callOK(); err != nil {
		t.Fatalf("before the change: %v", err)
	}
	if got := x.suspend(t, p, accounts.StatusDisabled); got.Status != string(accounts.StatusDisabled) {
		t.Errorf("the account is %q after suspending it, want %q", got.Status, accounts.StatusDisabled)
	}
	if err := callOK(); !errors.Is(err, accounts.ErrUnknownKey) {
		t.Errorf("a call with the account suspended: %v, want refused", err)
	}
	if err := signIn(); err != nil {
		t.Errorf("a sign-in on a suspended account: %v", err)
	}
	if got := x.suspend(t, p, accounts.StatusActive); got.Status != string(accounts.StatusActive) {
		t.Errorf("the account is %q after resuming it, want %q", got.Status, accounts.StatusActive)
	}
	if err := callOK(); err != nil {
		t.Errorf("a call with the account resumed: %v", err)
	}
}

// TestDisablingALoginStopsTheSessionsAlone: disabling the user ends every session it has and refuses its sign-in while
// the account's keys keep working; enabling it lets it sign in again and brings no session back; only a change of state
// is written down.
func TestDisablingALoginStopsTheSessionsAlone(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	if err := pgtest.Run(t.Context(), x.database.Superuser, ids.Replace(
		`INSERT INTO accounts.site_domains VALUES ('`+apiDomain+`', :s1, 'api', 'https://`+apiDomain+`', NULL)`)); err != nil {
		t.Fatal(err)
	}
	signIn := func() error {
		_, err := x.s.SignIn(t.Context(), accounts.SignInRequest{Host: console, Email: x.email, Password: password, ClientIP: client})
		return err
	}
	p := x.signedIn(t)
	key := x.keyFor(t, p)
	issued := x.signIn(t, console) // a second session, both ended by the disabling
	if got := x.disable(t, p, accounts.StatusDisabled); got.Status != string(accounts.StatusDisabled) {
		t.Errorf("the user is %q after disabling it, want %q", got.Status, accounts.StatusDisabled)
	}
	if err := signIn(); !errors.Is(err, accounts.ErrBadCredentials) {
		t.Errorf("a sign-in of a disabled user: %v, want refused", err)
	}
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("a session of a disabled user: %v, want refused", err)
	}
	if err := call(t, x.s, s1, key, apiDomain); err != nil {
		t.Errorf("a call while the user is disabled: %v", err)
	}
	ended := texts(t, x.db, platform, `SELECT detail->>'sessions' FROM platform.audit_log WHERE action = 'user.disable'`)
	if !slices.Equal(ended, []string{"2"}) { // the session of signedIn and the one issued after
		t.Errorf("the sessions the disabling ended: %v, want 2", ended)
	}
	x.disable(t, p, accounts.StatusDisabled)
	if got := auditActions(t, x.db); !slices.Equal(got, []string{"user.disable"}) {
		t.Errorf("the audit log after disabling twice: %v", got)
	}
	x.disable(t, p, accounts.StatusActive)
	if err := signIn(); err != nil {
		t.Errorf("a sign-in after enabling the user: %v", err)
	}
	if _, err := x.principal(t, console, issued); !errors.Is(err, accounts.ErrNoSession) {
		t.Errorf("the session of a disabled user came back: %v", err)
	}
	inPlatform(t, x.db, func(tx pg.Tx) error {
		if _, err := x.s.SetUserStatus(t.Context(), tx, system, accounts.UserID(uuid.NewV7()), accounts.StatusDisabled, "a test"); !errors.Is(err, accounts.ErrNotFound) {
			t.Errorf("a user that is not there: %v, want not found", err)
		}
		return nil
	})
}
