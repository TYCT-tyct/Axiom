package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrInvalidConfig identifies a backend URL or key the admin cannot save.
var ErrInvalidConfig = errors.New("websearch: invalid search backend configuration")

var searchLocation = secret.Location{Table: "platform.search_config", RowID: uuid.Nil().String(), Purpose: "config"}

// Settings is the backend configuration at the admin boundary. Key is nil when the caller did not send one, which keeps
// the saved key while the URL is unchanged; it is never returned.
type Settings struct {
	URL string  `json:"url"`
	Key *string `json:"key,omitempty"`
}

// StoredConfig opens the complete configuration; absent means the environment is still authoritative, and a saved row
// with an empty URL means the admin turned the feature off.
func StoredConfig(ctx context.Context, tx pg.Tx, keys *secret.Keyring) (Config, bool, error) {
	var sealed secret.Sealed
	err := tx.QueryRow(ctx, `SELECT ciphertext,nonce,wrapped_dek,key_version FROM platform.search_config WHERE id=$1`, uuid.Nil()).
		Scan(&sealed.Ciphertext, &sealed.Nonce, &sealed.WrappedDEK, &sealed.KeyVersion)
	if errors.Is(err, pg.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	plain, err := keys.Open(sealed, searchLocation)
	if err != nil {
		return Config{}, false, err
	}
	var s Settings
	if err = jsonx.Unmarshal([]byte(plain.Reveal()), &s); err != nil {
		return Config{}, false, err
	}
	key := ""
	if s.Key != nil {
		key = *s.Key
	}
	cfg, err := Parse(s.URL, key)
	return cfg, true, err
}

// SaveConfig serializes edits and their audit. A URL change cannot forward the previous key to it: the caller must
// enter it again, so a saved credential never authenticates to a backend it was not entered for.
func SaveConfig(ctx context.Context, tx pg.Tx, keys *secret.Keyring, actor audit.Actor, s Settings, reason string) (Config, error) {
	s.URL = strings.TrimSpace(s.URL)
	if s.Key != nil {
		key := strings.TrimSpace(*s.Key)
		s.Key = &key
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('platform.search_config'))`); err != nil {
		return Config{}, err
	}
	old, _, err := StoredConfig(ctx, tx, keys)
	if err != nil {
		return Config{}, err
	}
	key, err := s.keyFor(old)
	if err != nil {
		return Config{}, err
	}
	cfg, err := Parse(s.URL, key)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if !cfg.Configured() {
		s = Settings{}
	} else {
		s = Settings{URL: cfg.URL, Key: &key}
	}
	body, err := jsonx.MarshalCanonical(s)
	if err != nil {
		return Config{}, err
	}
	sealed := keys.Seal(secret.New(string(body)), searchLocation)
	_, err = tx.Exec(ctx, `INSERT INTO platform.search_config(id,ciphertext,nonce,wrapped_dek,key_version) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(id) DO UPDATE SET ciphertext=excluded.ciphertext,nonce=excluded.nonce,wrapped_dek=excluded.wrapped_dek,key_version=excluded.key_version`, uuid.Nil(), sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion)
	if err != nil {
		return Config{}, err
	}
	err = audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "search_config.update", Reason: reason, Target: audit.Target{Module: "platform", Kind: "search_config", ID: uuid.Nil().String()}, Detail: map[string]any{"configured": cfg.Configured()}})
	return cfg, err
}

// keyFor is the key the saved settings will carry: the caller's when it sent one, the saved one when the URL is
// unchanged, and nothing when the backend is cleared.
func (s Settings) keyFor(old Config) (string, error) {
	if s.URL == "" {
		return "", nil
	}
	if s.Key != nil {
		return *s.Key, nil
	}
	if old.Key.Reveal() == "" {
		return "", nil
	}
	if old.URL != s.URL {
		return "", fmt.Errorf("%w: reenter the key when changing the backend URL", ErrInvalidConfig)
	}
	return old.Key.Reveal(), nil
}

// TestConfig runs one search against the backend and returns only a stable, non-sensitive outcome: the query is fixed,
// and the error is logged without the backend's body.
func TestConfig(ctx context.Context, cfg Config, client *egress.Client, logger *slog.Logger) string {
	s, err := Open(cfg, client)
	if err != nil {
		logger.WarnContext(ctx, "search configuration test failed", "request_id", httpserver.RequestID(ctx), "err", err)
		return "failed"
	}
	if s == nil {
		return "disabled"
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if _, err := s.Search(ctx, Query{Text: "axiom", MaxResults: 1}); err != nil {
		logger.WarnContext(ctx, "search configuration test failed", "request_id", httpserver.RequestID(ctx), "err", err)
		return "failed"
	}
	return "accepted"
}
