package suppliers

import (
	"context"
	"fmt"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// bindKey binds one of the supplier's keys to a selected group, as the admin does.
func (a admins) bindKey(supplier, group uuid.UUID, ref string) Credential {
	a.t.Helper()
	c, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Credential, error) {
		return a.admin.BindKey(ctx, tx, a.actor, supplier, group, ref, "a key made at the supplier")
	})
	if err != nil {
		a.t.Fatal(err)
	}
	return c
}

// limitsOf sets a credential's limits, as the handler does.
func (a admins) limitsOf(supplier, credential uuid.UUID, limits Limits) (Credential, error) {
	return inTx(a, func(ctx context.Context, tx pg.Tx) (Credential, error) {
		return a.admin.SetLimits(ctx, tx, a.actor, supplier, credential, limits, "the key allows more")
	})
}

// TestSetLimits: the calls a key serves at once and the requests its minute admits are set together, kept as asked and
// recorded once; asking for the limits it already has writes nothing and records nothing; a minute can be cleared.
func TestSetLimits(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	if _, err := a.choose(id, group, true); err != nil {
		t.Fatal(err)
	}
	bound := a.bindKey(id, group, "key-7")
	same(t, "the limits a new binding has", []any{defaultMaxConcurrency, (*int)(nil)}, []any{bound.MaxConcurrency, bound.RequestsPerMinute})
	generation := a.generation()
	minute := 60
	changed, err := a.limitsOf(id, bound.ID, Limits{MaxConcurrency: 8, RequestsPerMinute: &minute})
	refuses(t, "both limits", err, nil)
	same(t, "the credential", Credential{
		ID: bound.ID, Group: group, UpstreamKeyRef: "key-7", Status: "binding", MaxConcurrency: 8, RequestsPerMinute: &minute,
	}, changed)
	same(t, "the row", "8|60",
		a.text(fmt.Sprintf(`SELECT max_concurrency || '|' || requests_per_minute FROM suppliers.credentials WHERE id = '%s'`, bound.ID)))
	// jsonb orders an object's members by length and then by bytes, so the entry is read member by member.
	same(t, "the audit entry", "credential.limits|the key allows more|"+
		"credential,max_concurrency_from,max_concurrency_to,requests_per_minute_from,requests_per_minute_to",
		a.text(`SELECT action || '|' || reason || '|' || (SELECT string_agg(k, ',' ORDER BY k) FROM jsonb_object_keys(detail) k)
			FROM platform.audit_log WHERE action = 'credential.limits'`))
	same(t, "the values recorded", fmt.Sprintf("%s|4|8||60", bound.ID), a.text(
		`SELECT (detail->>'credential') || '|' || (detail->>'max_concurrency_from') || '|' || (detail->>'max_concurrency_to') ||
			'|' || coalesce(detail->>'requests_per_minute_from', '') || '|' || (detail->>'requests_per_minute_to')
		 FROM platform.audit_log WHERE action = 'credential.limits'`))
	again, err := a.limitsOf(id, bound.ID, Limits{MaxConcurrency: 8, RequestsPerMinute: &minute})
	refuses(t, "the limits it already has", err, nil)
	same(t, "the credential unchanged", changed, again)
	same(t, "audit entries", "1", a.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'credential.limits'`))
	cleared, err := a.limitsOf(id, bound.ID, Limits{MaxConcurrency: 8})
	refuses(t, "clearing the minute", err, nil)
	same(t, "no ceiling of ours", (*int)(nil), cleared.RequestsPerMinute)
	same(t, "the row cleared", "8|",
		a.text(fmt.Sprintf(`SELECT max_concurrency || '|' || coalesce(requests_per_minute::text, '') FROM suppliers.credentials WHERE id = '%s'`, bound.ID)))
	same(t, "the clearing recorded", "60|null", a.text(
		`SELECT (detail->>'requests_per_minute_from') || '|' || coalesce(detail->>'requests_per_minute_to', 'null')
		 FROM platform.audit_log WHERE action = 'credential.limits' ORDER BY id DESC LIMIT 1`))
	largest := MaxRequestsPerMinute
	atCap, err := a.limitsOf(id, bound.ID, Limits{MaxConcurrency: 8, RequestsPerMinute: &largest})
	refuses(t, "the largest requests a minute", err, nil)
	same(t, "the largest requests a minute", &largest, atCap.RequestsPerMinute)
	same(t, "the configuration generation", generation, a.generation())
}

// TestLimitsAboveTheCapReadAsTheCap: an old node can still write a higher value during a rolling release. The new code
// must not treat it as unbounded, in the call path or in what the admin shows.
func TestLimitsAboveTheCapReadAsTheCap(t *testing.T) {
	t.Parallel()
	over := int32(MaxRequestsPerMinute + 1)
	if got := ceiling(&over); got != MaxRequestsPerMinute {
		t.Errorf("the call path read %d, want the cap %d", got, MaxRequestsPerMinute)
	}
	shown := limited(&over)
	if shown == nil || *shown != MaxRequestsPerMinute {
		t.Errorf("the admin shows %v, want the cap %d", shown, MaxRequestsPerMinute)
	}
}

// TestSetLimitsRefusals: a credential the supplier does not have, one of another supplier, a supplier on its way out,
// and numbers the columns cannot hold are refused, and nothing is recorded.
func TestSetLimitsRefusals(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	if _, err := a.choose(id, group, true); err != nil {
		t.Fatal(err)
	}
	bound := a.bindKey(id, group, "key-7")
	_, err := a.limitsOf(id, uuid.NewV7(), Limits{MaxConcurrency: 2})
	refuses(t, "a credential the supplier lacks", err, ErrNoCredential)
	other := a.connect("https://other.example.com", "ops@example.com")
	_, err = a.limitsOf(other.ID, bound.ID, Limits{MaxConcurrency: 2})
	refuses(t, "a credential of another supplier", err, ErrNoCredential)
	per := func(n int) *int { return &n }
	for _, c := range []struct {
		name   string
		limits Limits
	}{
		{"no concurrency", Limits{MaxConcurrency: 0}},
		{"a negative concurrency", Limits{MaxConcurrency: -1}},
		{"a concurrency the column cannot hold", Limits{MaxConcurrency: maxLimit + 1}},
		{"no requests a minute", Limits{MaxConcurrency: 2, RequestsPerMinute: per(0)}},
		{"a negative requests a minute", Limits{MaxConcurrency: 2, RequestsPerMinute: per(-1)}},
		{"a requests a minute above the cap", Limits{MaxConcurrency: 2, RequestsPerMinute: per(MaxRequestsPerMinute + 1)}},
		{"a requests a minute the column cannot hold", Limits{MaxConcurrency: 2, RequestsPerMinute: per(maxLimit + 1)}},
	} {
		_, err := a.limitsOf(id, bound.ID, c.limits)
		refuses(t, c.name, err, ErrLimits)
	}
	a.sql(fmt.Sprintf(`UPDATE suppliers.credentials SET status = 'erased' WHERE id = '%s'`, bound.ID))
	for _, concurrency := range []int{defaultMaxConcurrency, 2} {
		_, err := a.limitsOf(id, bound.ID, Limits{MaxConcurrency: concurrency})
		refuses(t, "an erased credential", err, ErrStatusLocked)
	}
	same(t, "the erased credential's limits", "4", a.text(fmt.Sprintf(
		`SELECT max_concurrency::text FROM suppliers.credentials WHERE id = '%s'`, bound.ID)))
	a.setStatus(id, "removing")
	_, err = a.limitsOf(id, bound.ID, Limits{MaxConcurrency: 2})
	refuses(t, "a supplier being removed", err, ErrStatusLocked)
	same(t, "audit entries", "0", a.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'credential.limits'`))
}

func TestSetLimitsRollsBackWithoutAudit(t *testing.T) {
	t.Parallel()
	a := newAdmins(t)
	id := a.connect("https://api.example.com", "ops@example.com").ID
	group := a.group(id, "1", true)
	if _, err := a.choose(id, group, true); err != nil {
		t.Fatal(err)
	}
	bound := a.bindKey(id, group, "key-7")
	_, err := inTx(a, func(ctx context.Context, tx pg.Tx) (Credential, error) {
		return a.admin.SetLimits(ctx, tx, audit.Actor{}, id, bound.ID, Limits{MaxConcurrency: 8}, "r")
	})
	if err == nil {
		t.Fatal("a change without a valid audit actor succeeded")
	}
	same(t, "the rolled-back concurrency", "4", a.text(fmt.Sprintf(
		`SELECT max_concurrency::text FROM suppliers.credentials WHERE id = '%s'`, bound.ID)))
	same(t, "audit entries", "0", a.text(`SELECT count(*)::text FROM platform.audit_log WHERE action = 'credential.limits'`))
}
