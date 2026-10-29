package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// The node permits three attempts. A refusal retries the same endpoint with another credential and, once every credential
// of the group rests, moves to the next endpoint; a rate limit or server error moves to another endpoint. None delivered content to the client.
func TestANodeFallsOverUpstreamFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		statuses []int
		fails    bool
	}{
		{"unauthorized", []int{401}, false},
		{"rate limited", []int{429}, false},
		{"internal error", []int{500}, false},
		{"unavailable", []int{503}, false},
		{"credential then endpoint", []int{401, 429}, false},
		{"credentials of the group all rest", []int{401, 401}, false},
		{"all fail", []int{401, 429, 503}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := newCallWorld(t)
			w.seedFailover()
			w.supplier.mu.Lock()
			w.supplier.failures = tc.statuses
			w.supplier.mu.Unlock()
			w.price()
			n := w.start()
			waitUntil(t, 15*time.Second, func() bool { return n.ready(t) })
			r := w.call(n, "/v1/messages", w.richKey, messagesCall)
			count, charged := len(tc.statuses)+1, int64(messagesCharge)
			if tc.fails {
				count, charged = len(tc.statuses), 0
				if r.status != http.StatusBadGateway || !strings.Contains(string(r.body), "upstream_error") {
					t.Fatalf("exhausted attempts: got %d %s, want 502 upstream_error", r.status, r.body)
				}
				w.checkSettled(r, 0)
			} else {
				w.checkFailureSuccess(r, count)
			}
			w.checkFailedAttempts(r, tc.statuses, count, charged)
			if tc.statuses[0] == http.StatusUnauthorized {
				w.checkRestedCredential(n)
			}
		})
	}
}

// failedNative is what an attempt that failed with no usage is estimated at: the request's input at the guessed bytes per
// token, priced at the fixture's input rate. A refusal before execution (401) costs nothing.
const failedNative = "0.0005"

func (w *callWorld) checkFailureSuccess(r reply, count int) {
	w.t.Helper()
	w.served(r, "after upstream failures")
	if !strings.Contains(string(r.body), "Hello!") {
		w.t.Errorf("the successful attempt delivered no recorded content: %s", r.body)
	}
	w.checkBooked(r, booking{
		attempts: count, charge: messagesCharge, cost: w.attemptCost(r),
		ref: "relay-" + strconv.Itoa(count), upstream: "up-claude", input: 25, output: 15,
	})
}

