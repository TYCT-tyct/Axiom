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
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := websearch.SaveConfig(t.Context(), tx, masterKeys(t), audit.System("platform.keyring.test"), settings, "save")
		return err
	}); err != nil {
		t.Fatal(err)
	}
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
