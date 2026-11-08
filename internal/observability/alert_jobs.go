package observability

import (
	"context"
	"errors"
	"html"
	"time"
	"uuid"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/mail"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

type alertArgs struct{}

type spendArgs struct{}

type receiptArgs struct {
	Receipt uuid.UUID `json:"receipt"`
}

// Each InsertOpts is written out rather than shared: TestEveryJobKindNamesOneOfTheQueues reads the queue from the
// method's own literal, and a job kind it cannot read fails the table.
func (alertArgs) Kind() string { return "observability.alerts" }

func (alertArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

func (spendArgs) Kind() string { return "observability.spend" }
func (spendArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: pg.QueueNotifications, MaxAttempts: 5, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled, rivertype.JobStatePending}}}
}

// RegisterAlerts registers the persistent signal consumer and the spend detector before the queue starts.
func RegisterAlerts(db *pg.DB, scope pg.Scope, q *pg.Queue, m *mail.Mailer, keys *secret.Keyring, exit *egress.Client) error {
	a := &alertWorker{db: db, scope: scope, queue: q, mail: m, now: time.Now}
	m.Receipt = a.deliveryReceipt
	q.OnDiscard = a.discarded
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(a.work)); err != nil {
		return err
	}
	w := &webhookWorker{alertWorker: a, keys: keys, exit: exit}
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(w.work)); err != nil {
		return err
	}
	s := &spendWorker{db: db, scope: scope, now: time.Now}
	if err := river.AddWorkerSafely(q.Workers(), river.WorkFunc(s.work)); err != nil {
		return err
	}
	q.Every(time.Minute, func() river.JobArgs { return alertArgs{} })
	q.Every(spendEvery, func() river.JobArgs { return spendArgs{} })
	return nil
}

type spendWorker struct {
	db    *pg.DB
	scope pg.Scope
	now   func() time.Time
}

