package gateway

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
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
	// supplierInput is what the fake upstream reports as input tokens.
	supplierInput = 130
	// supplierSchedule prices an input token at 0.00001 quota and an output token at 0.00004, the native unit of a
	// new-api relay. The supplier's unit value is 0.001 in seedSupplierCost, so a quota is worth a thousandth of a USD.
	supplierSchedule = `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00004"}}`
	// knownCost is what those tokens cost: 0.00001 quota × 0.001 USD is ten billionths of a credit a token, below the
	// sale price as the 保本线 keeps a real one, so a cost taken for the sale price would count far more and the waived
	// case would be charged: that is what pins the known cost. It is not the ledger tests' default fake cost (1000),
	// so wiring that fake back in fails the booked amount.
	knownCost = supplierInput * 10
	// saleForTheInput is what billing charges the user for those tokens at the rig's own price, 0.000001 a token.
	saleForTheInput = supplierInput * inputUnit
)

// TestTheAttemptCostIsTheSuppliersKnownCost: the executor's Costs is the suppliers service in production, and here it is
// real, so the books hold the schedule's native amount times the unit value the supplier's balance was confirmed at, not
// the sale price. A delivered call whose output was reported zero is free, and nothing counts its cost: the three rules
// alone decide; one whose output never came finally is no zero output, so it is charged what it used
// (docs/domains/billing.md「免单规则」).
func TestTheAttemptCostIsTheSuppliersKnownCost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		reported bool // the final usage event reports the output, as zero
		waived   bool // the three rules make the call free
	}{
		{"a delivered call whose output was reported zero is free", true, true},
		{"a delivered call whose output never came finally is no zero output", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, receipt := costedRig(t)
			sample, _ := sample(t, protocol.Messages)
			up := withCosts(r, r.billing, r.suppliers)
			up.setReceipt(receipt)
			up.setScript(costedScript(sample, tc.reported))
			a := r.funded(1e12)
			r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/alpha")))
			switch charged := r.charged(r.lastCall().ID); {
			case tc.waived && charged != 0:
				t.Errorf("the call was charged %d, want nothing: every sampling reported its output and it was zero", charged)
			case !tc.waived && charged == 0:
				t.Error("the call was free, want it charged: an output the upstream never reported is no zero output")
			}
			r.checkTheSuppliersCost(r.lastCall().ID)
			r.ledgerBalanced()
		})
	}
}

// checkTheSuppliersCost checks what a costed call left in the books and on the supplier's side. The books hold the
// supplier's own amount for what the attempt used — the reported tokens, and any the relay had to count itself — and
// never the sale price, a hundred times more; no cap counts a waiver. The supplier's row is the same attempt, written in
// the settlement's own transaction and priced from the same schedule, with the usage the upstream reported and the
// receipt's charge ref. The ref is the supplier's id of the request, which comes with its response headers: the fake
// upstream sends none, so the row has none either — what billing hands over is written as it is (suppliers' own tests
// cover a ref).
func (r *rig) checkTheSuppliersCost(id uuid.UUID) {
	r.t.Helper()
	if got := r.count("SELECT coalesce(sum(amount), 0) FROM billing.entries WHERE book = 'supplier_cost'"); got < knownCost || got >= saleForTheInput {
		r.t.Errorf("the attempt cost the books %d, want the supplier's %d at least and well below the sale's %d", got, knownCost, saleForTheInput)
	}
	row, ok := r.supplierRow(id)
	if !ok {
		r.t.Fatal("the attempt has no cost row of its own")
	}
	if row.items["input"] != supplierInput || row.ref != nil || row.native == "0" {
		r.t.Errorf("the supplier's row is %s native (%s), ref %v, items %v; want the reported input, no ref, an amount",
			row.native, row.flag, row.ref, row.items)
	}
}

// costedRig is a rig whose route entry is a real supplier's endpoint, so that the receipt the executor prices is the
// one that endpoint's attempt leaves, and whose settlement writes the supplier's side through the module's own functions
// (suppliers.CallWrites).
func costedRig(t *testing.T) (*rig, suppliers.AttemptReceipt) {
	t.Helper()
	r := newRig(t, func(o *rigOptions) { o.suppliers = true })
	receipt, endpoint := r.seedSupplierCost()
	e := r.endpoints["alpha-1"]
	e.ID = routing.EndpointID(endpoint)
	r.endpoints["alpha-1"] = e
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, e)
	return r, receipt
}

// seedSupplierCost writes one supplier into the suppliers tables and loads it: a group with an active credential, an
// endpoint of the acme/alpha model priced by supplierSchedule, and unit value 0.001. It returns the receipt an attempt on
// the endpoint leaves, and the endpoint's id.
func (r *rig) seedSupplierCost() (suppliers.AttemptReceipt, uuid.UUID) {
	r.t.Helper()
	return r.seedSupplierWith(supplierSchedule)
}

// seedSupplierWith is seedSupplierCost with the endpoint's cost schedule given: another schedule prices other items, so
// a test can check that an attempt's row holds the count of every item it used.
func (r *rig) seedSupplierWith(prices string) (suppliers.AttemptReceipt, uuid.UUID) {
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
				VALUES ($1, $2, $3, 'quota', $4::jsonb, 'published', 'v1')`, []any{schedule, supplier, endpoint, prices}},
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
	if err := r.suppliers.Reload(ctx); err != nil {
		r.t.Fatal(err)
	}
	a, err := r.suppliers.Acquire(endpoint)
	if err != nil {
		r.t.Fatal(err)
	}
	return a.Done(), endpoint
}

// newSuppliers makes the rig's suppliers service, before the rig's billing has one to talk to: the tables it reads are
// written by seedSupplierCost, which reloads it.
func (r *rig) newSuppliers() (*suppliers.Service, error) {
	exit, err := egress.New(egress.Direct)
	if err != nil {
		return nil, err
	}
	return suppliers.New(suppliers.Config{DB: r.db, Scope: platformScope, Keys: masterKey(r.t), Egress: exit})
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
// Output is reported as 0 when reported, and left out otherwise, because a count never reported proves nothing about a
// zero output (billing's zeroOutput reads it that way).
func supplierStart(reported bool) string {
	usage := `{"input_tokens":` + strconv.Itoa(supplierInput) + `}`
	if reported {
		usage = `{"input_tokens":` + strconv.Itoa(supplierInput) + `,"output_tokens":0}`
	}
	return "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant",` +
		`"model":"up","content":[],"stop_reason":null,"stop_sequence":null,"usage":` + usage + "}}\n\n"
}

// costedScript is the upstream's answer: it reports the input and sends content, which commits the call, and, when the
// output is reported, the final usage event with it at 0. Either way the stream ends without a message_stop, which the
// relay reads as the upstream breaking off. The final event is what makes an output reported rather than estimated: a
// count that never came finally is a lower bound, and billing does not take a lower bound for a reported zero.
func costedScript(sample []string, reported bool) scripted {
	data := supplierStart(reported) + events(sample, evBlock) + events(sample, evHello)
	if reported {
		data += strings.Replace(events(sample, evMsgDelta), `"output_tokens": 15`, `"output_tokens": 0`, 1)
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
