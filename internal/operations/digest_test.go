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

// The digests' tests run on a fixed clock, whatever the time they run at: today is 2026-11-08, the job runs at noon,
// and everything they write is dated by these days, never by the wall clock.
var (
	today   = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	noon    = today.Add(12 * time.Hour)
	settled = noon.Add(-time.Minute) // every transaction begun before it has ended
)

// day is the date days from today, as a digest names it.
func day(days int) string { return today.AddDate(0, 0, days).Format(time.DateOnly) }

// digestAt runs the job once with the clock at now and the frontier at frontier, and returns how many it made.
func (db opsDB) digestAt(t *testing.T, now, frontier time.Time) int {
	t.Helper()
	made, err := operations.MakeAuditDigestsAt(t.Context(), db.DB, platformScope, now, frontier)
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

// seedYesterday writes what the digest of 2026-11-07 counts, and beside it what it must leave out: an operation and
// its approval proposed that day and one the next, the operations calls of that day (refused, succeeded, failed) and of
// the days around it, and a cap policy with that day's and the next day's counters.
func (db opsDB) seedYesterday(t *testing.T) {
	t.Helper()
	if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, true)); err != nil {
		t.Fatal(err)
	}
	superuser(t, db, `UPDATE operations.operations SET created_at = '2026-11-07 10:00+00';
		UPDATE operations.approvals SET requested_at = '2026-11-07 10:00+00', expires_at = '2026-11-07 10:30+00'`)
	if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, false)); err != nil {
		t.Fatal(err)
	}
	superuser(t, db, `UPDATE operations.operations SET created_at = '2026-11-08 09:00+00' WHERE created_at > '2026-11-08';
		INSERT INTO operations.operation_log (id, credential_id, tool, reason, input, status, error_code,
		request_id, started_at, ended_at, logged_at) SELECT uuidv7(d.at - now()), uuidv7(), 'model_get', '', '{}', d.status,
		d.code, 'r', d.at, d.at, d.at FROM (VALUES ('2026-11-07 10:00+00'::timestamptz, 403, 'forbidden'), ('2026-11-07 11:00+00', 403, 'forbidden'),
		('2026-11-07 12:00+00', 400, NULL), ('2026-11-07 13:00+00', 200, NULL), ('2026-11-07 14:00+00', 500, 'internal'),
		('2026-11-08 10:00+00', 403, 'forbidden'), ('2026-11-06 23:59:59+00', 403, 'forbidden')) d (at, status, code);
		INSERT INTO operations.cap_policies (category, currency, single_max, daily_max, weekly_max, object_daily_max,
		daily_count_max, hard_single_max, hard_daily_max, hard_weekly_max, rev)
		VALUES ('money.top_up', 'USD', 10, 100, 500, 50, 5, 20, 200, 900, 1);
		INSERT INTO operations.cap_counters (category, window_kind, window_start, reserved, committed, count)
		VALUES ('money.top_up', 'day', '2026-11-07 00:00+00', 30, 20, 2), ('money.top_up', 'day', '2026-11-08 00:00+00', 7, 0, 1)`)
}

