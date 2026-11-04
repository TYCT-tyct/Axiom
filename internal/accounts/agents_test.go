package accounts_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// manualClock is a node's clock that a test moves.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *manualClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// agentSetup is the seeded database and a node on it whose clock the test moves.
func agentSetup(t *testing.T) (*pg.DB, pgtest.Database, *accounts.Service, *manualClock) {
	t.Helper()
	db, database := open(t)
	clock := &manualClock{now: time.Now()}
	return db, database, node(t, db, hashKeys(t, 1), clock.Now), clock
}

// makeAgent makes a credential through s in the platform scope and returns its token.
func makeAgent(t *testing.T, db *pg.DB, s *accounts.Service, spec accounts.AgentSpec) accounts.AgentToken {
	t.Helper()
	var made accounts.AgentToken
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
		made, err = s.CreateAgent(t.Context(), tx, system, spec)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return made
}

// agentLife is how long the test's credentials live: the owner chooses it for each one.
const agentLife = 30 * 24 * time.Hour

// agentSpec is a credential of perms that expires agentLife after clock's now, the clock the test moves.
func agentSpec(clock *manualClock, perms ...string) accounts.AgentSpec {
	return accounts.AgentSpec{
		Name: "hermes", Permissions: perms, Vendors: []string{"deepseek"}, CreatedBy: accounts.UserID(u1),
		ExpiresAt: clock.Now().Add(agentLife), Reason: "r",
	}
}

// TestAgentCredentialLifecycle: a new credential's token authenticates, always holds operations.read and records its
// use; it stops working at its expiry.
func TestAgentCredentialLifecycle(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	spec := agentSpec(clock, "suppliers.view", "calls.search", "suppliers.view")
	spec.Sites = []accounts.SiteID{accounts.SiteID(s1)}
	made := makeAgent(t, db, s, spec)
	if !strings.HasPrefix(made.Token.Reveal(), "op-ax-v1-") {
		t.Fatalf("token %q lacks the prefix", made.Token.Reveal()[:9])
	}
	agent, err := s.AuthenticateAgent(t.Context(), made.Token.Reveal())
	if err != nil {
		t.Fatal(err)
	}
	info := agent.Info()
	if info.ID != made.ID || strings.Join(info.Permissions, ",") != "calls.search,operations.read,suppliers.view" ||
		info.Platform || len(info.Sites) != 1 || info.Sites[0] != accounts.SiteID(s1) {
		t.Errorf("the credential is %+v", info)
	}
	if got := texts(t, db, platform, "SELECT (last_used_at IS NOT NULL)::text FROM accounts.agent_credentials")[0]; got != "true" {
		t.Errorf("last use recorded: %s", got)
	}
	clock.advance(agentLife) // the expiry itself: a credential is live only before it
	expectAgent(t, s, made.Token.Reveal(), accounts.ErrUnknownAgent)
}

// TestAgentCredentialRotation: a rotation keeps the old token for the overlap and no longer, and a second rotation ends
// the first one's overlap at once, so at most two tokens work.
func TestAgentCredentialRotation(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	first := makeAgent(t, db, s, agentSpec(clock))
	rotate := func(overlap time.Duration) accounts.AgentToken {
		t.Helper()
		var made accounts.AgentToken
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
			made, err = s.RotateAgent(t.Context(), tx, system, first.ID, overlap, "r")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return made
	}
	second := rotate(time.Hour)
	third := rotate(2 * time.Hour)
	expectAgent(t, s, first.Token.Reveal(), accounts.ErrUnknownAgent)
	expectAgent(t, s, second.Token.Reveal(), nil)
	expectAgent(t, s, third.Token.Reveal(), nil)
	clock.advance(2*time.Hour + time.Second)
	expectAgent(t, s, second.Token.Reveal(), accounts.ErrUnknownAgent)
	expectAgent(t, s, third.Token.Reveal(), nil)
}

