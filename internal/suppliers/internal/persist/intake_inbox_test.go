package persist_test

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/persist"
)

// exec runs statements in the platform scope.
func (c *cluster) exec(statements ...string) {
	c.t.Helper()
	if err := c.open().Tx(c.t.Context(), platform, func(tx pg.Tx) error {
		for _, s := range statements {
			if _, err := tx.Exec(c.t.Context(), s); err != nil {
				return fmt.Errorf("%s: %w", s, err)
			}
		}
		return nil
	}); err != nil {
		c.t.Fatal(err)
	}
}

// rule stores the supplier's intake rule.
func (c *cluster) rule(mode, include, exclude string, rev int) {
	c.t.Helper()
	c.exec(fmt.Sprintf(`INSERT INTO suppliers.intake_rules (supplier_id, mode, include, exclude, rev) VALUES ('%s', '%s', '%s', '%s', %d)
		ON CONFLICT (supplier_id) DO UPDATE SET mode = excluded.mode, include = excluded.include, exclude = excluded.exclude, rev = excluded.rev`,
		c.supplier, mode, include, exclude, rev))
}

// TestTheIntakeRuleChoosesTheEndpoints: with no rule, or `listed`, a name of a model on sale becomes an endpoint, as
// before rules; `known` also takes the name of a draft, which the catalog's snapshot does not hold, by the catalog's own
// recognition, and the name of a draft a group alias stands for; `names` takes only the names its patterns take,
// whatever their case. No rule takes a retired model's name. A group is put in use only when the rule takes one of its
// priced names. (The draft's endpoint serves nothing until the model is listed: gateway's listedModelOf builds routing
// entries of listed models only, and a draft is not in the catalog's snapshot at all.)
func TestTheIntakeRuleChoosesTheEndpoints(t *testing.T) {
	t.Parallel()
	listedGPT := []string{"gpt-6", "gpt-6-mini"}
	cases := []struct {
		name, mode, include, exclude string
		endpoints                    []string
		pending                      []string
		selected                     string
	}{
		{"no rule", "", "", "", []string{"gpt-6 known", "gpt-6-mini known"}, []string{"draft-x", "old-x"}, "true"},
		{"listed", "listed", "{}", "{}", []string{"gpt-6 known", "gpt-6-mini known"}, []string{"draft-x", "old-x"}, "true"},
		{"known", "known", "{}", "{}", []string{"draft-x known", "gpt-6 known", "gpt-6-mini known", "x-alias known"}, []string{"old-x"}, "true"},
		{"names", "names", "{GPT-*}", "{*-mini}", []string{"gpt-6 known"}, []string{"draft-x", "old-x"}, "true"},
		{"names that take nothing", "names", "{claude-*}", "{}", nil, []string{"draft-x", "old-x"}, "false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCluster(t)
			for _, name := range listedGPT {
				c.model(name, catalog.StatusListed)
			}
			c.model("draft-x", catalog.StatusDraft)
			c.alias(uuid.Nil(), "x-alias", c.model("draft-y", catalog.StatusDraft))
			c.model("old-x", catalog.StatusRetired)
			if tc.mode != "" {
				c.rule(tc.mode, tc.include, tc.exclude, 1)
			}
			var models []listedModel
			for _, name := range []string{"draft-x", "gpt-6", "gpt-6-mini", "old-x", "x-alias"} {
				models = append(models, listedModel{name: name, formats: []protocol.Format{protocol.Chat}, plan: &unitPrice})
			}
			c.commitListing(c.acquire(c.node("a")), 1, []listedGroup{{ref: "default", name: "Default", ratio: "1", models: models}})
			same(t, "endpoints", c.column("endpoints", "upstream_name || ' ' || cost_state", "upstream_name"), tc.endpoints)
			same(t, "names to map", c.pending(), tc.pending)
			same(t, "selected", c.column("supplier_groups", "selected::text", "id"), []string{tc.selected})
		})
	}
}

