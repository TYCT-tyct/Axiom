package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
)

func (a admins) setTo(id uuid.UUID, to Status) (Supplier, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
		return a.admin.SetStatus(ctx, tx, a.actor, id, to, "because")
	})
}

func (a admins) generation() string {
	return a.text(`SELECT config_gen::text FROM suppliers.suppliers`)
}

// TestSetStatus: a supplier in use is paused and a paused one resumed, each once and audited; one as asked is left
// alone, the generation unchanged.
func TestSetStatus(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	a.setStatus(id, "active")
	for _, step := range []struct {
		to   Status
		gen  string
		what string
	}{{Paused, "1", "pausing"}, {Paused, "1", "pausing again"}, {Active, "2", "resuming"}} {
		s, err := a.setTo(id, step.to)
		refuses(t, step.what, err, nil)
		same(t, step.what, []string{string(step.to), step.gen}, []string{string(s.Status), a.generation()})
	}
	same(t, "audit entries", "2", a.text(`SELECT count(*)::text FROM platform.audit_log WHERE action IN ('supplier.pause', 'supplier.resume')`))
}

// TestSetStatusLocked: a supplier that is not in use or paused is not moved by hand; one with a low balance may be
// paused.
func TestSetStatusLocked(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	for _, status := range []string{"connecting", "needs_attention", "removing", "removed"} {
		a.setStatus(id, status)
		_, err := a.setTo(id, Paused)
		refuses(t, "pausing a supplier that is "+status, err, ErrStatusLocked)
	}
	a.setStatus(id, "low_balance")
	s, err := a.setTo(id, Paused)
	refuses(t, "pausing a supplier with a low balance", err, nil)
	same(t, "its status", Paused, s.Status)
	_, err = a.setTo(uuid.NewV7(), Paused)
	refuses(t, "a supplier that is not there", err, ErrNoSupplier)
}

// TestRemove: a supplier is removed from any status but removed, at once; asking again changes nothing, and the
// generation rises once so a round of the sync that began before is discarded.
func TestRemove(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	for i, from := range []string{"connecting", "active", "low_balance", "needs_attention", "paused", "removing"} {
		id := a.connect("https://api.example.com", fmt.Sprintf("ops%d@example.com", i)).ID
		a.setStatus(id, from)
		before := a.text(fmt.Sprintf(`SELECT config_gen::text FROM suppliers.suppliers WHERE id = '%s'`, id))
		remove := func() (Supplier, error) {
			return inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
				return a.admin.Remove(ctx, tx, a.actor, id, "no longer needed")
			})
		}
		for _, what := range []string{"removing from " + from, "removing again"} {
			s, err := remove()
			refuses(t, what, err, nil)
			same(t, what, []any{Removed, "1", "1"}, []any{
				s.Status,
				a.text(fmt.Sprintf(`SELECT (config_gen - %s)::text FROM suppliers.suppliers WHERE id = '%s'`, before, id)),
				a.text(fmt.Sprintf(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'supplier.remove' AND target_id = '%s'`, id)),
			})
		}
	}
	_, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
		return a.admin.Remove(ctx, tx, a.actor, uuid.NewV7(), "r")
	})
	refuses(t, "a supplier that is not there", err, ErrNoSupplier)
}

// TestReplaceLogin: the new login is sealed over the old, its version goes up, and the supplier is `connecting` again
// with the generation raised; a login that cannot be made, and a supplier being removed, leave the old one.
func TestReplaceLogin(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	a.setStatus(id, "needs_attention")
	replace := func(c Credentials) (Supplier, error) {
		return inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
			return a.admin.ReplaceLogin(ctx, tx, a.actor, id, c, "rotated")
		})
	}
	state := func() []string {
		return []string{a.text(`SELECT version::text FROM suppliers.supplier_logins`), a.text(`SELECT encode(ciphertext, 'hex') FROM suppliers.supplier_logins`)}
	}
	before := state()
	_, err := replace(Credentials{})
	refusesLogin(t, "a login without a token", err, "access_token")
	same(t, "the login after a refusal", before, state())
	s, err := replace(Credentials{AccessToken: "new-token"})
	refuses(t, "replacing", err, nil)
	same(t, "the status and generation", []string{"connecting", "1", "2"}, []string{string(s.Status), a.generation(), state()[0]})
	if state()[1] == before[1] {
		t.Error("the login was not replaced")
	}
	a.setStatus(id, "removing")
	_, err = replace(Credentials{AccessToken: "x"})
	refuses(t, "replacing the login of a supplier being removed", err, ErrStatusLocked)
}

// TestUnitValue: a value above 0 and at most 1000 is kept as the latest, the earlier ones stay; another is refused.
func TestUnitValue(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	confirm := func(value string) (Supplier, error) {
		return inTx(a, func(ctx context.Context, tx pg.Tx) (Supplier, error) {
			return a.admin.ConfirmUnitValue(ctx, tx, a.actor, id, value, "read from the console")
		})
	}
	for _, good := range []string{"7.3", "1000", "0.000000000000001"} {
		s, err := confirm(good)
		refuses(t, good, err, nil)
		same(t, "the unit value", good, s.UnitValue)
	}
	for _, bad := range []string{"0", "0.0", "1000.000000000000001", "1000.1", "-1", "1e3", "1/2", "", "1.1234567890123456", "01"} {
		_, err := confirm(bad)
		refuses(t, "the value "+bad, err, ErrUnitValue)
	}
	same(t, "values kept", "3", a.text(`SELECT count(*)::text FROM suppliers.unit_values`))
	a.setStatus(id, "removed")
	_, err := confirm("5")
	refuses(t, "a value for a removed supplier", err, ErrStatusLocked)
}
