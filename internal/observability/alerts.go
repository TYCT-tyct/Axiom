package observability

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// AlertRule selects a signal and one channel. Zero repeat means one notice per incident.
type AlertRule struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	MinimumSeverity string  `json:"minimum_severity"`
	Threshold       float64 `json:"threshold"`
	DurationSeconds int     `json:"duration_seconds"`
	Channel         string  `json:"channel"`
	QuietStart      int     `json:"quiet_start"`
	QuietEnd        int     `json:"quiet_end"`
	RepeatSeconds   int     `json:"repeat_seconds"`
}

// AlertChannel is a platform operator destination; its mail uses the reserved zero-site outbox.
type AlertChannel struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// AlertConfiguration is replaced atomically with optimistic concurrency.
type AlertConfiguration struct {
	Rev      int64          `json:"rev"`
	Rules    []AlertRule    `json:"rules"`
	Channels []AlertChannel `json:"channels"`
}

// AlertEvent is an incident, with independent receipts for each destination.
type AlertEvent struct {
	ID          uuid.UUID         `json:"id"`
	Kind        string            `json:"kind"`
	Key         string            `json:"key"`
	Severity    string            `json:"severity"`
	Fields      map[string]string `json:"fields"`
	Occurrences int64             `json:"occurrences"`
	State       string            `json:"state"`
	FiredAt     time.Time         `json:"fired_at"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
	Receipts    []AlertReceipt    `json:"receipts"`
}

// AlertReceipt survives River's completed-job retention.
type AlertReceipt struct {
	ID          uuid.UUID  `json:"id"`
	RuleID      string     `json:"rule_id"`
	Sequence    int        `json:"sequence"`
	State       string     `json:"state"`
	At          time.Time  `json:"at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

var (
	// ErrAlertConfiguration rejects invalid rules or destinations at the admin boundary.
	ErrAlertConfiguration = errors.New("observability: invalid alert configuration")
	// ErrAlertRevision refuses a stale configuration replacement.
	ErrAlertRevision = errors.New("observability: stale alert configuration revision")
)

// GetAlertConfiguration reads the platform's rules and channels in the caller's authorized scope.
func GetAlertConfiguration(ctx context.Context, tx pg.Tx) (AlertConfiguration, error) {
	return readAlertConfiguration(ctx, tx, false)
}

func readAlertConfiguration(ctx context.Context, tx pg.Tx, lock bool) (AlertConfiguration, error) {
	var c AlertConfiguration
	var raw []byte
	var rev int64
	query := `SELECT rev,body FROM observability.alert_configuration WHERE singleton`
	if lock {
		query += ` FOR SHARE`
	}
	err := tx.QueryRow(ctx, query).Scan(&rev, &raw)
	if err == nil {
		err = jsonx.Unmarshal(raw, &c)
	}
	c.Rev = rev
	return c, err
}

// SetAlertConfiguration validates destinations and rejects a concurrent replacement.
func SetAlertConfiguration(ctx context.Context, tx pg.Tx, c AlertConfiguration) error {
	if err := validateAlertConfiguration(c); err != nil {
		return err
	}
	body, err := jsonx.MarshalCanonical(c)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE observability.alert_configuration SET rev=rev+1,body=$1 WHERE singleton AND rev=$2`, body, c.Rev)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrAlertRevision
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM observability.alert_windows`)
	return err
}

func validateAlertConfiguration(c AlertConfiguration) error {
	if len(c.Rules) > 32 || len(c.Channels) > 32 {
		return ErrAlertConfiguration
	}
	channels, rules := map[string]bool{}, map[string]bool{}
	destinations := map[[2]string]bool{}
	for _, ch := range c.Channels {
		if err := validateAlertChannel(ch); err != nil {
			return err
		}
		if channels[ch.ID] {
			return ErrAlertConfiguration
		}
		channels[ch.ID] = true
	}
	for _, r := range c.Rules {
		if err := validateAlertRule(r); err != nil {
			return err
		}
		if err := validateAlertRoute(r, channels, destinations); err != nil {
			return err
		}
		if rules[r.ID] {
			return ErrAlertConfiguration
		}
		rules[r.ID] = true
	}
	return nil
}