// TestAnEndpointTheRuleStopsTakingLeavesRouting: an endpoint of a name the rule no longer takes keeps its row and its
// schedule and has its cost unknown, with the rule as the reason, and is back when the rule takes it again.
func TestAnEndpointTheRuleStopsTakingLeavesRouting(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-6", catalog.StatusListed)
	c.model("gpt-6-mini", catalog.StatusListed)
	groups := []listedGroup{{ref: "default", name: "Default", ratio: "1", models: []listedModel{
		{name: "gpt-6", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
		{name: "gpt-6-mini", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
	}}}
	s := c.acquire(c.node("a"))
	c.commitListing(s, 1, groups)
	c.rule("names", "{gpt-6}", "{}", 1)
	c.commitListing(s, 2, groups)
	same(t, "the endpoints the rule leaves",
		c.column("endpoints", "upstream_name || ' ' || cost_state || ' ' || coalesce(cost_state_reason, '-')", "upstream_name"),
		[]string{"gpt-6 known -", "gpt-6-mini unknown the supplier's intake rule does not take the model"})
	c.rule("listed", "{}", "{}", 2)
	c.commitListing(s, 3, groups)
	same(t, "the endpoints after", c.column("endpoints", "upstream_name || ' ' || cost_state", "upstream_name"),
		[]string{"gpt-6 known", "gpt-6-mini known"})
	if n := c.count("cost_schedules"); n != 2 {
		t.Errorf("%d schedules, want the two the endpoints had", n)
	}
}

// inbox is the supplier's queue as kind, subject and whether it is ignored.
func (c *cluster) inbox() []string {
	c.t.Helper()
	return c.column("inbox_items", "kind || ' ' || subject || ' ' || (ignored_at IS NOT NULL)::text", "kind, subject")
}

// TestTheQueueFollowsTheSync: a commit that read the whole offer brings the queue in line with the tables, a commit that
// did not leaves it; an observation shows again what a person ignored and still holds, an item whose condition ended is
// deleted, and an observation that finds the same writes nothing.
func TestTheQueueFollowsTheSync(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	c.model("gpt-6", catalog.StatusListed)
	groups := []listedGroup{
		{ref: "default", name: "Default", ratio: "1", models: []listedModel{
			{name: "gpt-6", formats: []protocol.Format{protocol.Chat}, plan: &unitPrice},
			{name: "mystery", formats: []protocol.Format{protocol.Chat}},
		}},
		{ref: "new", name: "New", ratio: "2", models: []listedModel{{name: "mystery", formats: []protocol.Format{protocol.Chat}}}},
	}
	s := c.acquire(c.node("a"))
	commit := func(version int64, offer bool) {
		t.Helper()
		step := listing(version, groups)
		step.OfferRead = offer
		if err := s.Store().Commit(t.Context(), version-1, step); err != nil {
			t.Fatal(err)
		}
	}
	commit(1, false)
	if items := c.inbox(); len(items) != 0 {
		t.Fatalf("a commit that read no offer filled the queue: %v", items)
	}
	commit(2, true)
	var defaultGroup, newGroup uuid.UUID
	c.value(&defaultGroup, "SELECT id FROM suppliers.supplier_groups WHERE upstream_ref = 'default'")
	c.value(&newGroup, "SELECT id FROM suppliers.supplier_groups WHERE upstream_ref = 'new'")
	same(t, "the queue", c.inbox(), []string{
		"group_added " + newGroup.String() + " false",
		"unknown_model " + defaultGroup.String() + "/mystery false",
		"unknown_model " + newGroup.String() + "/mystery false",
	})

	credential := uuid.NewV7()
	c.exec(fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
		wrapped_dek, key_version, status, max_concurrency) VALUES ('%s', '%s', '%s', 'k1', '\x00112233445566778899aabbccddeeff',
		'\x000102030405060708090a0b', '\x01', 1, 'rejected', 4)`, credential, c.supplier, defaultGroup),
		fmt.Sprintf(`UPDATE suppliers.inbox_items SET ignored_at = '%s' WHERE kind = 'group_added'`, t0.Format(time.RFC3339)))
	before := c.marks("inbox_items", "kind || subject")
	commit(3, false)
	same(t, "a commit that read no offer", c.marks("inbox_items", "kind || subject"), before)
	commit(4, true)
	same(t, "the queue after the next read of the offer", c.inbox(), []string{
		"group_added " + newGroup.String() + " false",
		"key_invalid " + credential.String() + " false",
		"unknown_model " + defaultGroup.String() + "/mystery false",
		"unknown_model " + newGroup.String() + "/mystery false",
	})
	before = c.marks("inbox_items", "kind || subject")
	commit(5, true)
	same(t, "an observation that finds the same", c.marks("inbox_items", "kind || subject"), before)

	c.exec("UPDATE suppliers.credentials SET status = 'active' WHERE id = '"+credential.String()+"'",
		"UPDATE suppliers.supplier_groups SET selected = true, codename = 'abcdefgh' WHERE upstream_ref = 'new'")
	commit(6, true)
	same(t, "the queue once the conditions ended", c.inbox(), []string{
		"unknown_model " + defaultGroup.String() + "/mystery false",
		"unknown_model " + newGroup.String() + "/mystery false",
	})
}

// TestTheQueueSeesMoney: a selected group whose ratio changed in the last week, a balance under the line with the days
// it lasts at the last day's spend, and a day whose matched charges differ from their estimates by more than the
// reconciliation's tolerance (1%) are queued; a day exactly at the tolerance is not.
func TestTheQueueSeesMoney(t *testing.T) {
	t.Parallel()
	c := newCluster(t)
	now := t0
	group, credential, endpoint, schedule := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	day := func(back int, hour int) string {
		return now.Truncate(24*time.Hour).AddDate(0, 0, -back).Add(time.Duration(hour) * time.Hour).Format(time.RFC3339)
	}
	statements := []string{
		fmt.Sprintf(`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, present, selected, codename)
			VALUES ('%s', '%s', 'vip', 'VIP', 1.2, true, true, 'abcdefgh')`, group, c.supplier),
		fmt.Sprintf(`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, status, max_concurrency)
			VALUES ('%s', '%s', '%s', 'k', 'binding', 1)`, credential, c.supplier, group),
		fmt.Sprintf(`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
			VALUES ('%s', '%s', '%s', gen_random_uuid(), 'gpt', '{chat}')`, endpoint, c.supplier, group),
		fmt.Sprintf(`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
			VALUES ('%s', '%s', '%s', 'quota', '{}', 'published', 'v1')`, schedule, c.supplier, endpoint),
		fmt.Sprintf(`INSERT INTO suppliers.sync_events (supplier_id, version, seq, kind, subject, old_value, new_value, why, created_at)
			VALUES ('%[1]s', 1, 0, 'ratio_changed', 'vip', '1', '1.1', '', '%[2]s'), ('%[1]s', 2, 0, 'ratio_changed', 'vip', '1.1', '1.2', '', '%[3]s')`,
			c.supplier, day(10, 0), day(1, 0)),
		fmt.Sprintf(`UPDATE suppliers.suppliers SET status = 'low_balance', balance_native = 250 WHERE id = '%s'`, c.supplier),
	}
	// Two days of one matched charge each: 101 against 100 is exactly the tolerance, 101.01 is above it. Each attempt
	// is also the last day's spend the balance is divided by when it is in the last day.
	for i, d := range []struct {
		back            int
		estimate, price string
	}{{1, "100", "101"}, {2, "100", "101.01"}} {
		request := uuid.NewV7()
		statements = append(statements,
			fmt.Sprintf(`INSERT INTO suppliers.attempt_costs (request_id, attempt_seq, supplier_id, credential_id, endpoint_id,
				cost_schedule_id, charge_ref, usage, priced_at, finished_at, cost_flag, estimated_native, state)
				VALUES ('%s', 1, '%s', '%s', '%s', '%s', 'r%d', '{}', '%s', '%s', 'complete', %s, 'pending')`,
				request, c.supplier, credential, endpoint, schedule, i, day(d.back, 13), day(d.back, 13), d.estimate),
			fmt.Sprintf(`INSERT INTO suppliers.charge_records (supplier_id, charge_ref, occurred_at, amount_native, usage, funding,
				upstream_model, state, attribution, attempt_request_id, attempt_seq)
				VALUES ('%s', 'r%d', '%s', %s, '{}', 'wallet', 'gpt', 'matched', 'attempt', '%s', 1)`,
				c.supplier, i, day(d.back, 13), d.price, request))
	}
	c.exec(statements...)
	if err := c.open().Tx(t.Context(), platform, func(tx pg.Tx) error {
		return persist.ObserveInbox(t.Context(), tx, c.supplier, now, persist.InboxEverything, true)
	}); err != nil {
		t.Fatal(err)
	}
	same(t, "the queue", c.column("inbox_items", "kind || ' ' || subject || ' ' || detail::text", "kind, subject"), []string{
		"cost_mismatch " + day(2, 0)[:10] + ` {"date": "` + day(2, 0)[:10] + `", "charged_native": "101.01", "estimated_native": "100"}`,
		`low_balance balance {"balance_days": 2, "balance_native": "250"}`,
		"ratio_changed " + group.String() + ` {"ratio": "1.2", "group_id": "` + group.String() + `", "previous_ratio": "1.1"}`,
	})
}
