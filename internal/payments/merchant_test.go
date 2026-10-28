package payments

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/secret"
)

const theKey = "fake-merchant-key-0123456789"

var admin = audit.System("payments.test")

// world is a database with two sites, A and B, each with one account, and a Payments over it.
type world struct {
	t              *testing.T
	db             *pg.DB
	super          string
	p              *Payments
	logs           *lockedBuffer
	siteA, siteB   uuid.UUID
	account, acctB uuid.UUID
	user           uuid.UUID
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newWorld(t *testing.T) *world {
	t.Helper()
	return newTestWorld(t, fakePlugin{})
}

// newTestWorld is newWorld with a channel plugin of the test's own, for the tests that need to watch what a channel is
// asked or to have it answer in a way of their choosing.
func newTestWorld(t *testing.T, plugin Plugin) *world {
	t.Helper()
	d := pgtest.New(t, billing.Migrations(), Migrations())
	db, err := pg.Open(t.Context(), d.App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	w := &world{
		t: t, db: db, super: d.Superuser, logs: &lockedBuffer{},
		siteA: uuid.NewV7(), siteB: uuid.NewV7(), account: uuid.NewV7(), acctB: uuid.NewV7(), user: uuid.NewV7(),
	}
	w.exec(fmt.Sprintf("INSERT INTO platform.tenants VALUES ('%s','%[1]s'), ('%[1]s','%s'), ('%s','%[3]s'), ('%[3]s','%s')",
		w.siteA, w.account, w.siteB, w.acctB))
	w.exec(fmt.Sprintf("INSERT INTO billing.balances (site_id, account_id) VALUES ('%s','%s'), ('%s','%s')",
		w.siteA, w.account, w.siteB, w.acctB))
	w.p, err = Open(Config{
		DB: db, Platform: pg.NewScope(true, nil, nil), Keyring: masterKey(t), Plugins: []Plugin{plugin},
		Logger: slog.New(slog.NewTextHandler(w.logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

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

// exec runs statements as the superuser, from outside the application.
func (w *world) exec(statements ...string) {
	w.t.Helper()
	if err := pgtest.Run(w.t.Context(), w.super, statements...); err != nil {
		w.t.Fatal(err)
	}
}

func siteScope(site uuid.UUID) pg.Scope { return pg.NewScope(false, []uuid.UUID{site}, nil) }

// accountA and accountB are the accounts' scopes as the console authorizes them: with their site.
func (w *world) accountA() pg.Scope { return pg.AccountScopeIn(w.siteA, w.account) }
func (w *world) accountB() pg.Scope { return pg.AccountScopeIn(w.siteB, w.acctB) }

func (w *world) tx(s pg.Scope, f func(tx pg.Tx) error) error { return w.db.Tx(w.t.Context(), s, f) }

func (w *world) input(site uuid.UUID, methods ...MethodInput) MerchantInput {
	if len(methods) == 0 {
		methods = []MethodInput{{Method: "alipay", Upstream: "alipay", Fee: Fee{PPM: 30_000}, Enabled: true}}
	}
	return MerchantInput{
		Site: site, Kind: "fake", Name: "Gateway", Enabled: true, Currency: currency(w.t, "CNY"), TTLSeconds: 1800,
		Settings: []byte(`{"methods": ["alipay", "wxpay"], "gateway": "https://pay.example.com"}`),
		Secrets:  map[string]secret.Secret{"key": secret.New(theKey)}, Methods: methods,
	}
}

func (w *world) save(s pg.Scope, in MerchantInput) (Merchant, error) {
	var m Merchant
	err := w.tx(s, func(tx pg.Tx) (err error) { m, err = w.p.SaveMerchant(w.t.Context(), tx, admin, in); return err })
	return m, err
}

func (w *world) mustSave(s pg.Scope, in MerchantInput) Merchant {
	w.t.Helper()
	m, err := w.save(s, in)
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) channel(id MerchantID) (Channel, error) {
	var ch Channel
	err := w.tx(siteScope(w.siteA), func(tx pg.Tx) (err error) { ch, err = w.p.channel(w.t.Context(), tx, id); return err })
	return ch, err
}

func TestSaveMerchantSealsTheSecrets(t *testing.T) {
	w := newWorld(t)
	m := w.mustSave(siteScope(w.siteA), w.input(w.siteA))
	if m.Settings != `{"gateway":"https://pay.example.com","methods":["alipay","wxpay"]}` || m.Rev != 1 || len(m.Methods) != 1 {
		t.Fatalf("saved %+v", m)
	}
	ch, err := w.channel(MerchantID(m.ID))
	if err != nil || ch.(fakeChannel).key != theKey {
		t.Fatalf("channel = %+v, %v; want it opened with the key", ch, err)
	}
	// Nowhere the key could leak to holds it: the stored row, the audit log, the logs, the response.
	var stored string
	err = w.tx(pg.NewScope(true, nil, nil), func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT c::text || coalesce((SELECT string_agg(a::text, '') FROM platform.audit_log a), '')
			FROM payments.merchant_configs c`).Scan(&stored)
	})
	if err != nil || strings.Contains(stored, theKey) || !strings.Contains(stored, "merchant.create") {
		t.Fatalf("stored rows %q, %v", stored, err)
	}
	if strings.Contains(w.logs.String(), theKey) || strings.Contains(fmt.Sprintf("%+v %#v", m, w.input(w.siteA)), theKey) {
		t.Fatal("the key is in the logs or in a printed merchant")
	}
}

func TestSaveMerchantRefusesInvalidInput(t *testing.T) {
	w := newWorld(t)
	site := siteScope(w.siteA)
	m := w.mustSave(site, w.input(w.siteA))
	for name, c := range map[string]struct {
		edit  func(*MerchantInput)
		field string
		err   error
	}{
		"settings not JSON":  {func(in *MerchantInput) { in.Settings = []byte("{") }, "/settings", nil},
		"settings an array":  {func(in *MerchantInput) { in.Settings = []byte("[]") }, "/settings", nil},
		"plugin refuses":     {func(in *MerchantInput) { in.Settings = []byte(`{"gateway":"http://x","methods":[]}`) }, "/settings", nil},
		"no key":             {func(in *MerchantInput) { in.Secrets = map[string]secret.Secret{} }, "/settings", nil},
		"secrets on create":  {func(in *MerchantInput) { in.Secrets = nil }, "/secrets", nil},
		"unknown kind":       {func(in *MerchantInput) { in.Kind = "nope" }, "/kind", nil},
		"method not offered": {func(in *MerchantInput) { in.Methods[0].Method = "paypal" }, "/methods/0/method", nil},
		"fee above 50%":      {func(in *MerchantInput) { in.Methods[0].Fee.PPM = maxFeePPM + 1 }, "/methods/0/fee_ppm", nil},
		"min above max": {func(in *MerchantInput) {
			in.Methods[0].SingleMin, in.Methods[0].SingleMax = new(int64(5)), new(int64(4))
		}, "/methods/0/single_max", nil},
		"method twice":       {func(in *MerchantInput) { in.Methods = append(in.Methods, in.Methods[0]) }, "/methods/1/method", nil},
		"ttl":                {func(in *MerchantInput) { in.TTLSeconds = 1 }, "/ttl_seconds", nil},
		"stale revision":     {func(in *MerchantInput) { in.ID, in.Rev = MerchantID(m.ID), 7 }, "", ErrStale},
		"another site's":     {func(in *MerchantInput) { in.ID, in.Rev = MerchantID(uuid.NewV7()), 1 }, "", ErrNotFound},
		"kind cannot change": {func(in *MerchantInput) { in.ID, in.Rev, in.Kind = MerchantID(m.ID), 1, "nope" }, "", nil},
	} {
		in := w.input(w.siteA)
		c.edit(&in)
		_, err := w.save(site, in)
		var fe *FieldError
		if c.err != nil && !errors.Is(err, c.err) || c.err == nil && (!errors.As(err, &fe) || c.field != "" && fe.Field != c.field) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSaveMerchantKeepsTheSecretsWhenChanging(t *testing.T) {
	w := newWorld(t)
	site := siteScope(w.siteA)
	in := w.input(w.siteA)
	m := w.mustSave(site, in)
	in.ID, in.Rev, in.Secrets, in.Name = MerchantID(m.ID), m.Rev, nil, "Renamed"
	in.Settings = []byte(`{"gateway":"https://other.example.com","methods":["wxpay"]}`)
	in.Methods = []MethodInput{{Method: "wxpay", Upstream: "wxpay", Enabled: true}}
	changed := w.mustSave(site, in)
	if changed.Rev != 2 || changed.Name != "Renamed" || len(changed.Methods) != 1 || changed.Methods[0].Method != "wxpay" {
		t.Fatalf("changed to %+v", changed)
	}
	if ch, err := w.channel(MerchantID(m.ID)); err != nil || ch.(fakeChannel).key != theKey || ch.(fakeChannel).settings.Gateway != "https://other.example.com" {
		t.Fatalf("channel = %+v, %v", ch, err)
	}
}

// TestEditedGatewayMakesTheMerchantUnusable changes the settings in the database, past SaveMerchant: the sealed hash
// no longer matches, so neither the channel nor a save that keeps the secrets accepts them until the secrets are given.
func TestEditedGatewayMakesTheMerchantUnusable(t *testing.T) {
	w := newWorld(t)
	site := siteScope(w.siteA)
	in := w.input(w.siteA)
	m := w.mustSave(site, in)
	w.exec(`UPDATE payments.merchant_configs SET settings = '{"gateway":"https://evil.example.com","methods":["alipay"]}'`)
	if _, err := w.channel(MerchantID(m.ID)); !errors.Is(err, ErrTampered) {
		t.Fatalf("channel after the edit: %v", err)
	}
	if !strings.Contains(w.logs.String(), "changed outside a save") {
		t.Fatalf("no alert: %s", w.logs.String())
	}
	in.ID, in.Rev, in.Secrets = MerchantID(m.ID), m.Rev, nil
	if _, err := w.save(site, in); !errors.Is(err, ErrTampered) {
		t.Fatalf("save keeping the secrets: %v", err)
	}
	in.Secrets = map[string]secret.Secret{"key": secret.New(theKey)}
	w.mustSave(site, in)
	if _, err := w.channel(MerchantID(m.ID)); err != nil {
		t.Fatalf("channel after saving again: %v", err)
	}
}
