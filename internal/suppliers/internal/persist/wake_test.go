package persist_test

import (
	"context"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/registry"
	"axiom/internal/suppliers/internal/syncer"
)

type replaceableLogin struct {
	adapter.Adapter
	adapter.Onboarding
}

func (replaceableLogin) Kind() adapter.Kind            { return "fake" }
func (replaceableLogin) Buckets() []adapter.BucketSpec { return nil }

func (replaceableLogin) LoginFields(bool) []adapter.LoginField {
	return []adapter.LoginField{adapter.FieldAccessToken}
}

func (replaceableLogin) NewLogin(c adapter.Credentials) (adapter.Login, error) {
	return adapter.Login{Kind: "tokens", Plaintext: []byte(c.AccessToken)}, nil
}

// The balance keeps succeeding while the catalog stops and the other tasks wait beyond the next balance read.
type unreadableCatalog struct{ adapter.Manager }

func (unreadableCatalog) Account(context.Context) (adapter.Account, error) {
	return adapter.Account{Balance: "10", Funding: adapter.FundingWallet}, nil
}

func (unreadableCatalog) Offer(context.Context) (adapter.Offer, error) {
	return adapter.Offer{}, adapter.ErrUnrecognised
}

func (unreadableCatalog) Keys(context.Context) ([]adapter.KeyState, error) {
	return nil, adapter.Blocked{Until: t0.Add(time.Hour)}
}

func (unreadableCatalog) Charges(context.Context, adapter.ScanState) (adapter.ChargePage, adapter.ScanState, error) {
	return adapter.ChargePage{}, nil, adapter.Blocked{Until: t0.Add(time.Hour)}
}

// An alias added after the replacement must project even if the catalog task never succeeds again.
func TestAWokenSupplierStillProjectsTheListing(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	known := c.model("known-model", catalog.StatusListed)
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "mystery-model", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	c.commitListing(c.acquire(c.node("the node with the old login")), 1, groups)
	same(t, "the endpoints of a listing with no name mapped yet", c.column("endpoints", "upstream_name", "upstream_name"), nil)

	r, err := registry.New(replaceableLogin{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := suppliers.NewAdmin(suppliers.AdminConfig{Keys: c.keys, Registry: r, Clock: c.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := admin.ReplaceLogin(t.Context(), tx, audit.System("suppliers.test"), c.supplier,
			suppliers.Credentials{AccessToken: "replacement-token"}, "the old login was revoked")
		return err
	}); err != nil {
		t.Fatalf("replace the login: %v", err)
	}

	s := c.acquire(c.node("the node with the new login"))
	engine := syncer.New(s.Store(), unreadableCatalog{}, syncer.Defaults(), c.clock.now)
	if err := engine.Round(t.Context()); err != nil {
		t.Fatalf("the first round after replacement: %v", err)
	}
	st, err := s.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !st.Tasks[syncer.TaskCatalog].Halted {
		t.Fatal("ErrUnrecognised did not halt the catalog task")
	}
	same(t, "the endpoint before the alias", c.column("endpoints", "upstream_name", "upstream_name"), nil)
	c.alias(uuid.Nil(), "mystery-model", known)

	// The balance is due again; the halted catalog cannot refresh the listing.
	c.clock.advance(syncer.Defaults().Interval[syncer.TaskBalance])
	if err := engine.Round(t.Context()); err != nil {
		t.Fatalf("the balance round after adding the alias: %v", err)
	}
	final, err := s.Store().Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if final.Version != st.Version+1 || !final.Tasks[syncer.TaskBalance].LastOK.Equal(c.clock.now()) {
		t.Fatalf("the balance round did not commit exactly one balance step: %+v", final.Tasks)
	}
	same(t, "the endpoint of the name a person mapped",
		c.column("endpoints", "upstream_name || ' ' || model_id::text", "upstream_name"), []string{"mystery-model " + known.String()})
}
