package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"uuid"

	"axiom/internal/platform/httpserver"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrInvalidConfig identifies a locally invalid transport or unsafe credential reuse.
var ErrInvalidConfig = errors.New("mail: invalid SMTP configuration")

var smtpLocation = secret.Location{Table: "platform.smtp_config", RowID: uuid.Nil().String(), Purpose: "config"}

// Settings is the transport configuration at the admin boundary. Password is never serialized back.
type Settings struct {
	Enabled  bool    `json:"enabled"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	From     string  `json:"from"`
	Password *string `json:"password,omitempty"`
}

func (s Settings) config() (Config, error) {
	if !s.Enabled {
		return Config{}, nil
	}
	password := ""
	if s.Password != nil {
		password = *s.Password
	}
	return (rawSMTP{host: s.Host, port: strconv.Itoa(s.Port), username: s.Username, password: password, from: s.From}).parse()
}

// StoredConfig opens the complete configuration; absent means the environment is still authoritative.
func StoredConfig(ctx context.Context, tx pg.Tx, keys *secret.Keyring) (Config, bool, error) {
	var sealed secret.Sealed
	err := tx.QueryRow(ctx, `SELECT ciphertext,nonce,wrapped_dek,key_version FROM platform.smtp_config WHERE id=$1`, uuid.Nil()).Scan(&sealed.Ciphertext, &sealed.Nonce, &sealed.WrappedDEK, &sealed.KeyVersion)
	if errors.Is(err, pg.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	plain, err := keys.Open(sealed, smtpLocation)
	if err != nil {
		return Config{}, false, err
	}
	var s Settings
	if err = jsonx.Unmarshal([]byte(plain.Reveal()), &s); err != nil {
		return Config{}, false, err
	}
	cfg, err := s.config()
	return cfg, true, err
}

// SaveConfig serializes edits and their audit. Changing a destination cannot forward the previous password to it.
func SaveConfig(ctx context.Context, tx pg.Tx, keys *secret.Keyring, actor audit.Actor, s Settings, reason string) (Config, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('platform.smtp_config'))`); err != nil {
		return Config{}, err
	}
	old, _, err := StoredConfig(ctx, tx, keys)
	if err != nil {
		return Config{}, err
	}
	s, err = s.withSavedPassword(old)
	if err != nil {
		return Config{}, err
	}
	cfg, err := s.config()
	if err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	if !s.Enabled {
		s = Settings{}
	}
	body, err := jsonx.MarshalCanonical(s)
	if err != nil {
		return Config{}, err
	}
	sealed := keys.Seal(secret.New(string(body)), smtpLocation)
	_, err = tx.Exec(ctx, `INSERT INTO platform.smtp_config(id,ciphertext,nonce,wrapped_dek,key_version) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(id) DO UPDATE SET ciphertext=excluded.ciphertext,nonce=excluded.nonce,wrapped_dek=excluded.wrapped_dek,key_version=excluded.key_version`, uuid.Nil(), sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion)
	if err != nil {
		return Config{}, err
	}
	err = audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "smtp_config.update", Reason: reason, Target: audit.Target{Module: "platform", Kind: "smtp_config", ID: uuid.Nil().String()}, Detail: map[string]any{"enabled": s.Enabled}})
	return cfg, err
}

// TestConfig sends one fixed test message and returns only a stable, non-sensitive outcome.
func TestConfig(ctx context.Context, cfg Config, to string, logger *slog.Logger) string {
	if !cfg.Configured() {
		return "disabled"
	}
	return newSender(cfg).testConfig(ctx, to, logger)
}

func (s *sender) testConfig(ctx context.Context, to string, logger *slog.Logger) string {
	_, err := s.Send(ctx, Message{To: to, Subject: "Axiom SMTP configuration test", Text: "Axiom SMTP configuration test.", HTML: "<p>Axiom SMTP configuration test.</p>"})
	if err != nil {
		logger.WarnContext(ctx, "SMTP configuration test failed", "request_id", httpserver.RequestID(ctx), "err", smtpDiagnostic(err))
	}
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrRejected):
		return "rejected"
	default:
		return "failed"
	}
}

func (s Settings) withSavedPassword(old Config) (Settings, error) {
	if s.Password != nil || !s.Enabled || old.Password.Reveal() == "" {
		return s, nil
	}
	if s.Host != old.Host || s.Port != old.Port || s.Username != old.Username {
		return s, fmt.Errorf("%w: reenter password when changing destination", ErrInvalidConfig)
	}
	password := old.Password.Reveal()
	s.Password = &password
	return s, nil
}
