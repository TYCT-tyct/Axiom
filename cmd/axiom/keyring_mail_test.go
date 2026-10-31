package main

import (
	"bytes"
	"testing"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/websearch"
)

func TestKeyringRewrapsSMTPConfig(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	settings := mail.Settings{Enabled: true, Host: "smtp.example.com", Port: 587, Username: "test", From: "no-reply@example.com", Password: new("rotation smtp value")}
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := mail.SaveConfig(t.Context(), tx, masterKeys(t), audit.System("platform.keyring.test"), settings, "save")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	row := rotationEnvelope{store: secret.EnvelopeStore{Schema: "platform", Table: "smtp_config", ID: "id", Purpose: "config"}, id: uuid.Nil().String()}
	before := f.read(row)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	if err := f.r.Retire(t.Context(), 1); err == nil {
		t.Fatal("retired an SMTP reference")
	}
	f.run("rewrap", "--batch-size", "1")
	f.run("retire", "--version", "1")
	after := f.read(row)
	if after.KeyVersion != 2 || !bytes.Equal(before.Ciphertext, after.Ciphertext) || !bytes.Equal(before.Nonce, after.Nonce) {
		t.Fatal("SMTP data changed or key not rewrapped")
	}
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		cfg, found, err := mail.StoredConfig(t.Context(), tx, ring)
		if err == nil && (!found || cfg.Password.Reveal() != *settings.Password || cfg.Host != settings.Host) {
			t.Fatal("SMTP config did not survive rotation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringRewrapsSearchConfig(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	settings := websearch.Settings{URL: "https://search.example.com", Key: new("rotation search value")}
	saveSearchSettings(t, f, settings, websearch.Config{}, "save")
	row := rotationEnvelope{store: secret.EnvelopeStore{Schema: "platform", Table: "search_config", ID: "id", Purpose: "config"}, id: uuid.Nil().String()}
	before := f.read(row)
	f.run("add", "--version", "2")
	f.run("activate", "--version", "2")
	if err := f.r.Retire(t.Context(), 1); err == nil {
		t.Fatal("retired a search reference")
	}
	f.run("rewrap", "--batch-size", "1")
	f.run("retire", "--version", "1")
	after := f.read(row)
	if after.KeyVersion != 2 || !bytes.Equal(before.Ciphertext, after.Ciphertext) || !bytes.Equal(before.Nonce, after.Nonce) {
		t.Fatal("search data changed or key not rewrapped")
	}
	ring, _, err := f.r.Check(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		cfg, found, err := websearch.StoredConfig(t.Context(), tx, ring)
		if err == nil && (!found || cfg.Key.Reveal() != *settings.Key || cfg.URL != settings.URL) {
			t.Fatal("search config did not survive rotation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSearchConfigFirstSaveKeepsEnvironmentKey: before the first database row, the environment's configuration is
// what an omitted key means. Keeping the URL and leaving the key out must carry that key into the row rather than
// write an empty one; clearing the URL still disables the feature, and the empty row stays authoritative.
func TestSearchConfigFirstSaveKeepsEnvironmentKey(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	environment := websearch.Config{URL: "https://search.example.com", Key: secret.New("environment search value")}
	saveSearchSettings(t, f, websearch.Settings{URL: environment.URL}, environment, "first save")
	cfg, found := storedSearchSettings(t, f)
	if !found || cfg.URL != environment.URL || cfg.Key.Reveal() != environment.Key.Reveal() {
		t.Fatalf("first saved config = %+v, found %v; want the environment key retained", cfg, found)
	}
	saveSearchSettings(t, f, websearch.Settings{}, environment, "disable")
	cfg, found = storedSearchSettings(t, f)
	if !found || cfg.Configured() {
		t.Fatalf("after clearing, config = %+v, found %v; want an authoritative disabled row", cfg, found)
	}
}

// saveSearchSettings saves a search configuration as the admin does, with the environment as the configuration a node
// without a row has.
func saveSearchSettings(t *testing.T, f rotationFixture, s websearch.Settings, environment websearch.Config, reason string) {
	t.Helper()
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := websearch.SaveConfig(t.Context(), tx, masterKeys(t), audit.System("platform.keyring.test"), s, environment, reason)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// storedSearchSettings reads the saved search configuration; found is false before the first save.
func storedSearchSettings(t *testing.T, f rotationFixture) (cfg websearch.Config, found bool) {
	t.Helper()
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		cfg, found, err = websearch.StoredConfig(t.Context(), tx, masterKeys(t))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cfg, found
}
