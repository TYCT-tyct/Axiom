package gateway

import (
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The tests here run the executor with the production Costs: the suppliers service, pricing the attempts from a real
// supplier's cost schedule and confirmed unit value, against the real billing tables (the rig's). The upstream is the
// only fake, as it is in the ledger tests: what is checked is the money.

const (
	// supplierSchedule prices an input token at 0.00001 quota and an output token at 0.00004, the native unit of a
	// new-api relay. The supplier's unit value is 0.001 in seedSupplierCost, so a quota is worth a thousandth of a USD.
	supplierSchedule = `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00004"}}`
	// knownCost is what the 100 input tokens the fake upstream reports cost: 100 × 0.00001 quota × 0.001 USD, in
	// billionths of a credit. It is below the sale price, as the 保本线 keeps a real one, so a cost taken for the sale
	// price would count 100 times as much and the waived case would be charged: that is what pins the known cost.
	knownCost = 1000
	// saleForTheInput is what billing charges the user for those tokens at the rig's own price, 0.000001 a token.
	saleForTheInput = supplierInput * inputUnit
	supplierInput   = 100
)

// TestAnAttemptCostsTheSuppliersPrice: the executor's Costs is the suppliers service in production, and this one is
// real. An attempt books the schedule's native amount times the unit value the supplier's balance was confirmed at,
// and the cap set exactly at that cost waives the call: a cost taken for the attempt's sale price (100,000) would not,
// so this case proves the cap used the known cost and the books hold the cost itself.
func TestAnAttemptCostsTheSuppliersPrice(t *testing.T) {
	t.Parallel()
	r, svc, receipt := costedRig(t, knownCost)
	sample, _ := sample(t, protocol.Messages)
	up := withCosts(r, r.billing, svc)
	up.setReceipt(receipt)
	up.setScript(costedScript(sample, false, false))
	a := r.funded(1e12)
	r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))
	if got := r.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'supplier_cost'"); got != knownCost {
		t.Errorf("the attempt cost the books %d, want the suppliers service's %d", got, knownCost)
	}
	if got := r.count("SELECT coalesce(sum(cost), 0) FROM billing.waivers"); got != knownCost {
		t.Errorf("the cap counted the attempt at %d, want the known %d", got, knownCost)
	}
	if got := r.charged(r.lastCall().ID); got != 0 {
		t.Errorf("the call within the cap was charged %d, want nothing", got)
	}
	r.ledgerBalanced()
}

// TestTheWaiverCapStopsAtTheKnownCost: the daily cap counts an attempt's cost as the suppliers service prices it, so
// the call that takes the account past the cap is charged its sale price, and of the waivable calls only a delivered
// one whose output was reported zero still waives past it (docs/domains/billing.md「免单规则」). The first case's cap
// is the known cost itself, far below the sale price: a cost taken for the sale price would not waive at all, so that
// case shows which of the two the cap used. Past the cap the zero-output waiver is not counted: it is the condition,
// not room the cap had.
func TestTheWaiverCapStopsAtTheKnownCost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		cap         int64
		deliver     bool // the upstream sends content and the final usage before it breaks: the call was delivered
		zeroOutput  bool // its final usage reports output 0, which proves the zero-output condition
		wantCharged int64
		wantCounted bool // the cap had room for the call's cost and counted it; past the cap it never does
	}{
		{"within the cap, the failed call is waived", knownCost, false, false, 0, true},
		{"past the cap, the failed call is charged its sale price", knownCost - 1, false, false, saleForTheInput, false},
		{"past the cap, a delivered zero-output finish still waives", knownCost - 1, true, true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, svc, receipt := costedRig(t, tc.cap)
			sample, _ := sample(t, protocol.Messages)
			up := withCosts(r, r.billing, svc)
			up.setReceipt(receipt)
			up.setScript(costedScript(sample, tc.deliver, tc.zeroOutput))
			a := r.funded(1e12)
			r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))
			if got := r.charged(r.lastCall().ID); got != tc.wantCharged {
				t.Errorf("the call past the cap was charged %d, want %d", got, tc.wantCharged)
			}
			wantCounted := int64(0)
			if tc.wantCounted {
				wantCounted = knownCost
			}
			if got := r.count("SELECT coalesce(sum(cost), 0) FROM billing.waivers"); got != wantCounted {
				t.Errorf("the cap counted %d, want %d", got, wantCounted)
			}
			if got := r.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'supplier_cost'"); got != knownCost {
				t.Errorf("the attempt cost the books %d, want the suppliers service's %d", got, knownCost)
			}
			r.ledgerBalanced()
		})
	}
}

// costedRig is a rig whose route entry is a real supplier's endpoint, so that the receipt the executor prices is the
// one that endpoint's attempt leaves. It has the account's daily waiver cap set.
func costedRig(t *testing.T, waiverCap int64) (*rig, *suppliers.Service, suppliers.AttemptReceipt) {
	t.Helper()
	r := newRig(t, func(o *rigOptions) { o.suppliers, o.waiverCap = true, waiverCap })
	svc, receipt, endpoint := r.seedSupplierCost()
	e := r.endpoints["alpha-1"]
	e.ID = routing.EndpointID(endpoint)
	r.endpoints["alpha-1"] = e
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, e)
	return r, svc, receipt
}

