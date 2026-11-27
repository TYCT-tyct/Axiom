package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// RotateAlertWebhookSecret sets the secret of a channel whose kind takes one: a webhook's signing secret, a Feishu
// bot's signing secret or a Telegram bot's token. It binds the secret to the destination, never to a reusable channel
// name alone. A kind with an overlap keeps the old secret valid for it, and the next rotation waits for its end, which
// keeps every promised old key valid and the headers bounded; a kind without one drops the old secret at once.
func RotateAlertWebhookSecret(ctx context.Context, tx pg.Tx, keys *secret.Keyring, channel string, rev int64, value secret.Secret, now time.Time) (uuid.UUID, error) {
	var current int64
	if err := tx.QueryRow(ctx, `SELECT rev FROM observability.alert_configuration WHERE singleton FOR UPDATE`).Scan(&current); err != nil {
		return uuid.Nil(), err
	}
	if current != rev {
		return uuid.Nil(), ErrAlertRevision
	}
	c, err := GetAlertConfiguration(ctx, tx)
	if err != nil {
		return uuid.Nil(), err
	}
	for _, ch := range c.Channels {
		if k, _, ok := kindOf(ch); ok && ch.ID == channel && k.secret.accepts(value) {
			return rotateWebhookKey(ctx, tx, keys, ch, k.secret, value, now)
		}
	}
	return uuid.Nil(), ErrAlertConfiguration
}

func (r secretRule) accepts(value secret.Secret) bool {
	v := value.Reveal()
	return r.max > 0 && len(v) >= r.min && len(v) <= r.max && (r.form == nil || r.form.MatchString(v))
}

func rotateWebhookKey(ctx context.Context, tx pg.Tx, keys *secret.Keyring, ch AlertChannel, rule secretRule, value secret.Secret, now time.Time) (uuid.UUID, error) {
	dest := destination(ch)
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM observability.webhook_keys WHERE channel_id=$1 AND destination=$2 AND expires_at>$3)`, ch.ID, dest, now).Scan(&overlap); err != nil {
		return uuid.Nil(), err
	}
	if overlap && rule.overlap > 0 {
		return uuid.Nil(), ErrAlertRotationWindow
	}
	if _, err := tx.Exec(ctx, `UPDATE observability.webhook_keys SET expires_at=$3 WHERE channel_id=$1 AND destination=$2 AND (expires_at IS NULL OR expires_at>$3)`, ch.ID, dest, now.Add(rule.overlap)); err != nil {
		return uuid.Nil(), err
	}
	id := uuid.NewV7()
	rowID := webhookKeyRow(id.String(), ch)
	s := keys.Seal(value, webhookKeyLocation(rowID))
	_, err := tx.Exec(ctx, `INSERT INTO observability.webhook_keys(id,channel_id,destination,ciphertext,nonce,wrapped_dek,key_version) VALUES($1,$2,$3,$4,$5,$6,$7)`, rowID, ch.ID, dest, s.Ciphertext, s.Nonce, s.WrappedDEK, int64(s.KeyVersion))
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE observability.alert_configuration SET rev=rev+1 WHERE singleton`)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE observability.channel_tests SET next_due=$2 WHERE channel_id=$1`, ch.ID, now)
	}
	return id, err
}

func webhookKeyRow(id string, ch AlertChannel) string {
	binding := sha256.Sum256([]byte(ch.ID + "\x00" + destination(ch)))
	return id + "/" + hex.EncodeToString(binding[:])
}

func webhookKeyLocation(id string) secret.Location {
	return secret.Location{Table: "observability.webhook_keys", RowID: id, Purpose: "signing"}
}

func readWebhookKeys(ctx context.Context, tx pg.Tx, keys *secret.Keyring, ch AlertChannel, now time.Time) ([]secret.Secret, error) {
	rows, err := tx.Query(ctx, `SELECT id,ciphertext,nonce,wrapped_dek,key_version FROM observability.webhook_keys WHERE channel_id=$1 AND destination=$2 AND (expires_at IS NULL OR expires_at>$3) ORDER BY id DESC`, ch.ID, destination(ch), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []secret.Secret
	for rows.Next() {
		var id string
		var s secret.Sealed
		if err = rows.Scan(&id, &s.Ciphertext, &s.Nonce, &s.WrappedDEK, &s.KeyVersion); err != nil {
			return nil, err
		}
		base, _, _ := strings.Cut(id, "/")
		if id != webhookKeyRow(base, ch) {
			return nil, errors.New("webhook: signing key binding changed")
		}
		value, err := keys.Open(s, webhookKeyLocation(id))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