// seedFailover adds a second group serving the same model, with two credentials per group.
// Identical prices make route order immaterial; stored IDs prove which kind of retry occurred.
func (w *callWorld) seedFailover() {
	w.t.Helper()
	group, endpoint, schedule := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		for _, s := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, selected, present, codename)
    SELECT $1, supplier_id, 'fallback', 'fallback', 1, true, true, 'cedar' FROM suppliers.supplier_groups`, []any{group}},
			{`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
    SELECT $1, supplier_id, $2, model_id, upstream_name, formats FROM suppliers.endpoints WHERE upstream_name = 'up-claude'`, []any{endpoint, group}},
			{`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
    SELECT $1, supplier_id, $2, native_unit, schedule, basis, fingerprint FROM suppliers.cost_schedules
    WHERE endpoint_id = (SELECT id FROM suppliers.endpoints WHERE upstream_name = 'up-claude' AND id <> $2)`, []any{schedule, endpoint}},
			{`UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = $1 WHERE id = $2`, []any{schedule, endpoint}},
		} {
			if _, err := tx.Exec(w.t.Context(), s.sql, s.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	original := uuid.MustParse(w.query("SELECT supplier_group_id::text FROM suppliers.credentials"))
	w.addFailureCredential(original, "second")
	w.addFailureCredential(group, "fallback-first")
	w.addFailureCredential(group, "fallback-second")
}

func (w *callWorld) addFailureCredential(group uuid.UUID, name string) {
	w.t.Helper()
	credential := uuid.NewV7()
	sealed := masterKeys(w.t).Seal(secret.New("supplier-"+name), secret.Location{
		Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key",
	})
	if err := w.db.Tx(w.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := tx.Exec(w.t.Context(), `INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref,
   ciphertext, nonce, wrapped_dek, key_version, status, max_concurrency)
   SELECT $1, supplier_id, id, $2, $3, $4, $5, $6, 'active', 4 FROM suppliers.supplier_groups WHERE id = $7`,
			credential, name, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, int64(sealed.KeyVersion), group)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// attemptCost converts the native costs at this fixture's confirmed unit value, independently of the call log.
func (w *callWorld) attemptCost(r reply) int64 {
	w.t.Helper()
	n, err := strconv.ParseInt(w.query(`SELECT (sum(estimated_native) * 1000000)::bigint::text
		FROM suppliers.attempt_costs WHERE request_id::text = $1`, r.header.Get("X-Generation-Id")), 10, 64)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *callWorld) checkFailedAttempts(r reply, statuses []int, count int, charged int64) {
	w.t.Helper()
	id := r.header.Get("X-Generation-Id")
	if got := w.query("SELECT count(*)::text FROM suppliers.attempt_costs WHERE request_id::text = $1", id); got != strconv.Itoa(count) {
		w.t.Errorf("attempt rows = %s, want %d", got, count)
	}
	if got := len(w.supplier.sent()); got != count {
		w.t.Fatalf("upstream calls = %d, want %d", got, count)
	}
	for i, status := range statuses {
		flag, native := "no_usage", failedNative
		if status == http.StatusUnauthorized {
			flag, native = "complete", "0"
		}
		got := w.query(`SELECT cost_flag || ' ' || estimated_native::text || ' ' || charge_ref
			FROM suppliers.attempt_costs WHERE request_id::text = $1 AND attempt_seq = $2`, id, i+1)
		if want := fmt.Sprintf("%s %s relay-%d", flag, native, i+1); got != want {
			w.t.Errorf("attempt %d (%d): cost = %q, want %q", i+1, status, got, want)
		}
		if i+1 < count {
			w.checkFailureTransition(id, i+1, status)
		}
	}
	w.checkAttemptLedger(id)
	if got := w.query(`SELECT balance::text || ' ' || reserved::text FROM billing.balances
		WHERE account_id = (SELECT account_id FROM accounts.users WHERE email = 'rich@example.com')`); got != fmt.Sprintf("%d 0", 10_000_000_000-charged) {
		w.t.Errorf("account balance/reserved = %s, want %d 0", got, 10_000_000_000-charged)
	}
	if got := w.query(`SELECT coalesce(sum(e.amount), 0)::text FROM billing.entries e
		JOIN billing.transactions t ON t.id = e.transaction_id WHERE t.request_id::text = $1 AND e.book = 'user_balance'`, id); got != strconv.FormatInt(charged, 10) {
		w.t.Errorf("user ledger charge = %s, want %d", got, charged)
	}
	w.checkBalanced()
}

// Every supplier row has its own cost in the ledger, including unknown-execution failures.
func (w *callWorld) checkAttemptLedger(id string) {
	w.t.Helper()
	if got := w.query(`SELECT count(*)::text FROM suppliers.attempt_costs c WHERE c.request_id::text = $1 AND
		ceil(c.estimated_native * 1000000) <> coalesce((SELECT sum(e.amount) FROM billing.entries e
		JOIN billing.transactions t ON t.id = e.transaction_id WHERE t.request_id = c.request_id
		AND t.attempt_seq = c.attempt_seq AND e.book = 'supplier_cost'), 0)`, id); got != "0" {
		w.t.Errorf("%s attempts have supplier costs missing or different in the ledger", got)
	}
	if got := w.query(`SELECT count(*)::text FROM billing.entries e JOIN billing.transactions t ON t.id = e.transaction_id
		WHERE t.request_id::text = $1 AND t.kind = 'attempt_cost' AND e.book NOT IN ('supplier_cost', 'supplier_prepaid')`, id); got != "0" {
		w.t.Errorf("%s attempt cost entries touch books other than supplier cost and prepayment", got)
	}
}

func (w *callWorld) checkFailureTransition(id string, seq, status int) {
	w.t.Helper()
	got := w.query(`SELECT (a.endpoint_id = b.endpoint_id)::text || ' ' || (a.credential_id = b.credential_id)::text
		FROM suppliers.attempt_costs a JOIN suppliers.attempt_costs b ON a.request_id = b.request_id AND b.attempt_seq = a.attempt_seq + 1
		WHERE a.request_id::text = $1 AND a.attempt_seq = $2`, id, seq)
	if status == http.StatusUnauthorized && seq == 1 {
		if got != "true false" {
			w.t.Errorf("401 retry endpoint/credential identity = %s, want true false", got)
		}
	} else if !strings.HasPrefix(got, "false ") {
		w.t.Errorf("%d retry endpoint/credential identity = %s, want another endpoint", status, got)
	}
}

// The refused credential is the one the first upstream call used (the first call of a fresh world). A second request must still avoid it: a retry-local exclusion alone is not a rest.
func (w *callWorld) checkRestedCredential(n runningNode) {
	w.t.Helper()
	before := w.supplier.sent()
	r := w.call(n, "/v1/messages", w.richKey, messagesCall)
	w.served(r, "after a credential was refused")
	w.checkBooked(r, booking{charge: messagesCharge, cost: messagesCost, ref: w.lastRef(), upstream: "up-claude", input: 25, output: 15})
	for _, sent := range w.supplier.sent()[len(before):] {
		if sent.auth == before[0].auth {
			w.t.Error("the refused credential was used again by the next request")
		}
	}
}