// TestAgentCredentialRevocation: a revocation and the emergency stop refuse every token from the next request; a
// revoked credential cannot be revoked again; a second stop revokes nothing; only a new credential works after it.
func TestAgentCredentialRevocation(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	revoke := func(id accounts.AgentID) error {
		return db.Tx(t.Context(), platform, func(tx pg.Tx) error { return s.RevokeAgent(t.Context(), tx, system, id, "r") })
	}
	stop := func() int {
		t.Helper()
		var n int
		if err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
			n, err = s.StopAgents(t.Context(), tx, system, "stop")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first, other := makeAgent(t, db, s, agentSpec(clock)), makeAgent(t, db, s, agentSpec(clock))
	if err := revoke(first.ID); err != nil {
		t.Fatal(err)
	}
	expectAgent(t, s, first.Token.Reveal(), accounts.ErrUnknownAgent)
	expectAgent(t, s, other.Token.Reveal(), nil)
	if err := revoke(first.ID); !errors.Is(err, accounts.ErrAgentNotFound) {
		t.Errorf("revoking twice: %v", err)
	}
	late := makeAgent(t, db, s, agentSpec(clock))
	if n := stop(); n != 2 {
		t.Errorf("the stop revoked %d, want the 2 live ones", n)
	}
	expectAgent(t, s, other.Token.Reveal(), accounts.ErrUnknownAgent)
	expectAgent(t, s, late.Token.Reveal(), accounts.ErrUnknownAgent)
	if n := stop(); n != 0 {
		t.Errorf("a second stop revoked %d", n)
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM platform.audit_log WHERE action = 'operations_agent.stop'")[0]; got != "2" {
		t.Errorf("%s stops audited", got)
	}
	expectAgent(t, s, makeAgent(t, db, s, agentSpec(clock)).Token.Reveal(), nil)
}

// TestExpiredAgentIsGone: an expired credential that nobody revoked is not found by a revocation, not counted by the
// stop, and its rotation's overlap is not listed, as it can no longer authenticate.
func TestExpiredAgentIsGone(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	spec := agentSpec(clock)
	spec.ExpiresAt = clock.Now().Add(time.Hour) // before the rotation's overlap ends
	expiring := makeAgent(t, db, s, spec)
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := s.RotateAgent(t.Context(), tx, system, expiring.ID, accounts.AgentOverlap, "r")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Hour)
	err := db.Tx(t.Context(), platform, func(tx pg.Tx) error { return s.RevokeAgent(t.Context(), tx, system, expiring.ID, "r") })
	if !errors.Is(err, accounts.ErrAgentNotFound) {
		t.Errorf("revoking an expired credential: %v", err)
	}
	var stopped int
	var list []accounts.AgentCredential
	if err := db.Tx(t.Context(), platform, func(tx pg.Tx) (err error) {
		if stopped, err = s.StopAgents(t.Context(), tx, system, "stop"); err != nil {
			return err
		}
		list, _, err = s.AgentCredentials(t.Context(), tx, accounts.AgentID{}, 10)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if stopped != 0 || len(list) != 1 || list[0].PreviousUntil != nil || list[0].RevokedAt != nil {
		t.Errorf("the stop counted %d; the list is %+v", stopped, list)
	}
}

func expectAgent(t *testing.T, s *accounts.Service, token string, want error) {
	t.Helper()
	if _, err := s.AuthenticateAgent(t.Context(), token); !errors.Is(err, want) {
		t.Errorf("authenticating the token: %v, want %v", err, want)
	}
}

// TestAgentTokenShape: a token that is not a well-formed operations token, an API key's shape included, is refused
// before the database is asked.
func TestAgentTokenShape(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	token := makeAgent(t, db, s, agentSpec(clock)).Token.Reveal()
	for name, raw := range map[string]string{
		"empty":            "",
		"one character":    token[:len(token)-1] + string(token[len(token)-1]^1),
		"an API key":       "sk-ax-v1-" + token[len("op-ax-v1-"):],
		"no prefix":        token[len("op-ax-v1-"):],
		"another checksum": token[:len(token)-6] + "000000",
	} {
		if _, err := s.AuthenticateAgent(t.Context(), raw); !errors.Is(err, accounts.ErrUnknownAgent) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestCreateAgentRefuses: the control plane's permissions, undeclared ones, unknown sites, a missing or malformed
// vendor and an expiry outside the lifetime are refused with where the request is wrong, and nothing is written.
func TestCreateAgentRefuses(t *testing.T) {
	t.Parallel()
	db, database, s, clock := agentSetup(t)
	past := clock.Now().Add(-time.Minute)
	cases := []struct {
		name  string
		edit  func(*accounts.AgentSpec)
		field string
	}{
		{"its own management", func(a *accounts.AgentSpec) { a.Permissions = []string{"operations.manage"} }, "/permissions/0"},
		{"granting platform roles", func(a *accounts.AgentSpec) { a.Permissions = []string{"calls.search", "platform_grants.manage"} }, "/permissions/1"},
		{"an undeclared permission", func(a *accounts.AgentSpec) { a.Permissions = []string{"operations.approve"} }, "/permissions/0"},
		{"an unknown site", func(a *accounts.AgentSpec) { a.Sites = []accounts.SiteID{accounts.SiteID(a1)} }, "/sites/0"},
		{"no vendor", func(a *accounts.AgentSpec) { a.Vendors = nil }, "/proposer_vendors"},
		{"a vendor in capitals", func(a *accounts.AgentSpec) { a.Vendors = []string{"OpenAI"} }, "/proposer_vendors/0"},
		{"an expiry in the past", func(a *accounts.AgentSpec) { a.ExpiresAt = past }, "/expires_at"},
		{"no expiry", func(a *accounts.AgentSpec) { a.ExpiresAt = time.Time{} }, "/expires_at"},
	}
	for _, c := range cases {
		spec := agentSpec(clock)
		c.edit(&spec)
		err := db.Tx(t.Context(), platform, func(tx pg.Tx) error {
			_, err := s.CreateAgent(t.Context(), tx, system, spec)
			return err
		})
		if bad, ok := errors.AsType[*accounts.AgentSpecError](err); !ok || bad.Field != c.field {
			t.Errorf("%s: %v, want a refusal at %s", c.name, err, c.field)
		}
	}
	if got := texts(t, db, platform, "SELECT count(*)::text FROM accounts.agent_credentials")[0]; got != "0" {
		t.Errorf("%s credentials written", got)
	}
	// The table refuses the control plane's permissions even from a writer that skips the code's checks.
	err := pgtest.Run(t.Context(), database.Superuser, `INSERT INTO accounts.agent_credentials
		(id, name, permissions, platform, proposer_vendors, created_by, expires_at)
		VALUES (gen_random_uuid(), 'x', '{operations.read,operations.manage}', false, '{a}', gen_random_uuid(), now() + interval '1 day')`)
	if err == nil || !strings.Contains(err.Error(), "check") {
		t.Errorf("the table took operations.manage: %v", err)
	}
}

// TestAuthorizeAgent: a permission the credential lacks is forbidden; the platform's resources need its platform grant
// and are otherwise not found; its sites bound everything else.
func TestAuthorizeAgent(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	siteOnly := agentSpec(clock, "calls.search", "suppliers.view")
	siteOnly.Sites = []accounts.SiteID{accounts.SiteID(s1)}
	withPlatform := agentSpec(clock, "suppliers.view")
	withPlatform.Platform = true
	for _, c := range []struct {
		name     string
		spec     accounts.AgentSpec
		perm     accounts.Permission
		platform bool
		want     error
	}{
		{"a permission it lacks", siteOnly, accounts.ViewCatalog, false, accounts.ErrForbidden},
		{"the platform without the grant", siteOnly, accounts.ViewSuppliers, true, accounts.ErrNotFound},
		{"the platform with the grant", withPlatform, accounts.ViewSuppliers, true, nil},
		{"its sites", siteOnly, accounts.SearchCalls, false, nil},
		{"its own record", siteOnly, accounts.OperationsRead, false, nil},
	} {
		agent, err := s.AuthenticateAgent(t.Context(), makeAgent(t, db, s, c.spec).Token.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AuthorizeAgent(agent, c.perm, c.platform); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
}

// TestAgentRequestsAreLimited: a credential gets its burst on a node and then waits; another credential is not held up.
func TestAgentRequestsAreLimited(t *testing.T) {
	t.Parallel()
	db, _, s, clock := agentSetup(t)
	busy := makeAgent(t, db, s, agentSpec(clock)).Token.Reveal()
	quiet := makeAgent(t, db, s, agentSpec(clock)).Token.Reveal()
	for range 60 {
		expectAgent(t, s, busy, nil)
	}
	_, err := s.AuthenticateAgent(t.Context(), busy)
	if wait, ok := errors.AsType[*accounts.TooManyAttempts](err); !ok || wait.RetryAfter <= 0 {
		t.Fatalf("the 61st request: %v", err)
	}
	expectAgent(t, s, quiet, nil)
	clock.advance(100 * time.Millisecond)
	expectAgent(t, s, busy, nil)
}