func validateAlertChannel(ch AlertChannel) error {
	if ch.ID == "" || len(ch.ID) > 64 || len(ch.Email) > 254 {
		return ErrAlertConfiguration
	}
	if _, err := mail.ParseAddress(ch.Email); err != nil {
		return ErrAlertConfiguration
	}
	return nil
}

func validateAlertRule(r AlertRule) error {
	if r.ID == "" || len(r.ID) > 64 || r.Kind == "" || len(r.Kind) > 128 || severity(r.MinimumSeverity) == 0 || r.Threshold < 0 {
		return ErrAlertConfiguration
	}
	for _, limit := range []struct{ value, max int }{{r.DurationSeconds, 86400}, {r.RepeatSeconds, 604800}, {r.QuietStart, 1439}, {r.QuietEnd, 1439}} {
		if limit.value < 0 || limit.value > limit.max {
			return ErrAlertConfiguration
		}
	}
	return nil
}

func severity(s string) int {
	switch s {
	case "info":
		return 1
	case "warning":
		return 2
	case "critical":
		return 3
	}
	return 0
}

func applyAlert(ctx context.Context, tx pg.Tx, s alert.Signal, now time.Time) error {
	if s.Resolved {
		_, err := tx.Exec(ctx, `UPDATE observability.alert_events SET resolved_at=$3,state='resolved',pending_recovery=true WHERE kind=$1 AND key=$2 AND resolved_at IS NULL`, s.Kind, s.Key, now)
		return err
	}
	create, err := createAlert(ctx, tx, s)
	if err != nil {
		return err
	}
	fields, err := jsonx.MarshalCanonical(s.Fields)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO observability.alert_events(id,kind,key,severity,fields,state,fired_at)
 SELECT $1,$2,$3,$4,$5,'firing',$6 WHERE $7 OR EXISTS(SELECT FROM observability.alert_events WHERE kind=$2 AND key=$3 AND resolved_at IS NULL) ON CONFLICT(kind,key) WHERE resolved_at IS NULL
 DO UPDATE SET severity=EXCLUDED.severity,fields=EXCLUDED.fields,occurrences=observability.alert_events.occurrences+1`, uuid.NewV7(), s.Kind, s.Key, s.Severity, fields, now, create)
	return err
}

// ListAlertEvents uses a UUID keyset and caps the page; receipts belong to the returned incidents only.
func ListAlertEvents(ctx context.Context, tx pg.Tx, before uuid.UUID) ([]AlertEvent, error) {
	events, err := readAlertEvents(ctx, tx, `SELECT id,kind,key,severity,fields,occurrences,state,fired_at,resolved_at
 FROM observability.alert_events WHERE ($1::uuid='00000000-0000-0000-0000-000000000000' OR id<$1) ORDER BY id DESC LIMIT 100`, before)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	receipts, err := alertReceipts(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i := range events {
		events[i].Receipts = receipts[events[i].ID]
	}
	return events, nil
}

func alertReceipts(ctx context.Context, tx pg.Tx, ids []uuid.UUID) (map[uuid.UUID][]AlertReceipt, error) {
	rows, err := tx.Query(ctx, `SELECT e.id,r.id,r.rule_id,r.sequence,r.state,r.at,r.delivered_at FROM unnest($1::uuid[]) e(id)
 CROSS JOIN LATERAL (SELECT * FROM observability.alert_receipts WHERE event_id=e.id ORDER BY at DESC,id DESC LIMIT 100) r ORDER BY e.id,r.at,r.id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	receipts := map[uuid.UUID][]AlertReceipt{}
	for _, id := range ids {
		receipts[id] = []AlertReceipt{}
	}
	for rows.Next() {
		var id uuid.UUID
		var r AlertReceipt
		if err = rows.Scan(&id, &r.ID, &r.RuleID, &r.Sequence, &r.State, &r.At, &r.DeliveredAt); err != nil {
			return nil, err
		}
		receipts[id] = append(receipts[id], r)
	}
	return receipts, rows.Err()
}

