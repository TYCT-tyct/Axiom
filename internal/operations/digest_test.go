package operations_test

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// digest runs the job once, with the day's last transactions given settle to end, and returns how many it made.
func (db opsDB) digest(t *testing.T, replicaOn bool, settle time.Duration) int {
	t.Helper()
	made, err := operations.MakeAuditDigestsAfter(t.Context(), db.DB, platformScope, replicaOn, settle)
	if err != nil {
		t.Fatal(err)
	}
	return made
}

// digestSignals are the digest's signals in the order they were raised, each its key and whether it resolves.
func (db opsDB) digestSignals(t *testing.T) string {
	t.Helper()
	return db.text(t, `SELECT coalesce(string_agg((body->>'key') || ':' || coalesce(body->>'resolved', 'false'), ',' ORDER BY id), '')
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_digest'`)
}

// utcDay is the UTC day days from today, as a digest names it.
func utcDay(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format(time.DateOnly) }

func (db opsDB) digests(t *testing.T, before *time.Time, n int) ([]operations.AuditDigest, bool) {
	t.Helper()
	var digests []operations.AuditDigest
	var more bool
	if err := db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) (err error) {
		digests, more, err = operations.AuditDigests(t.Context(), tx, before, n)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return digests, more
}

// seedYesterday writes what the digest of yesterday counts, and beside it what it must leave out: an operation and its
// approval proposed yesterday and one today, the operations calls of yesterday (refused, succeeded, failed) and of today
// and the day before, and a cap policy with yesterday's and today's counters.
func (db opsDB) seedYesterday(t *testing.T) {
	t.Helper()
	if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true)); err != nil {
		t.Fatal(err)
	}
	superuser(t, db, `UPDATE operations.operations SET created_at = created_at - interval '1 day';
		UPDATE operations.approvals SET requested_at = requested_at - interval '1 day', expires_at = expires_at - interval '1 day'`)
	if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, false)); err != nil { // today's
		t.Fatal(err)
	}
	superuser(t, db, `INSERT INTO operations.operation_log (id, credential_id, tool, reason, input, status, error_code,
		request_id, started_at, ended_at) SELECT uuidv7(d.shift), uuidv7(), 'model_get', '', '{}', d.status, d.code, 'r',
		now() + d.shift, now() + d.shift FROM (VALUES (interval '-1 day', 403, 'forbidden'), (interval '-1 day', 403, 'forbidden'),
		(interval '-1 day', 400, NULL), (interval '-1 day', 200, NULL), (interval '-1 day', 500, 'internal'),
		(interval '0', 403, 'forbidden'), (interval '-2 days', 403, 'forbidden')) d (shift, status, code);
		INSERT INTO operations.cap_policies (category, currency, single_max, daily_max, weekly_max, object_daily_max,
		daily_count_max, hard_single_max, hard_daily_max, hard_weekly_max, rev)
		VALUES ('money.top_up', 'USD', 10, 100, 500, 50, 5, 20, 200, 900, 1);
		INSERT INTO operations.cap_counters (category, window_kind, window_start, reserved, committed, count)
		VALUES ('money.top_up', 'day', date_trunc('day', now(), 'UTC') - interval '1 day', 30, 20, 2),
		('money.top_up', 'day', date_trunc('day', now(), 'UTC'), 7, 0, 1)`)
}

// TestAuditDigest: yesterday's digest counts what happened in that UTC day alone (operations by tool and state,
// approvals, refused calls, each cap policy's day) with the chain and its copy as they are, and is made and signalled
// once however often the job runs.
func TestAuditDigest(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.seedYesterday(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)

	if db.digest(t, false, time.Minute) != 1 || db.digest(t, true, time.Minute) != 0 {
		t.Fatal("the digest was not made exactly once")
	}
	digests, more := db.digests(t, nil, 5)
	if len(digests) != 1 || more {
		t.Fatalf("%d digests, more %v", len(digests), more)
	}
	d := digests[0]
	if d.Day != utcDay(-1) || d.MadeAt.IsZero() {
		t.Errorf("day %s made at %s", d.Day, d.MadeAt)
	}
	d.Day, d.MadeAt, d.Chain.HeadAt, d.Chain.CheckedAt = "", time.Time{}, nil, nil
	got, err := jsonx.MarshalCanonical(d)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"approvals":[{"count":1,"state":"pending"}],"chain":{"faults":0,"head_seq":` + db.text(t, `SELECT count(*)::text FROM operations.audit_chain`) +
		`},"day":"","made_at":"0001-01-01T00:00:00Z","money":[{"category":"money.top_up","committed":"20","count":2,` +
		`"currency":"USD","daily_count_max":5,"daily_max":"100","hard_daily_max":"200","reserved":"30","scope":"platform"}],` +
		`"operations":[{"count":1,"state":"pending_approval","tool":"model_set_status"}],"refused":[{"code":"","count":1},` +
		`{"code":"forbidden","count":2}],"replica":{"replicated_seq":0,"state":"unconfigured"}}`
	if string(got) != want {
		t.Errorf("digest\n%s\nwant\n%s", got, want)
	}
	db.expect(t, "the signal", `SELECT string_agg((body->>'severity') || ' ' || (body->'fields'->>'operations') || ' ' ||
		(body->'fields'->>'refused') || ' ' || (body->'fields'->>'money_proposals') || ' ' || (body->'fields'->>'replica'), ',')
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_digest'`, "info 1 3 2 unconfigured")
	if got := db.digestSignals(t); got != utcDay(-1)+":false" {
		t.Errorf("signals %s", got)
	}
}