func (s *spendWorker) work(ctx context.Context, _ *river.Job[spendArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return s.db.Tx(ctx, s.scope, func(tx pg.Tx) error {
		_, _, err := detectSpendAnomalies(ctx, tx, s.now())
		return err
	})
}

type alertWorker struct {
	db    *pg.DB
	scope pg.Scope
	mail  *mail.Mailer
	queue *pg.Queue
	now   func() time.Time
}

func (a *alertWorker) work(ctx context.Context, _ *river.Job[alertArgs]) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	now := a.now().UTC()
	if err := a.db.Tx(ctx, a.scope, func(tx pg.Tx) error {
		if err := reportChannelHealth(ctx, tx); err != nil {
			return err
		}
		return alert.Consume(ctx, tx, func(s alert.Signal) error { return applyAlert(ctx, tx, s, now) })
	}); err != nil {
		return err
	}
	return a.db.Tx(ctx, a.scope, func(tx pg.Tx) error {
		if err := scheduleAlertTests(ctx, tx, now); err != nil {
			return err
		}
		if err := repairAlertReceipts(ctx, tx, now); err != nil {
			return err
		}
		if err := evaluateAlerts(ctx, tx, now); err != nil {
			return err
		}
		if err := a.dispatch(ctx, tx, now); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM observability.alert_events WHERE id IN (
 SELECT id FROM observability.alert_events WHERE resolved_at<$1 AND NOT pending_recovery ORDER BY resolved_at,id LIMIT 100)`, now.Add(-30*24*time.Hour))
		return err
	})
}

const alertEvaluation = `SELECT e.id,e.kind,e.key,e.severity,e.fields,e.occurrences,e.state,e.fired_at,e.resolved_at FROM observability.alert_events e
 WHERE resolved_at IS NULL OR pending_recovery
 ORDER BY checked_at,id LIMIT 100`

func evaluateAlerts(ctx context.Context, tx pg.Tx, now time.Time) error {
	c, err := readAlertConfiguration(ctx, tx, true)
	if err != nil {
		return err
	}
	events, err := readAlertEvents(ctx, tx, alertEvaluation)
	if err != nil {
		return err
	}
	channels := map[string]AlertChannel{}
	for _, ch := range c.Channels {
		channels[ch.ID] = ch
	}
	for _, e := range events {
		if err = evaluateIncident(ctx, tx, e, c.Rules, channels, now); err != nil {
			return err
		}
	}
	return nil
}

func evaluateIncident(ctx context.Context, tx pg.Tx, e AlertEvent, rules []AlertRule, channels map[string]AlertChannel, now time.Time) error {
	if e.ResolvedAt != nil {
		if err := recoverAlerts(ctx, tx, e, now); err != nil {
			return err
		}
	} else {
		tellers, err := tellIncident(ctx, tx, e, rules, channels, now)
		if err != nil {
			return err
		}
		if noticeKind(e.Kind) {
			if err := closeTold(ctx, tx, e, tellers, now); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE observability.alert_events e SET checked_at=$2,pending_recovery=resolved_at IS NOT NULL AND (
 EXISTS(SELECT FROM observability.alert_receipts WHERE event_id=e.id AND state IN ('pending','queued')) OR
 (NOT $3 AND EXISTS(SELECT FROM observability.alert_receipts r WHERE r.event_id=e.id AND r.sequence=0 AND
 NOT EXISTS(SELECT FROM observability.alert_receipts recovery WHERE recovery.event_id=e.id AND recovery.rule_id=r.rule_id AND recovery.sequence=-1)))) WHERE id=$1`, e.ID, now, noticeKind(e.Kind))
	return err
}

// tellIncident evaluates open incident e against every rule on its kind whose channel exists, and returns the IDs of
// the rules that must tell it (mustTell); a rule that does not hold for it now has its duration start over.
func tellIncident(ctx context.Context, tx pg.Tx, e AlertEvent, rules []AlertRule, channels map[string]AlertChannel, now time.Time) ([]string, error) {
	var tellers []string
	for _, r := range rules {
		ch, ok := channels[r.Channel]
		if !ok || r.Kind != e.Kind {
			continue
		}
		if !mustTell(e, r, ch) {
			if err := forgetWindow(ctx, tx, e, r); err != nil {
				return nil, err
			}
			continue
		}
		tellers = append(tellers, r.ID)
		if err := evaluateRule(ctx, tx, e, r, ch, now); err != nil {
			return nil, err
		}
	}
	return tellers, nil
}

// mustTell reports whether rule r, delivering on ch, must tell incident e: its whole condition holds for e (kind,
// minimum severity, threshold), and ch is not the channel e reports unavailable. It is the one answer to "who must be
// told": evaluation tells e through exactly these rules once their duration has passed, and a notice closes only when
// each of them has (closeTold).
func mustTell(e AlertEvent, r AlertRule, ch AlertChannel) bool {
	return eligibleAlert(e, r) && (e.Kind != "alert.channel_unavailable" || e.Key != ch.ID)
}

// forgetWindow drops how long e has met r, for a rule that does not hold for it now: its duration starts over.
func forgetWindow(ctx context.Context, tx pg.Tx, e AlertEvent, r AlertRule) error {
	_, err := tx.Exec(ctx, `DELETE FROM observability.alert_windows WHERE event_id=$1 AND rule_id=$2`, e.ID, r.ID)
	return err
}

// evaluateRule makes r's next notification of e when r's duration has passed and the notification is due; r must tell
// e (mustTell).
func evaluateRule(ctx context.Context, tx pg.Tx, e AlertEvent, r AlertRule, ch AlertChannel, now time.Time) error {
	breached, err := alertBreached(ctx, tx, e, r, now)
	if err != nil || !breached {
		return err
	}
	seq, ready, err := nextAlertSequence(ctx, tx, e.ID, r, now)
	if err != nil || !ready {
		return err
	}
	raw, err := jsonx.MarshalCanonical(ch)
	if err != nil {
		return err
	}
	fields, err := jsonx.MarshalCanonical(e.Fields)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observability.alert_receipts(id,event_id,rule_id,sequence,channel,quiet_start,quiet_end,fields,at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(event_id,rule_id,sequence) DO NOTHING`, uuid.NewV7(), e.ID, r.ID, seq, raw, r.QuietStart, r.QuietEnd, fields, now)
	return err
}

// alertBreached reports whether r, which holds for e, has held for its duration, starting the window the first time.
func alertBreached(ctx context.Context, tx pg.Tx, e AlertEvent, r AlertRule, now time.Time) (bool, error) {
	var since time.Time
	err := tx.QueryRow(ctx, `INSERT INTO observability.alert_windows(event_id,rule_id,since) VALUES($1,$2,$3) ON CONFLICT(event_id,rule_id) DO UPDATE SET since=observability.alert_windows.since RETURNING since`, e.ID, r.ID, now).Scan(&since)
	return now.Sub(since) >= time.Duration(r.DurationSeconds)*time.Second, err
}

func nextAlertSequence(ctx context.Context, tx pg.Tx, id uuid.UUID, r AlertRule, now time.Time) (int, bool, error) {
	var last AlertReceipt
	err := tx.QueryRow(ctx, `SELECT sequence,state,at,delivered_at FROM observability.alert_receipts WHERE event_id=$1 AND rule_id=$2 ORDER BY sequence DESC LIMIT 1`, id, r.ID).Scan(&last.Sequence, &last.State, &last.At, &last.DeliveredAt)
	if errors.Is(err, pg.ErrNoRows) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	since := last.At
	if last.DeliveredAt != nil {
		since = *last.DeliveredAt
	}
	ready := r.RepeatSeconds > 0 && (last.State == "delivered" || last.State == "failed") && now.Sub(since) >= time.Duration(r.RepeatSeconds)*time.Second
	return last.Sequence + 1, ready, nil
}

type alertDelivery struct {
	id         uuid.UUID
	eventID    uuid.UUID
	ch         AlertChannel
	start, end int
	kind, key  string
	severity   string
	fields     []byte
	seq        int
	payload    []byte
}

func pendingAlertDeliveries(ctx context.Context, tx pg.Tx, now time.Time) ([]alertDelivery, error) {
	rows, err := tx.Query(ctx, `SELECT r.id,e.id,r.channel,r.quiet_start,r.quiet_end,e.kind,e.key,r.sequence,e.severity,coalesce(r.fields,e.fields) FROM observability.alert_receipts r JOIN observability.alert_events e ON e.id=r.event_id WHERE r.state='pending' AND r.not_before<=$1 ORDER BY r.at,r.id LIMIT 100 FOR UPDATE OF r SKIP LOCKED`, now)
	if err != nil {
		return nil, err
	}
	var deliveries []alertDelivery
	for rows.Next() {
		var d alertDelivery
		var raw []byte
		err = rows.Scan(&d.id, &d.eventID, &raw, &d.start, &d.end, &d.kind, &d.key, &d.seq, &d.severity, &d.fields)
		if err != nil {
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

func (a *alertWorker) dispatch(ctx context.Context, tx pg.Tx, now time.Time) error {
	deliveries, err := pendingAlertDeliveries(ctx, tx, now)
	if err != nil {
		return err
	}
	for _, d := range deliveries {
		if err = a.dispatchOne(ctx, tx, now, d); err != nil {
			return err
		}
	}
	return nil
}

func (a *alertWorker) dispatchOne(ctx context.Context, tx pg.Tx, now time.Time, d alertDelivery) error {
	var err error

	if wait := alertQuiet(now, d.start, d.end); wait > 0 {
		if _, err = tx.Exec(ctx, `UPDATE observability.alert_receipts SET not_before=$2 WHERE id=$1`, d.id, now.Add(wait)); err != nil {
			return err
		}
		return nil
	}
	if d.ch.WebhookURL != "" {
		return a.dispatchWebhook(ctx, tx, d)
	}
	phase := "firing"
	if d.seq == -1 {
		phase = "resolved"
	}
	body := "Axiom alert " + phase + ": " + d.kind + " (" + d.key + ")\nSeverity: " + d.severity + "\nFields: " + string(d.fields) + "\nNotification: " + d.id.String()
	msg := mail.Message{To: d.ch.Email, Subject: "Axiom alert " + phase, Text: body, HTML: "<pre>" + html.EscapeString(body) + "</pre>"}
	if err = a.mail.EnqueueTracked(ctx, tx, mail.Tenant{}, msg, d.id); err != nil {
		if !errors.Is(err, mail.ErrNotConfigured) && !errors.Is(err, mail.ErrInvalidMessage) {
			return err
		}
		if err = recordAlertReceipt(ctx, tx, d.id, false, now); err != nil {
			return err
		}
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE observability.alert_receipts SET state='queued' WHERE id=$1`, d.id); err != nil {
		return err
	}
	return nil
}

func (a *alertWorker) deliveryReceipt(ctx context.Context, id uuid.UUID, delivered bool) error {
	return a.db.Tx(ctx, a.scope, func(tx pg.Tx) error {
		return recordAlertReceipt(ctx, tx, id, delivered, a.now().UTC())
	})
}

// noticeKind reports the incidents that tell one fact rather than a fault, a channel test or the daily audit digest
// (docs/domains/operations.md「审计」): nothing recovers, so closing one sends no recovery and does not wait for one.
// An open one closes itself once every rule that must tell it has (closeTold); its producer never resolves it, so a
// backlog that holds its signal back cannot close it before it was told.
func noticeKind(kind string) bool {
	return kind == "alert.test" || kind == "operations.audit_digest"
}

// closeTold closes notice e once every rule that must tell it (tellers, by ID: mustTell) has made its first
// notification and none of its notifications is still to be sent; with none it closes at once, as there is nobody to
// tell. Only evaluation calls it, after the rules were evaluated, so no notice closes before it was weighed.
func closeTold(ctx context.Context, tx pg.Tx, e AlertEvent, tellers []string, now time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE observability.alert_events e SET resolved_at=$2,state='resolved' WHERE id=$1 AND resolved_at IS NULL
 AND NOT EXISTS(SELECT FROM observability.alert_receipts WHERE event_id=e.id AND state IN ('pending','queued'))
 AND NOT EXISTS(SELECT FROM unnest($3::text[]) m(rule_id) WHERE NOT EXISTS(
 SELECT FROM observability.alert_receipts r WHERE r.event_id=e.id AND r.rule_id=m.rule_id AND r.sequence=0))`, e.ID, now, tellers)
	return err
}

func recoverAlerts(ctx context.Context, tx pg.Tx, e AlertEvent, now time.Time) error {
	if noticeKind(e.Kind) {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO observability.alert_receipts(id,event_id,rule_id,sequence,channel,quiet_start,quiet_end,fields,at,state)
 SELECT uuidv7(),r.event_id,r.rule_id,-1,r.channel,r.quiet_start,r.quiet_end,e.fields,$2,CASE WHEN r.state='delivered' THEN 'pending' ELSE 'failed' END
 FROM observability.alert_receipts r JOIN observability.alert_events e ON e.id=r.event_id
 WHERE r.event_id=$1 AND r.sequence=0 AND r.state IN ('delivered','failed')
 AND NOT EXISTS(SELECT FROM observability.alert_receipts pending WHERE pending.event_id=r.event_id AND pending.rule_id=r.rule_id AND pending.sequence>=0 AND pending.state IN ('pending','queued'))
 ON CONFLICT(event_id,rule_id,sequence) DO NOTHING`, e.ID, now)
	return err
}

func (a *alertWorker) discarded(ctx context.Context, tx pg.Tx, kind string, args []byte) error {
	if err := alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "queue.discarded", Key: kind, Severity: "critical", Fields: map[string]string{"value": "1"}}); err != nil {
		return err
	}
	if kind == "mail.send_tracked" || kind == "observability.webhook" {
		var tracked receiptArgs
		if err := jsonx.Unmarshal(args, &tracked); err != nil {
			return err
		}
		if tracked.Receipt != uuid.Nil() {
			if err := recordAlertReceipt(ctx, tx, tracked.Receipt, false, a.now().UTC()); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *alertWorker) dispatchWebhook(ctx context.Context, tx pg.Tx, d alertDelivery) error {
	body, err := webhookPayload(d)
	if err != nil {
		return err
	}
	if _, err = a.queue.InsertManyTx(ctx, tx, []river.InsertManyParams{{Args: webhookArgs{Receipt: d.id}}}); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE observability.alert_receipts SET state='queued',payload=$2 WHERE id=$1`, d.id, body)
	return err
}
