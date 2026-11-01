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

func TestKeyringRewrapsSearchEngines(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	id := uuid.NewV7()
	saveSearchEngine(t, f, id, "rotation search value", websearch.Config{})
	row := rotationEnvelope{store: secret.EnvelopeStore{Schema: "platform", Table: "search_engines", ID: "id", Purpose: "config"}, id: id.String()}
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
		loaded, err := websearch.Load(t.Context(), tx, ring, websearch.Config{})
		if err == nil && (len(loaded.Engines) != 1 || loaded.Engines[0].Key.Reveal() != "rotation search value") {
			t.Fatal("the search engine did not survive rotation")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSearchEnginesFirstSaveKeepsEnvironmentKey: before the first engine row, the environment's backend is what the
// admin sees. Saving it under its own name without retyping the key must carry that key into the row rather than write
// an empty one.
func TestSearchEnginesFirstSaveKeepsEnvironmentKey(t *testing.T) {
	t.Parallel()
	f := newRotationFixture(t)
	environment := websearch.Config{URL: "https://search.example.com", Key: secret.New("environment search value")}
	saveSearchEngine(t, f, uuid.Nil(), "", environment)
	var loaded websearch.Loaded
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		loaded, err = websearch.Load(t.Context(), tx, masterKeys(t), environment)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(loaded.Engines) != 1 || loaded.Engines[0].Key.Reveal() != environment.Key.Reveal() {
		t.Fatalf("first saved engines = %+v, want the environment key retained", loaded.Engines)
	}
	// Clearing the list must stay cleared even though the environment still names a backend.
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := websearch.Save(t.Context(), tx, masterKeys(t), audit.System("platform.keyring.test"),
			websearch.Settings{}, environment, "disable")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		var err error
		loaded, err = websearch.Load(t.Context(), tx, masterKeys(t), environment)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if loaded.Source != "none" || len(loaded.Engines) != 0 {
		t.Fatalf("after clearing = %+v, want no engines even with the environment configured", loaded)
	}
}

// saveSearchEngine saves one SearXNG engine as the admin does. An empty id creates one; key overrides the environment
// key the new engine would otherwise inherit at the same URL.
func saveSearchEngine(t *testing.T, f rotationFixture, id uuid.UUID, key string, environment websearch.Config) {
	t.Helper()
	url := environment.URL
	if url == "" {
		url = "https://search.example.com"
	}
	settings := websearch.EngineSettings{
		Name: "searxng", Kind: "searxng", URL: url, Enabled: true, Default: true,
		CostSearch: "0", CostResult: "0", Markup: "0",
	}
	if id != uuid.Nil() {
		settings.ID = id.String()
	}
	if key != "" {
		settings.Key = &key
	}
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error {
		_, err := websearch.Save(t.Context(), tx, masterKeys(t), audit.System("platform.keyring.test"),
			websearch.Settings{Engines: []websearch.EngineSettings{settings}}, environment, "save")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