// TestAuditDigestPages: the digests read back newest first, a page at a time from the day the last page ended on.
func TestAuditDigestPages(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if db.digest(t, false, time.Minute) != 1 {
		t.Fatal("no digest made")
	}
	digests, _ := db.digests(t, nil, 1)
	superuser(t, db, `INSERT INTO operations.audit_digests (day, body) SELECT day - g, jsonb_set(body, '{day}',
		to_jsonb((day - g)::text)) FROM operations.audit_digests, generate_series(1, 2) g`)
	page, more := db.digests(t, nil, 2)
	if len(page) != 2 || !more || page[0].Day != digests[0].Day {
		t.Fatalf("first page %d, more %v", len(page), more)
	}
	before, err := time.Parse(time.DateOnly, page[1].Day)
	if err != nil {
		t.Fatal(err)
	}
	last, more := db.digests(t, &before, 2)
	if len(last) != 1 || more || last[0].Day != before.AddDate(0, 0, -1).Format(time.DateOnly) {
		t.Errorf("last page %+v, more %v", last, more)
	}
}

// digestDays is one row of TestAuditDigestDays: what happened before the job ran (given the database and how many
// runs to make at once), and the digests and signals it must have left: the days by their offset from today, each
// with how many operations of the day it counted.
type digestDays struct {
	before func(t *testing.T, db opsDB)
	runs   int
	days   map[int]int
}

// TestAuditDigestDays: the digests follow docs/domains/operations.md「每天的摘要现在的实现」 1 and 2: each UTC day once,
// in order from the day after the last, after the day's last transactions, with every write committed into it, each
// announced by one signal keyed by its day, whatever ran before or races beside it.
func TestAuditDigestDays(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]digestDays{
		"the first run makes yesterday": {runs: 1, days: map[int]int{-1: 0}},
		"midnight between commit and digest": {before: func(t *testing.T, db opsDB) {
			// A transaction begun before midnight is still open: the frontier has not passed the day, which a
			// settling time longer than the day stands for, so nothing is made yet.
			if n := db.digest(t, false, 48*time.Hour); n != 0 {
				t.Fatalf("made %d before the day's transactions ended", n)
			}
			// It commits after midnight with yesterday's time, as an operation begun at 23:59:59 does.
			if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, false)); err != nil {
				t.Fatal(err)
			}
			superuser(t, db, `UPDATE operations.operations SET created_at = date_trunc('day', now(), 'UTC') - interval '1 second'`)
		}, runs: 1, days: map[int]int{-1: 1}},
		"the job down for days": {before: func(t *testing.T, db opsDB) {
			superuser(t, db, `INSERT INTO operations.audit_digests (day, body) VALUES ((now() AT TIME ZONE 'UTC')::date - 4, '{"day": "-"}')`)
		}, runs: 1, days: map[int]int{-3: 0, -2: 0, -1: 0}},
		"two nodes racing": {runs: 2, days: map[int]int{-1: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c.expect(t)
		})
	}
}

func (c digestDays) expect(t *testing.T) {
	db := openOps(t)
	if c.before != nil {
		c.before(t, db)
	}
	seeded := db.text(t, `SELECT count(*)::text FROM operations.audit_digests`)
	errs := make(chan error, c.runs)
	for range c.runs {
		go func() {
			_, err := operations.MakeAuditDigestsAfter(t.Context(), db.DB, platformScope, false, time.Minute)
			errs <- err
		}()
	}
	for range c.runs {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := db.digest(t, false, time.Minute); n != 0 {
		t.Errorf("a later run made %d", n)
	}
	digests, _ := db.digests(t, nil, 10)
	counted := map[string]int{}
	for _, d := range digests {
		counted[d.Day] = len(d.Operations)
	}
	var want []string
	for _, day := range slices.Sorted(maps.Keys(c.days)) {
		want = append(want, utcDay(day)+":false")
		if got, ok := counted[utcDay(day)]; !ok || got != c.days[day] {
			t.Errorf("%s counted %d operations (made %v), want %d", utcDay(day), got, ok, c.days[day])
		}
	}
	if got := db.digestSignals(t); got != strings.Join(want, ",") {
		t.Errorf("signals %s, want %s", got, strings.Join(want, ","))
	}
	if got := len(digests); strconv.Itoa(got-len(c.days)) != seeded {
		t.Errorf("%d digests beside the %s seeded, want %d", got, seeded, len(c.days))
	}
}
