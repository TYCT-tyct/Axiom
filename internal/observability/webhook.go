package observability

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/riverqueue/river"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

type webhookArgs struct {
	Receipt uuid.UUID `json:"receipt"`
}

func (webhookArgs) Kind() string { return "observability.webhook" }
func (webhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

type webhookWorker struct {
	*alertWorker
	keys *secret.Keyring
	exit *egress.Client
}

// Webhook retries use River's persisted backoff; returning only a category keeps URLs and response text out of jobs.
func (w *webhookWorker) work(ctx context.Context, job *river.Job[webhookArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, keys, terminal, err := w.load(ctx, job.Args.Receipt)
	if err != nil {
		return errors.New("webhook: cannot load delivery")
	}
	if terminal {
		return nil
	}
	if err = w.send(ctx, d, keys); err != nil {
		if job.Attempt >= job.MaxAttempts {
			if receiptErr := w.deliveryReceipt(ctx, d.id, false); receiptErr != nil {
				return errors.New("webhook: cannot persist failed receipt")
			}
		}
		return err
	}
	if err = w.deliveryReceipt(ctx, d.id, true); err != nil {
		return errors.New("webhook: cannot persist delivered receipt")
	}
	return nil
}

func webhookPayload(d alertDelivery) ([]byte, error) {
	phase := "firing"
	if d.seq == -1 {
		phase = "resolved"
	}
	if d.kind == "alert.test" {
		phase = "test"
	}
	// Fields and severity are frozen at dispatch; the incident may receive further signals during retries.
	return jsonx.MarshalCanonical(struct {
		ID       uuid.UUID `json:"id"`
		EventID  uuid.UUID `json:"event_id"`
		Sequence int       `json:"sequence"`
		Kind     string    `json:"kind"`
		Key      string    `json:"key"`
		Severity string    `json:"severity"`
		State    string    `json:"state"`
		Fields   jsonx.Raw `json:"fields"`
	}{d.id, d.eventID, d.seq, d.kind, d.key, d.severity, phase, d.fields})
}

func (w *webhookWorker) send(ctx context.Context, d alertDelivery, keys []secret.Secret) error {
	if d.ch.WebhookURL == "" || d.ch.Email != "" || validateAlertChannel(d.ch) != nil {
		return errors.New("webhook: invalid destination")
	}
	if len(keys) == 0 {
		return errors.New("webhook: signing key unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.ch.WebhookURL, bytes.NewReader(d.payload))
	if err != nil {
		return errors.New("webhook: invalid destination")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Axiom-Signature", webhookSignature(d.payload, keys, w.now().UTC()))
	resp, err := w.exit.Do(req)
	if err != nil {
		return errors.New("webhook: request failed")
	}
	defer resp.Body.Close() //nolint:errcheck // A response body is never used as a receipt.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: HTTP status %d", resp.StatusCode)
	}
	return nil
}

func webhookSignature(body []byte, keys []secret.Secret, now time.Time) string {
	stamp := strconv.FormatInt(now.Unix(), 10)
	var header strings.Builder
	header.WriteString("t=" + stamp)
	for _, key := range keys {
		mac := hmac.New(sha256.New, []byte(key.Reveal()))
		mac.Write([]byte(stamp + "."))
		mac.Write(body)
		header.WriteString(",v1=" + hex.EncodeToString(mac.Sum(nil)))
	}
	return header.String()
}

func (w *webhookWorker) load(ctx context.Context, id uuid.UUID) (alertDelivery, []secret.Secret, bool, error) {
	var d alertDelivery
	var keys []secret.Secret
	var terminal bool
	err := w.db.ReadTx(ctx, w.scope, func(tx pg.Tx) error {
		var raw []byte
		var state string
		err := tx.QueryRow(ctx, `SELECT id,channel,payload,state FROM observability.alert_receipts WHERE id=$1`, id).Scan(&d.id, &raw, &d.payload, &state)
		if errors.Is(err, pg.ErrNoRows) || state == "delivered" || state == "failed" {
			terminal = true
			return nil
		}
		if err != nil {
			return err
		}
		if err = jsonx.Unmarshal(raw, &d.ch); err != nil {
			return err
		}
		keys, err = readWebhookKeys(ctx, tx, w.keys, d.ch, w.now().UTC())
		return err
	})
	return d, keys, terminal, err
}
