package main

import (
	"bytes"
	"errors"
	"testing"

	"axiom/internal/payments"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

func TestKeyringRewrapsMerchantConfig(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	currency, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	in := payments.MerchantInput{
		Kind: "rotation", Name: "Rotation", Currency: currency, TTLSeconds: 60, Settings: []byte(`{}`),
		Secrets: map[string]secret.Secret{"key": secret.New("rotation merchant secret")},
		Methods: []payments.MethodInput{{Method: "rotation", Upstream: "rotation"}},
	}
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	f.run("rewrap")
	// Reproduce a restored merchant row: it alone references V1, while the other stores have already moved to V2.
	made := f.saveRotationMerchant(masterKeys(t), in)
	var merchant secret.EnvelopeStore
	for _, store := range f.r.Stores {
		if store.Table == "merchant_configs" {
			merchant = store
		}
	}
	row := rotationEnvelope{store: merchant, id: made.ID.String()}
	before := f.read(row)
	metadata := f.text("SELECT settings || ':' || rev::text FROM payments.merchant_configs")
	if err := f.r.Retire(t.Context(), 1); err == nil {
		t.Fatal("retired a referenced merchant key")
	}
	f.run("rewrap", "--batch-size", "1")
	f.run("retire", "--version", "1")
	after := f.read(row)
	if after.KeyVersion != 2 || !bytes.Equal(before.Ciphertext, after.Ciphertext) || !bytes.Equal(before.Nonce, after.Nonce) {
		t.Fatal("merchant rewrap changed the data layer or kept the old version")
	}
	if got := f.text("SELECT settings || ':' || rev::text FROM payments.merchant_configs"); got != metadata {
		t.Fatal("merchant rewrap changed the configuration or its revision")
	}
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	in.ID, in.Rev, in.Secrets = payments.MerchantID(made.ID), made.Rev, nil
	// A save without replacement secrets must open the old configuration through payments' own AAD and settings hash.
	f.saveRotationMerchant(ring, in)
}

func (f rotationFixture) saveRotationMerchant(keys *secret.Keyring, in payments.MerchantInput) payments.Merchant {
	f.t.Helper()
	p, err := payments.Open(payments.Config{
		DB: f.db, Platform: pg.PlatformScope(), Keyring: keys, Plugins: []payments.Plugin{rotationMerchantPlugin{}},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.db.Tx(f.t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		if err := tx.QueryRow(f.t.Context(), "SELECT id FROM accounts.sites WHERE is_main").Scan(&in.Site); err != nil {
			return err
		}
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
	made, err := p.SaveMerchant(f.t.Context(), pg.PlatformScope(), audit.System("platform.keyring.test"), in)
	if err != nil {
		f.t.Fatal(err)
	}
	return made
}

type rotationMerchantPlugin struct{}

func (rotationMerchantPlugin) Kind() payments.Kind { return "rotation" }

func (rotationMerchantPlugin) Open(c payments.MerchantConfig) (payments.Channel, error) {
	if c.Secrets["key"].Reveal() != "rotation merchant secret" {
		return nil, errors.New("merchant secret did not survive rotation")
	}
	return rotationMerchantChannel{}, nil
}

// Only Methods is needed to save a configuration; no payment is executed by the rotation test.
type rotationMerchantChannel struct{ payments.Channel }

func (rotationMerchantChannel) Methods() []payments.Method { return []payments.Method{"rotation"} }
