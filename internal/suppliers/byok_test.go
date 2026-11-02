package suppliers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/store"
)

// byokFixture makes one key with a listed model for an account and returns it.
func byokFixture(t *testing.T, a admins, site, account uuid.UUID) ByokKey {
	t.Helper()
	registerTenant(a, site, account)
	key, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.CreateByok(ctx, tx, ByokRequest{
			Site: site, Account: account, Name: "my key", Provider: "openai",
			Key: "sk-proj-abcdefghijklmnop", Fallback: true, Actor: a.actor,
		}, []ByokModel{{
			ID: uuid.NewV7(), Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`),
		}})
	})
	refuses(t, "making a provider key", err, nil)
	return key
}

// registerTenant makes the audit log's foreign key accept the site and account a test uses.
func registerTenant(a admins, site, account uuid.UUID) {
	a.sql(
		fmt.Sprintf("INSERT INTO platform.tenants (site_id, tenant_id) VALUES ('%s', '%s') ON CONFLICT DO NOTHING", site, site),
		fmt.Sprintf("INSERT INTO platform.tenants (site_id, tenant_id) VALUES ('%s', '%s') ON CONFLICT DO NOTHING", site, account),
	)
}

// TestByokLifecycle: a key is sealed, listed with only its last four characters, switched off, refused by the provider
// and cleared by a successful test; removing it erases the ciphertext and keeps the row for the attempts that used it.
func TestByokLifecycle(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	key := byokFixture(t, a, site, account)
	same(t, "the key as made", []any{"active", true, false, true, "mnop"}, []any{
		key.Status, key.Enabled, key.Rejected, key.Fallback, key.Hint,
	})

	var listed []ByokKey
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		listed, err = a.admin.ListByok(t.Context(), tx, site, account)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the list", 1, len(listed))

	var plain string
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, _, secret, err := a.admin.RevealByok(t.Context(), tx, site, account, key.ID)
		plain = secret.Reveal()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the sealed key opens to what was sent", "sk-proj-abcdefghijklmnop", plain)

	disabled, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, false, a.actor)
	})
	refuses(t, "switching the key off", err, nil)
	same(t, "a disabled key", []any{"disabled", false}, []any{disabled.Status, disabled.Enabled})

	rejected, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokTestResult(ctx, tx, site, account, key.ID, false, a.actor)
	})
	refuses(t, "a refused key", err, nil)
	same(t, "a refused key", []any{"rejected", true}, []any{rejected.Status, rejected.Rejected})

	again, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, true, a.actor)
	})
	refuses(t, "switching a refused key on", err, nil)
	same(t, "the switch cannot clear a refusal", "rejected", again.Status)

	accepted, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokTestResult(ctx, tx, site, account, key.ID, true, a.actor)
	})
	refuses(t, "a key that works again", err, nil)
	same(t, "a successful test makes it active", "active", accepted.Status)

	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (struct{}, error) {
		return struct{}{}, a.admin.RemoveByok(ctx, tx, site, account, key.ID, a.actor)
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "removed", []any{"removed", "0"}, []any{
		a.text(`SELECT status FROM suppliers.suppliers WHERE id = '` + key.ID.String() + `'`),
		a.text(`SELECT coalesce(length(ciphertext), 0)::text FROM suppliers.credentials WHERE supplier_id = '` + key.ID.String() + `'`),
	})
}

// TestByokIsPerAccount: another account sees no key and cannot read, switch off or remove it: the account's scope is
// what finds the row, so an id from elsewhere is the same answer as one that does not exist.
func TestByokIsPerAccount(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, owner, other := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	key := byokFixture(t, a, site, owner)
	if err := a.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		list, err := a.admin.ListByok(t.Context(), tx, site, other)
		if err != nil {
			return err
		}
		same(t, "another account's list", 0, len(list))
		if _, _, _, err := a.admin.RevealByok(t.Context(), tx, site, other, key.ID); !errors.Is(err, ErrByokNotFound) {
			t.Errorf("another account opened the key: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, other, key.ID, false, a.actor)
	}); !errors.Is(err, ErrByokNotFound) {
		t.Errorf("another account switched the key off: %v", err)
	}
}

// credentialOf is the key's credential id, read the way the service reads it.
func byokCredential(ctx context.Context, t *testing.T, a admins, id uuid.UUID) uuid.UUID {
	t.Helper()
	var credential uuid.UUID
	if err := a.db.ReadTx(ctx, platformScope, func(tx pg.Tx) error {
		var err error
		credential, err = store.New().CredentialOfByok(ctx, tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return credential
}

// TestByokWritesInAccountScope: the console's four writes run in the account's own transaction, which is what the audit
// log's row-level security and its registered-tenant foreign key check. A platform transaction would hide a missing
// tenant in the audit target, so this test uses only pg.AccountScope.
func TestByokWritesInAccountScope(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	registerTenant(a, site, account)
	model := uuid.NewV7()
	ctx := t.Context()

	var key ByokKey
	if err := a.db.Tx(ctx, pg.AccountScope(account), func(tx pg.Tx) (err error) {
		key, err = a.admin.CreateByok(ctx, tx, ByokRequest{
			Site: site, Account: account, Name: "scoped", Provider: "openai",
			Key: "sk-proj-abcdefghijklmnop", Fallback: true, Actor: a.actor,
		}, []ByokModel{{
			ID: model, Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`),
		}})
		return err
	}); err != nil {
		t.Fatalf("create in the account's scope: %v", err)
	}

	for _, step := range []struct {
		name string
		do   func(pg.Tx) error
	}{
		{"disable", func(tx pg.Tx) error {
			_, err := a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, false, a.actor)
			return err
		}},
		{"test", func(tx pg.Tx) error {
			_, err := a.admin.SetByokTestResult(ctx, tx, site, account, key.ID, true, a.actor)
			return err
		}},
		{"remove", func(tx pg.Tx) error {
			return a.admin.RemoveByok(ctx, tx, site, account, key.ID, a.actor)
		}},
	} {
		if err := a.db.Tx(ctx, pg.AccountScope(account), step.do); err != nil {
			t.Errorf("%s in the account's scope: %v", step.name, err)
		}
	}
}

