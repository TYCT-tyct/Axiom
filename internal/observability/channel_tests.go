package observability

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func scheduleAlertTests(ctx context.Context, tx pg.Tx, now time.Time) error {
	// Take the exclusive lock before rule evaluation takes its shared configuration lock.
	var rev int64
	if err := tx.QueryRow(ctx, `SELECT rev FROM observability.alert_configuration WHERE singleton FOR UPDATE`).Scan(&rev); err != nil {
		return err
	}
	c, err := GetAlertConfiguration(ctx, tx)
	if err != nil {
		return err
	}
	for _, ch := range c.Channels {
		if err = scheduleChannelTest(ctx, tx, ch, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `DELETE FROM observability.webhook_keys k WHERE expires_at<=$1 OR (
 NOT EXISTS(SELECT FROM observability.alert_configuration c,jsonb_array_elements(c.body->'channels') ch WHERE ch->>'id'=k.channel_id AND ch->>'webhook_url'=k.destination)
 AND NOT EXISTS(SELECT FROM observability.alert_receipts r JOIN observability.alert_events e ON e.id=r.event_id
 WHERE (r.state IN ('pending','queued') OR e.resolved_at IS NULL OR e.pending_recovery)
 AND r.channel->>'id'=k.channel_id AND r.channel->>'webhook_url'=k.destination))`, now)
	return err
}

func repairAlertReceipts(ctx context.Context, tx pg.Tx, now time.Time) error {
	deliveries, err := abandonedAlertDeliveries(ctx, tx, now)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		kind := "mail.send_tracked"
		if d.ch.WebhookURL != "" {
			kind = "observability.webhook"
		}
		pending, err := pg.HasPendingJob(ctx, tx, kind, receiptArgs{Receipt: d.id})
		if err != nil {
			return err
		}
		if !pending {
			if err = recordAlertReceipt(ctx, tx, d.id, false, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func scheduleChannelTest(ctx context.Context, tx pg.Tx, ch AlertChannel, now time.Time) error {
	destination := ch.Email + ch.WebhookURL
	var due bool
	err := tx.QueryRow(ctx, `SELECT destination<>$2 OR (next_due<=$3 AND NOT EXISTS(SELECT FROM observability.alert_receipts WHERE id=receipt_id AND state IN ('pending','queued'))) FROM observability.channel_tests WHERE channel_id=$1`, ch.ID, destination, now).Scan(&due)
	if err != nil && !errors.Is(err, pg.ErrNoRows) {
		return err
	}
	if err == nil && !due {
		return nil
	}
	id, err := SendAlertTest(ctx, tx, ch.ID, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observability.channel_tests(channel_id,destination,receipt_id,next_due) VALUES($1,$2,$3,$4)
 ON CONFLICT(channel_id) DO UPDATE SET destination=EXCLUDED.destination,receipt_id=EXCLUDED.receipt_id,next_due=EXCLUDED.next_due,reported_state=''`, ch.ID, destination, id, now.Add(time.Hour))
	return err
}

func recordAlertReceipt(ctx context.Context, tx pg.Tx, id uuid.UUID, delivered bool, now time.Time) error {
	state := "failed"
	if delivered {
		state = "delivered"
	}
	tag, err := tx.Exec(ctx, `UPDATE observability.alert_receipts SET state=$2,delivered_at=CASE WHEN $2='delivered' THEN $3::timestamptz ELSE NULL END WHERE id=$1 AND state<>'delivered'`, id, state, now)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE observability.alert_events SET state='delivered' WHERE resolved_at IS NULL AND id=(SELECT event_id FROM observability.alert_receipts WHERE id=$1 AND sequence>=0 AND state='delivered')`, id)
	return err
}

func abandonedAlertDeliveries(ctx context.Context, tx pg.Tx, now time.Time) ([]alertDelivery, error) {
	rows, err := tx.Query(ctx, `SELECT id,channel FROM observability.alert_receipts WHERE state='queued' AND at<$1 ORDER BY at,id LIMIT 100 FOR UPDATE SKIP LOCKED`, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	var deliveries []alertDelivery
	for rows.Next() {
		var d alertDelivery
		var raw []byte
		if err = rows.Scan(&d.id, &raw); err != nil {
			break
		}
		if err = jsonx.Unmarshal(raw, &d.ch); err != nil {
			break
		}
		deliveries = append(deliveries, d)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return deliveries, nil
}

func reportChannelHealth(ctx context.Context, tx pg.Tx) error {
	if err := retireChannelTests(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT t.channel_id,r.state FROM observability.channel_tests t JOIN observability.alert_receipts r ON r.id=t.receipt_id,observability.alert_configuration c
 WHERE r.state IN ('delivered','failed') AND r.state<>t.reported_state AND EXISTS(SELECT FROM jsonb_array_elements(c.body->'channels') ch WHERE ch->>'id'=t.channel_id AND coalesce(ch->>'email','')||coalesce(ch->>'webhook_url','')=t.destination)
 ORDER BY t.channel_id LIMIT 32 FOR UPDATE OF t SKIP LOCKED`)
	if err != nil {
		return err
	}
	var reports []struct{ channel, state string }
	for rows.Next() {
		var r struct{ channel, state string }
		if err = rows.Scan(&r.channel, &r.state); err != nil {
			break
		}
		reports = append(reports, r)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range reports {
		if _, err = tx.Exec(ctx, `UPDATE observability.channel_tests SET reported_state=$2 WHERE channel_id=$1`, r.channel, r.state); err != nil {
			return err
		}
		if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "alert.channel_unavailable", Key: r.channel, Severity: "critical", Resolved: r.state == "delivered"}); err != nil {
			return err
		}
	}
	return nil
}

// Resolve retired channels in the signal phase, before evaluation takes configuration/event locks.
func retireChannelTests(ctx context.Context, tx pg.Tx) error {
	rows, err := tx.Query(ctx, `DELETE FROM observability.channel_tests t WHERE channel_id IN (
 SELECT t.channel_id FROM observability.channel_tests t WHERE NOT EXISTS (
 SELECT FROM observability.alert_configuration c,jsonb_array_elements(c.body->'channels') ch WHERE ch->>'id'=t.channel_id)
 ORDER BY channel_id LIMIT 32 FOR UPDATE SKIP LOCKED) RETURNING channel_id`)
	if err != nil {
		return err
	}
	var channels []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		channels = append(channels, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range channels {
		if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "alert.channel_unavailable", Key: id, Severity: "info", Resolved: true}); err != nil {
			return err
		}
	}
	return nil
}
