package accounts_test

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// TestOriginIsTheRegisteredOne: a host's origin is the one its domain registered, whatever case the host is written in;
// an unknown host has none.
func TestOriginIsTheRegisteredOne(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	s := node(t, db, hashKeys(t, 1), time.Now)
	if origin, err := s.Origin("MAIN.example"); err != nil || origin != "https://main.example" {
		t.Errorf("the origin of main.example: %q, %v", origin, err)
	}
	if _, err := s.Origin("other.example"); !errors.Is(err, accounts.ErrUnknownHost) {
		t.Errorf("the origin of an unknown host: %v", err)
	}
}

// TestAuthorizedScopeSeesItsOwnSite: the scope of an account the console authorized reads its own site's public
// configuration and shared counters; the scope carries no other home site.
func TestAuthorizedScopeSeesItsOwnSite(t *testing.T) {
	t.Parallel()
	x := newTestSite(t)
	p, err := x.principal(t, console, x.signIn(t, console))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := x.s.Authorize(t.Context(), p, accounts.ViewBalance, accounts.AccountTarget(p.AccountID()))
	if err != nil {
		t.Fatal(err)
	}
	for site, want := range map[uuid.UUID]bool{s1: true, s2: false} {
		var sees bool
		err := x.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			return tx.QueryRow(t.Context(), "SELECT platform.home_site() = $1", site).Scan(&sees)
		})
		if err != nil || sees != want {
			t.Errorf("the scope sees site %s: %v, %v; want %v", site, sees, err, want)
		}
	}
}