// TestByokCodenameIsUnique: the group's codename is globally unique. A prefix of a UUIDv7 repeats for every key made in
// the same 65.536-second window, so two keys made together would fail the unique constraint; the name is random instead.
func TestByokCodenameIsUnique(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site := uuid.NewV7()
	first, second := uuid.NewV7(), uuid.NewV7()
	one := byokFixture(t, a, site, first)
	two := byokFixture(t, a, site, second)
	name := func(id uuid.UUID) string {
		return a.text(`SELECT codename FROM suppliers.supplier_groups WHERE supplier_id = '` + id.String() + `'`)
	}
	if a, b := name(one.ID), name(two.ID); a == b || len(a) < 9 || a[:1] != "g" {
		t.Errorf("codenames %q and %q, want two different random names", a, b)
	}
}

// TestByokGate: a deployment that has not enabled provider keys refuses to create one, whatever the console asks.
func TestByokGate(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	a.admin.byok = false
	site, account := uuid.NewV7(), uuid.NewV7()
	registerTenant(a, site, account)
	_, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.CreateByok(ctx, tx, ByokRequest{
			Site: site, Account: account, Name: "off", Provider: "openai",
			Key: "sk-proj-abcdefghijklmnop", Fallback: true, Actor: a.actor,
		}, []ByokModel{{ID: uuid.NewV7(), Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat}}})
	})
	if !errors.Is(err, ErrByokDisabled) {
		t.Fatalf("creating a key while the gate is off: %v, want ErrByokDisabled", err)
	}
}

// TestByokNoFallbackSurvivesRejection: the rule is read from the key's rows, not from its working credentials, so a 401
// that takes the key out of routing does not let the next request spend the platform's endpoints.
func TestByokNoFallbackSurvivesRejection(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	registerTenant(a, site, account)
	model := uuid.NewV7()
	var key ByokKey
	if err := a.db.Tx(t.Context(), pg.AccountScope(account), func(tx pg.Tx) (err error) {
		key, err = a.admin.CreateByok(t.Context(), tx, ByokRequest{
			Site: site, Account: account, Name: "no fallback", Provider: "openai",
			Key: "sk-proj-abcdefghijklmnop", Fallback: false, Actor: a.actor,
		}, []ByokModel{{
			ID: model, Upstream: "gpt-5", Formats: []protocol.Format{protocol.Chat},
			Schedule: []byte(`{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`),
		}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	credential := byokCredential(t.Context(), t, a, key.ID)
	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (struct{}, error) {
		return struct{}{}, store.New().SetByokRejected(ctx, tx, credential)
	}); err != nil {
		t.Fatal(err)
	}
	rules, err := ByokRules(t.Context(), a.db, pg.PlatformScope())
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Site != site || rules[0].Account != account || rules[0].Model != model {
		t.Fatalf("the rules after a 401 are %+v, want the account's no-fallback rule for %s", rules, model)
	}
}

// TestByokSwitchCannotOverwriteARejection: a switch and a rejection that cross must not end with the switch putting
// back a status the rejection just wrote. The switch reads the credential again under its row lock, so the later
// writer's view wins instead of the status it read before waiting.
func TestByokSwitchCannotOverwriteARejection(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	site, account := uuid.NewV7(), uuid.NewV7()
	key := byokFixture(t, a, site, account)
	credential := byokCredential(t.Context(), t, a, key.ID)
	if _, err := inTx(a, func(ctx context.Context, tx pg.Tx) (ByokKey, error) {
		return a.admin.SetByokEnabled(ctx, tx, site, account, key.ID, false, a.actor)
	}); err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- a.db.Tx(t.Context(), pg.AccountScope(account), func(tx pg.Tx) error {
			if _, err := tx.Exec(t.Context(), `SELECT id FROM suppliers.credentials WHERE id = $1 FOR UPDATE`, credential); err != nil {
				return err
			}
			close(locked)
			<-release
			_, err := tx.Exec(t.Context(), `UPDATE suppliers.credentials SET status = 'rejected' WHERE id = $1`, credential)
			return err
		})
	}()
	<-locked

	started := make(chan struct{})
	switched := make(chan error, 1)
	go func() {
		switched <- a.db.Tx(t.Context(), pg.AccountScope(account), func(tx pg.Tx) error {
			close(started)
			_, err := a.admin.SetByokEnabled(t.Context(), tx, site, account, key.ID, true, a.actor)
			return err
		})
	}()
	<-started
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	if status := a.text(`SELECT status FROM suppliers.credentials WHERE id = '` + credential.String() + `'`); status != "rejected" {
		t.Fatalf("the switch left the credential %q, want rejected", status)
	}
}