// seedSupplierCost writes one supplier into the suppliers tables and loads it: a group with an active credential, an
// endpoint of the acme/alpha model priced by supplierSchedule, and unit value 0.001. It returns the service, the
// receipt an attempt on the endpoint leaves, and the endpoint's id.
func (r *rig) seedSupplierCost() (*suppliers.Service, suppliers.AttemptReceipt, uuid.UUID) {
	r.t.Helper()
	ctx := r.t.Context()
	keys := masterKey(r.t)
	supplier, group, credential, endpoint, schedule := uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	sealed := keys.Seal(secret.New("sk-supplier"), secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"})
	version := int64(sealed.KeyVersion)
	err := r.db.Tx(ctx, platformScope, func(tx pg.Tx) error {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{
				`INSERT INTO suppliers.suppliers (id, owner_scope, name, kind, origin, upstream_account, native_unit,
				settlement_currency, mgmt_egress_id, status)
				VALUES ($1, 'platform', 'Costed', 'newapi', 'https://supplier.test', 'account', 'quota', 'USD', 'direct', 'active')`,
				[]any{supplier},
			},
			{`INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, selected, present, codename)
				VALUES ($1, $2, 'default', 'default', 1, true, true, 'maple')`, []any{group, supplier}},
			{
				`INSERT INTO suppliers.credentials (id, supplier_id, supplier_group_id, upstream_key_ref, ciphertext, nonce,
				wrapped_dek, key_version, status, max_concurrency) VALUES ($1, $2, $3, 'key-1', $4, $5, $6, $7, 'active', 4)`,
				[]any{credential, supplier, group, sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, version},
			},
			{`INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
				VALUES ($1, $2, $3, $4, 'up-alpha', ARRAY['messages'])`, []any{endpoint, supplier, group, uuid.UUID(r.alpha)}},
			{`INSERT INTO suppliers.cost_schedules (id, supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
				VALUES ($1, $2, $3, 'quota', $4::jsonb, 'published', 'v1')`, []any{schedule, supplier, endpoint, supplierSchedule}},
			{`UPDATE suppliers.endpoints SET cost_state = 'known', cost_schedule_id = $1 WHERE id = $2`, []any{schedule, endpoint}},
			{`INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 0.001, 'test', 'seed')`, []any{supplier}},
		} {
			if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	exit, err := egress.New(egress.Direct)
	if err != nil {
		r.t.Fatal(err)
	}
	svc, err := suppliers.New(suppliers.Config{
		DB: r.db, Scope: platformScope, Keys: keys, Egress: exit, Logger: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	if err := svc.Reload(ctx); err != nil {
		r.t.Fatal(err)
	}
	a, err := svc.Acquire(suppliers.EndpointID(endpoint))
	if err != nil {
		r.t.Fatal(err)
	}
	return svc, a.Done(), endpoint
}

// masterKey reads a keyring from a file, as the node does from AXIOM_MASTER_KEY_FILE.
func masterKey(t *testing.T) *secret.Keyring {
	t.Helper()
	path := filepath.Join(t.TempDir(), "master-key")
	line := "1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))) + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := secret.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// supplierStart is the message_start of the fake upstream's stream: the input tokens the attempt is costed from.
// Output is reported as 0 when zeroOutput, and left unreported otherwise, because a count never reported proves
// nothing about a zero output (billing's zeroOutput reads it that way).
func supplierStart(zeroOutput bool) string {
	usage := `{"input_tokens":100}`
	if zeroOutput {
		usage = `{"input_tokens":100,"output_tokens":0}`
	}
	return "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant",` +
		`"model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":` + usage + "}}\n\n"
}

// costedScript is the upstream's answer: it reports the input and starts the reply, sends content when the call is to
// be delivered (which commits it) and the final usage event, and then ends without a message_stop, which the relay
// reads as the upstream breaking off. The final event is what makes an output reported rather than estimated: a count
// that never came finally is a lower bound, and billing does not take a lower bound for a reported zero.
func costedScript(sample []string, deliver, zeroOutput bool) scripted {
	data := supplierStart(zeroOutput) + events(sample, evBlock)
	if deliver {
		out := "15"
		if zeroOutput {
			out = "0"
		}
		data += events(sample, evHello) + strings.Replace(events(sample, evMsgDelta), `"output_tokens": 15`, `"output_tokens": `+out, 1)
	}
	return serve(beat{data: data})
}

// setScript gives every endpoint of the fake upstreams the same answer.
func (u *fakeUpstreams) setScript(s scripted) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.eps {
		e.script = []scripted{s}
	}
}

// setReceipt gives every endpoint of the fake upstreams the receipt a real attempt on it leaves.
func (u *fakeUpstreams) setReceipt(r suppliers.AttemptReceipt) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, e := range u.eps {
		e.receipt = r
	}
}

// lastCall is the call the gateway admitted last.
func (r *rig) lastCall() *Call {
	r.t.Helper()
	calls := r.calls()
	if len(calls) == 0 {
		r.t.Fatal("the gateway admitted no call")
	}
	return calls[len(calls)-1]
}

// charged is what the request's settlement charged the user.
func (r *rig) charged(id uuid.UUID) int64 {
	return r.count("SELECT charged FROM billing.reservations WHERE request_id = $1", id)
}
