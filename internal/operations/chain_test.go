package operations_test

import (
	"context"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/operations"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// backdated appends an audit entry whose transaction began ago, as only a superuser can date one, and returns its ID.
func (db opsDB) backdated(t *testing.T, ago string) uuid.UUID {
	t.Helper()
	id := uuid.NewV7()
	if err := pgtest.Run(t.Context(), db.superuser, `INSERT INTO platform.audit_log (id, at, actor_kind, actor_id, action,
		module, target_kind, target_id, detail) VALUES ('`+id.String()+`', now() - interval '`+ago+`', 'system',
		'test.chain', 'thing.touch', 'operations', 'thing', 't1', '{"n": 1}')`); err != nil {
		t.Fatal(err)
	}
	return id
}

// record appends an audit entry as the application does, in a transaction of its own.
func (db opsDB) record(t *testing.T) {
	t.Helper()
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { return touch(t.Context(), tx) }); err != nil {
		t.Fatal(err)
	}
}

func touch(ctx context.Context, tx pg.Tx) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: audit.System("test.chain"), Action: "thing.touch",
		Target: audit.Target{Module: "operations", Kind: "thing", ID: "t1"}, Detail: map[string]any{"n": 1},
	})
}

func (db opsDB) chain(t *testing.T, settle time.Duration) int {
	t.Helper()
	n, err := operations.ChainAuditAfter(t.Context(), db.DB, platformScope, settle)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (db opsDB) verifyChain(t *testing.T) bool {
	t.Helper()
	halted, err := operations.VerifyAuditChain(t.Context(), db.DB, platformScope)
	if err != nil {
		t.Fatal(err)
	}
	return halted
}

// linked is the entries of the chain in its order.
func (db opsDB) linked(t *testing.T) string {
	t.Helper()
	return db.text(t, `SELECT coalesce(string_agg(entry_id::text, ',' ORDER BY seq), '') FROM operations.audit_chain`)
}

func ids(of ...uuid.UUID) string {
	s := make([]string, len(of))
	for i, id := range of {
		s[i] = id.String()
	}
	return strings.Join(s, ",")
}

// TestAuditChainLinksCommittedEntries: the entries are linked in the order they began, those that began less than the
// settling time ago only once it has passed, each once, and the chain checks out; the table takes only a link that
// follows the head, and nothing changes one.
func TestAuditChainLinksCommittedEntries(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	second := db.backdated(t, "4 minutes")
	first := db.backdated(t, "5 minutes")
	third := db.backdated(t, "3 minutes")
	db.record(t)
	if n := db.chain(t, time.Minute); n != 3 || db.linked(t) != ids(first, second, third) {
		t.Errorf("linked %d: %s", n, db.linked(t))
	}
	if n := db.chain(t, time.Minute); n != 0 {
		t.Errorf("linked again %d", n)
	}
	if n := db.chain(t, 0); n != 1 || db.verifyChain(t) {
		t.Errorf("linked the settled entry %d, or the chain does not check out", n)
	}
	for name, statement := range map[string]string{
		"a link out of the chain": `INSERT INTO operations.audit_chain (seq, entry_id, at, prev_hash, hash) VALUES (5, uuidv7(), now(),
			'\x` + strings.Repeat("00", 32) + `', '\x` + strings.Repeat("11", 32) + `')`,
		"a link changed":  `UPDATE operations.audit_chain SET hash = prev_hash`,
		"a fault removed": `DELETE FROM operations.audit_chain_faults`,
	} {
		if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error { _, err := tx.Exec(t.Context(), statement); return err }); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestAuditChainWaitsForOpenTransactions: an entry that commits after a transaction that began before it and is still
// open is not linked until that one ends, so the older entry comes first and nothing is found late.
func TestAuditChainWaitsForOpenTransactions(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
			if err := touch(t.Context(), tx); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	db.record(t)
	n := db.chain(t, 0)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("linked %d behind an open transaction", n)
	}
	want := db.text(t, `SELECT string_agg(id::text, ',' ORDER BY at, id) FROM platform.audit_log`)
	if n := db.chain(t, 0); n != 2 || db.linked(t) != want || db.verifyChain(t) {
		t.Errorf("linked %d: %s, want %s", n, db.linked(t), want)
	}
}

// haltCase is a way to break the chain: tamper changes a database of three linked entries, as only a superuser can,
// given the second, and returns the entry the fault names; fault is the fault's kind and seq.
type haltCase struct {
	tamper func(t *testing.T, db opsDB, second uuid.UUID) uuid.UUID
	fault  string
}

func superuser(t *testing.T, db opsDB, statement string) {
	t.Helper()
	if err := pgtest.Run(t.Context(), db.superuser, statement); err != nil {
		t.Fatal(err)
	}
}

// TestAuditChainHalts: an entry slipped in behind the head, one removed, one changed and a link changed are each found
// by the next run, recorded once with its audit entry and alert, and stop the chaining until the owner acts.
func TestAuditChainHalts(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]haltCase{
		"late": {func(t *testing.T, db opsDB, _ uuid.UUID) uuid.UUID { return db.backdated(t, "270 seconds") }, "late:2"},
		"missing": {func(t *testing.T, db opsDB, second uuid.UUID) uuid.UUID {
			superuser(t, db, `DELETE FROM platform.audit_log WHERE id = '`+second.String()+`'`)
			return second
		}, "missing:2"},
		"mismatch": {func(t *testing.T, db opsDB, second uuid.UUID) uuid.UUID {
			superuser(t, db, `UPDATE platform.audit_log SET detail = '{"n": 2}' WHERE id = '`+second.String()+`'`)
			return second
		}, "mismatch:2"},
		"broken": {func(t *testing.T, db opsDB, _ uuid.UUID) uuid.UUID {
			superuser(t, db, `UPDATE operations.audit_chain SET prev_hash = hash WHERE seq = 3`)
			return uuid.MustParse(db.text(t, `SELECT entry_id::text FROM operations.audit_chain WHERE seq = 3`))
		}, "broken:3"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c.expect(t)
		})
	}
}