// TestAuditDigest: the digest of 2026-11-07 counts what happened in that UTC day alone (operations by tool and state,
// approvals, refused calls, each cap policy's day) with the chain as it is, is dated by the clock, and is made and
// signalled once however often the job runs.
func TestAuditDigest(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.seedYesterday(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)

	if db.digestAt(t, noon, settled) != 1 || db.digestAt(t, noon, settled) != 0 {
		t.Fatal("the digest was not made exactly once")
	}
	digests, more := db.digests(t, nil, 5)
	if len(digests) != 1 || more {
		t.Fatalf("%d digests, more %v", len(digests), more)
	}
	d := digests[0]
	if d.Day != day(-1) || !d.MadeAt.Equal(noon) {
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
		`{"code":"forbidden","count":2}]}`
	if string(got) != want {
		t.Errorf("digest\n%s\nwant\n%s", got, want)
	}
	db.expect(t, "the signal", `SELECT string_agg((body->>'key') || ' ' || (body->>'severity') || ' ' || (body->'fields'->>'operations') || ' ' ||
		(body->'fields'->>'refused') || ' ' || (body->'fields'->>'money_proposals'), ',')
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_digest'`, day(-1)+" info 1 3 2")
}

// TestAuditDigestDatesCallsByTheDatabase: a call counts in the day the database logged it, the clock the digest's
// boundary is on, not the day of its ID: a node whose clock runs 90 seconds behind logs a refusal at 00:01:10 with an
// ID of 23:59:40, after yesterday's digest was made; it is in today's, not lost.
func TestAuditDigestDatesCallsByTheDatabase(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		id, err := operations.RecordCall(t.Context(), tx, operations.LoggedCall{
			Credential: uuid.NewV7(), Tool: "whoami", Status: 200, RequestID: "r", Started: noon, Ended: noon,
		})
		if err != nil {
			return err
		}
		var now bool
		if err := tx.QueryRow(t.Context(), `SELECT logged_at = now() FROM operations.operation_log WHERE id = $1`, id).Scan(&now); err != nil || !now {
			t.Errorf("a recorded call is not dated by its transaction's start: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if db.digestAt(t, noon, settled) != 1 {
		t.Fatal("yesterday's digest was not made")
	}
	superuser(t, db, `INSERT INTO operations.operation_log (id, credential_id, tool, reason, input, status, error_code,
		request_id, started_at, ended_at, logged_at) SELECT uuidv7('2026-11-07 23:59:40+00' - now()), uuidv7(), 'model_get',
		'', '{}', 403, 'forbidden', 'r', '2026-11-07 23:59:39+00', '2026-11-07 23:59:40+00', '2026-11-08 00:01:10+00'`)
	if db.digestAt(t, noon.Add(24*time.Hour), settled.Add(24*time.Hour)) != 1 {
		t.Fatal("today's digest was not made")
	}
	digests, _ := db.digests(t, nil, 2)
	refused := map[string][]operations.DigestRefusal{}
	for _, d := range digests {
		refused[d.Day] = d.Refused
	}
	if len(refused[day(-1)]) != 0 || len(refused[day(0)]) != 1 || refused[day(0)][0] != (operations.DigestRefusal{Code: "forbidden", Count: 1}) {
		t.Errorf("refused by day %+v", refused)
	}
}

// TestAuditDigestCheckedAt: while a round of the check of the whole chain runs, the digest names when the one before
// it finished.
func TestAuditDigestCheckedAt(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	db.chain(t, time.Minute)
	if db.verifyChain(t) {
		t.Fatal("halted")
	}
	finished := db.text(t, `SELECT finished_at::text FROM operations.audit_chain_checks`)
	superuser(t, db, `UPDATE operations.audit_chain_checks SET started_at = now() - interval '25 hours'`)
	if _, err := operations.VerifyAuditChainBy(t.Context(), db.DB, platformScope, 1); err != nil { // starts the next round alone
		t.Fatal(err)
	}
	db.expect(t, "the round in progress", `SELECT (finished_at IS NULL)::text FROM operations.audit_chain_checks`, "true")
	if db.digestAt(t, noon, settled) != 1 {
		t.Fatal("no digest made")
	}
	digests, _ := db.digests(t, nil, 1)
	want, err := time.Parse("2006-01-02 15:04:05.999999-07", finished)
	if err != nil {
		t.Fatal(err)
	}
	if c := digests[0].Chain.CheckedAt; c == nil || !c.Equal(want) {
		t.Errorf("checked at %v, want %s", c, finished)
	}
}

// TestAuditDigestPages: the digests read back newest first, a page at a time from the day the last page ended on.
func TestAuditDigestPages(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if db.digestAt(t, noon, settled) != 1 {
		t.Fatal("no digest made")
	}
	superuser(t, db, `INSERT INTO operations.audit_digests (day, body) SELECT day - g, jsonb_set(body, '{day}',
		to_jsonb((day - g)::text)) FROM operations.audit_digests, generate_series(1, 2) g`)
	page, more := db.digests(t, nil, 2)
	if len(page) != 2 || !more || page[0].Day != day(-1) {
		t.Fatalf("first page %d, more %v", len(page), more)
	}
	before, err := time.Parse(time.DateOnly, page[1].Day)
	if err != nil {
		t.Fatal(err)
	}
	last, more := db.digests(t, &before, 2)
	if len(last) != 1 || more || last[0].Day != day(-3) {
		t.Errorf("last page %+v, more %v", last, more)
	}
}

// digestDays is one row of TestAuditDigestDays: what happened before the job ran, how many runs to make at once (at
// noon, every transaction ended), and the digests and signals it must have left: the days by their offset from today,
// each with how many operations of the day it counted.
type digestDays struct {
	before func(t *testing.T, db opsDB)
	runs   int
	days   map[int]int
	large  bool // a digest's body is over 64 KiB
}

// TestAuditDigestDays: the digests follow docs/domains/operations.md「每天的摘要现在的实现」 1 and 2: each UTC day once,
// in order from the day after the last, after the day's last transactions, with every write committed into it, each
// announced by one signal keyed by its day, however large, whatever ran before or races beside it.
func TestAuditDigestDays(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]digestDays{
		"the first run makes yesterday": {runs: 1, days: map[int]int{-1: 0}},
		"midnight between commit and digest": {before: func(t *testing.T, db opsDB) {
			// Half a minute past midnight a transaction begun at 23:59:59 is still open: the frontier is before the
			// day's end, so the day is not made.
			if n := db.digestAt(t, today.Add(30*time.Second), today.Add(-time.Second)); n != 0 {
				t.Fatalf("made %d before the day's transactions ended", n)
			}
			// It commits after midnight with the day's time; the next run, the frontier past midnight, counts it.
			if _, _, err := db.propose(t, retire(uuid.NewV7(), uuid.NewV7(), 1, false)); err != nil {
				t.Fatal(err)
			}
			superuser(t, db, `UPDATE operations.operations SET created_at = '2026-11-07 23:59:59+00'`)
		}, runs: 1, days: map[int]int{-1: 1}},
		"the job down for days": {before: func(t *testing.T, db opsDB) {
			superuser(t, db, `INSERT INTO operations.audit_digests (day, body) VALUES ('2026-11-04', '{"day": "2026-11-04"}')`)
		}, runs: 1, days: map[int]int{-3: 0, -2: 0, -1: 0}},
		"a digest over 64 KiB": {before: func(t *testing.T, db opsDB) {
			superuser(t, db, `INSERT INTO operations.audit_digests (day, body) VALUES ('2026-11-05', '{"day": "2026-11-05"}');
				INSERT INTO operations.cap_policies (site_id, category, currency, single_max, daily_max, weekly_max,
				object_daily_max, daily_count_max, hard_single_max, hard_daily_max, hard_weekly_max, rev)
				SELECT uuidv7(), 'money.top_up', 'USD', 10, 100, 500, 50, 5, 20, 200, 900, 1 FROM generate_series(1, 400)`)
		}, runs: 1, days: map[int]int{-2: 0, -1: 0}, large: true},
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
	db.digestsAtOnce(t, c.runs)
	if n := db.digestAt(t, noon, settled); n != 0 {
		t.Errorf("a later run made %d", n)
	}
	digests, _ := db.digests(t, nil, 10)
	counted := map[string]int{}
	for _, d := range digests {
		counted[d.Day] = len(d.Operations)
	}
	var want []string
	for _, offset := range slices.Sorted(maps.Keys(c.days)) {
		want = append(want, day(offset)+":false")
		if got, ok := counted[day(offset)]; !ok || got != c.days[offset] {
			t.Errorf("%s counted %d operations (made %v), want %d", day(offset), got, ok, c.days[offset])
		}
	}
	if got := db.digestSignals(t); got != strings.Join(want, ",") {
		t.Errorf("signals %s, want %s", got, strings.Join(want, ","))
	}
	if got := len(digests); strconv.Itoa(got-len(c.days)) != seeded {
		t.Errorf("%d digests beside the %s seeded, want %d", got, seeded, len(c.days))
	}
	if c.large {
		db.expect(t, "the largest body", `SELECT (max(octet_length(body::text)) > 65536)::text FROM operations.audit_digests`, "true")
	}
}

// digestsAtOnce runs the job n times at once at noon, every transaction ended, as n nodes would.
func (db opsDB) digestsAtOnce(t *testing.T, n int) {
	t.Helper()
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := operations.MakeAuditDigestsAt(t.Context(), db.DB, platformScope, noon, settled)
			errs <- err
		}()
	}
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