func createAlert(ctx context.Context, tx pg.Tx, s alert.Signal) (bool, error) {
	if s.Kind != "observability.write_lag" && s.Kind != "accounts.authentication_failures" {
		return true, nil
	}
	c, err := GetAlertConfiguration(ctx, tx)
	if err != nil {
		return false, err
	}
	for _, r := range c.Rules {
		if eligibleAlert(AlertEvent{Kind: s.Kind, Severity: s.Severity, Fields: s.Fields}, r) {
			return true, nil
		}
	}
	return false, nil
}

func eligibleAlert(e AlertEvent, r AlertRule) bool {
	value := float64(1)
	if v, ok := e.Fields["value"]; ok {
		var err error
		value, err = strconv.ParseFloat(v, 64)
		if err != nil {
			return false
		}
	}
	return e.Kind == r.Kind && severity(e.Severity) >= severity(r.MinimumSeverity) && value >= r.Threshold
}

func alertQuiet(now time.Time, start, end int) time.Duration {
	now = now.UTC()
	minute := now.Hour()*60 + now.Minute()
	quiet := start < end && minute >= start && minute < end || start > end && (minute >= start || minute < end)
	if !quiet {
		return 0
	}
	until := time.Date(now.Year(), now.Month(), now.Day(), end/60, end%60, 0, 0, time.UTC)
	if !until.After(now) {
		until = until.Add(24 * time.Hour)
	}
	return until.Sub(now)
}

// SendAlertTest persists a test incident and receipt; the normal dispatcher sends it with the same retries.
func SendAlertTest(ctx context.Context, tx pg.Tx, channel string, now time.Time) (uuid.UUID, error) {
	c, err := GetAlertConfiguration(ctx, tx)
	if err != nil {
		return uuid.Nil(), err
	}
	for _, ch := range c.Channels {
		if ch.ID == channel {
			id := uuid.NewV7()
			if _, err = tx.Exec(ctx, `INSERT INTO observability.alert_events(id,kind,key,severity,fields,state,fired_at,resolved_at,pending_recovery) VALUES($1,'alert.test',$2,'info','{}','resolved',$3,$3,true)`, id, id.String(), now); err != nil {
				return uuid.Nil(), err
			}
			raw, encodeErr := jsonx.MarshalCanonical(ch)
			if encodeErr != nil {
				return uuid.Nil(), encodeErr
			}
			receipt := uuid.NewV7()
			_, err = tx.Exec(ctx, `INSERT INTO observability.alert_receipts(id,event_id,rule_id,sequence,channel,quiet_start,quiet_end,at) VALUES($1,$2,'test',0,$3,0,0,$4)`, receipt, id, raw, now)
			return receipt, err
		}
	}
	return uuid.Nil(), fmt.Errorf("%w: unknown channel", ErrAlertConfiguration)
}

// ResolveAlertEvent lets operators close irreversible losses or incidents from a retired node.
func ResolveAlertEvent(ctx context.Context, tx pg.Tx, id uuid.UUID, now time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE observability.alert_events SET resolved_at=coalesce(resolved_at,$2),state='resolved',pending_recovery=true WHERE id=$1`, id, now)
	if err == nil && tag.RowsAffected() == 0 {
		return pg.ErrNoRows
	}
	return err
}

func readAlertEvents(ctx context.Context, tx pg.Tx, query string, args ...any) ([]AlertEvent, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	events := []AlertEvent{}
	for rows.Next() {
		var e AlertEvent
		var raw []byte
		err = rows.Scan(&e.ID, &e.Kind, &e.Key, &e.Severity, &raw, &e.Occurrences, &e.State, &e.FiredAt, &e.ResolvedAt)
		if err != nil {
			break
		}
		if err = jsonx.Unmarshal(raw, &e.Fields); err != nil {
			break
		}
		events = append(events, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return events, nil
}

func validateAlertRoute(r AlertRule, channels map[string]bool, seen map[[2]string]bool) error {
	if r.Channel == "" {
		return nil
	}
	pair := [2]string{r.Kind, r.Channel}
	if !channels[r.Channel] || seen[pair] {
		return ErrAlertConfiguration
	}
	seen[pair] = true
	return nil
}