func (c haltCase) expect(t *testing.T) {
	db := openOps(t)
	db.backdated(t, "5 minutes")
	second := db.backdated(t, "4 minutes")
	db.backdated(t, "3 minutes")
	if n := db.chain(t, time.Minute); n != 3 {
		t.Fatalf("linked %d", n)
	}
	entry := c.tamper(t, db, second)
	db.backdated(t, "2 minutes")
	for range 2 {
		if n := db.chain(t, time.Minute); n != 0 {
			t.Errorf("linked %d after the fault", n)
		}
	}
	db.expect(t, "the fault", `SELECT string_agg(kind || ':' || seq || ':' || entry_id, ',') FROM operations.audit_chain_faults`,
		c.fault+":"+entry.String())
	db.expect(t, "the record", `SELECT count(*) || ' ' || string_agg(actor_kind || ':' || actor_id || ' ' || (detail->>'fault'), ',')
		FROM platform.audit_log WHERE action = 'audit_chain.halt'`, "1 system:operations.audit_chain "+strings.Split(c.fault, ":")[0])
	db.expect(t, "the alert", `SELECT count(*)::text FROM platform.alert_signals
		WHERE body->>'kind' = 'operations.audit_chain_fault' AND body->>'severity' = 'critical'`, "1")
	if !db.verifyChain(t) {
		t.Error("the check runs on a halted chain")
	}
}

// TestAuditChainChecksBeyondTheWindow: a change to an entry further behind the head than each run looks is found by
// the check of the whole chain, which pages through it.
func TestAuditChainChecksBeyondTheWindow(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	if err := pgtest.Run(t.Context(), db.superuser, `INSERT INTO platform.audit_log (at, actor_kind, actor_id, action, module,
		target_kind, target_id, detail) SELECT now() - interval '10 minutes' + g * interval '1 millisecond', 'system',
		'test.chain', 'thing.touch', 'operations', 'thing', 't' || g, '{}' FROM generate_series(1, 1001) g`); err != nil {
		t.Fatal(err)
	}
	if n := db.chain(t, time.Minute); n != 1001 || db.verifyChain(t) {
		t.Fatalf("linked %d, or the chain does not check out", n)
	}
	if err := pgtest.Run(t.Context(), db.superuser, `UPDATE platform.audit_log SET target_id = 'x' WHERE target_id = 't1'`); err != nil {
		t.Fatal(err)
	}
	db.backdated(t, "2 minutes")
	if n := db.chain(t, time.Minute); n != 1 {
		t.Errorf("a run linked %d", n)
	}
	if !db.verifyChain(t) {
		t.Error("the check passed a changed entry")
	}
	db.expect(t, "the fault", `SELECT string_agg(kind || ':' || seq, ',') FROM operations.audit_chain_faults`, "mismatch:1")
}

// TestAuditChainLag: an entry not linked a minute past the settling time raises the lag, which the run that links it
// resolves.
func TestAuditChainLag(t *testing.T) {
	t.Parallel()
	db := openOps(t)
	db.backdated(t, "5 minutes")
	if err := db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		return operations.SignalChainLag(t.Context(), tx, time.Minute)
	}); err != nil {
		t.Fatal(err)
	}
	db.expect(t, "the lag", `SELECT (body->>'severity') || ' ' || ((body->'fields'->>'behind_seconds')::int >= 300)::text
		FROM platform.alert_signals WHERE body->>'kind' = 'operations.audit_chain_lag'`, "warning true")
	if n := db.chain(t, time.Minute); n != 1 {
		t.Errorf("linked %d", n)
	}
	db.expect(t, "the recovery", `SELECT count(*)::text FROM platform.alert_signals
		WHERE body->>'kind' = 'operations.audit_chain_lag' AND (body->>'resolved')::bool`, "1")
}
