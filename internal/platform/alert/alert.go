// Package alert is the bounded operations signal shared by modules, independent of observability.
package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Sink never waits for persistence. A nil error acknowledges local buffering only.
type Sink interface {
	Raise(kind, key, severity string, fields map[string]string) error
	Resolve(kind, key string) error
}

// Signal carries only operations metadata, never tenant content or credentials.
type Signal struct {
	Kind     string            `json:"kind"`
	Key      string            `json:"key"`
	Severity string            `json:"severity"`
	Fields   map[string]string `json:"fields,omitempty"`
	Resolved bool              `json:"resolved,omitempty"`
}

type pending struct {
	id     uuid.UUID
	signal Signal
}

// Bus owns a bounded handoff and one writer. Close drains it with the supplied deadline.
type Bus struct {
	mu     sync.Mutex
	closed bool
	queue  chan pending
	done   chan struct{}
	cancel context.CancelFunc
	logger *slog.Logger
	err    error
}

// New starts a writer using its own transactions, independent of every caller's transaction.
func New(ctx context.Context, db *pg.DB, scope pg.Scope, logger *slog.Logger) *Bus {
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	b := &Bus{queue: make(chan pending, 1024), done: make(chan struct{}), cancel: cancel, logger: logger}
	go b.write(run, db, scope)
	return b
}

// Raise queues a firing signal without waiting for the writer.
func (b *Bus) Raise(kind, key, severity string, fields map[string]string) error {
	return b.push(Signal{Kind: kind, Key: key, Severity: severity, Fields: fields})
}

// Resolve queues recovery; database arrival order defines ordering between independent producers.
func (b *Bus) Resolve(kind, key string) error {
	return b.push(Signal{Kind: kind, Key: key, Severity: "info", Resolved: true})
}

func (b *Bus) push(s Signal) error {
	if err := validate(s); err != nil {
		return err
	}
	s.Fields = maps.Clone(s.Fields)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("alert: closed")
	}
	select {
	case b.queue <- pending{uuid.NewV7(), s}:
		return nil
	default:
		return errors.New("alert: buffer full")
	}
}

func validate(s Signal) error {
	if !validText(s.Kind, 128) || !validText(s.Key, 256) || len(s.Fields) > 16 {
		return errors.New("alert: invalid signal")
	}
	if s.Severity != "info" && s.Severity != "warning" && s.Severity != "critical" {
		return errors.New("alert: invalid severity")
	}
	for k, v := range s.Fields {
		if !validText(k, 64) || !validText(v, 512) {
			return errors.New("alert: invalid fields")
		}
	}
	return nil
}

const signalLock = 316005

// Persist is the durable boundary. Repeating id is harmless, including after an uncertain commit.
// Producers and the consumer share this lock so identity allocation cannot overtake an uncommitted signal.
func Persist(ctx context.Context, tx pg.Tx, id uuid.UUID, s Signal) error {
	if err := validate(s); err != nil {
		return err
	}
	body, err := jsonx.MarshalCanonical(s)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, signalLock); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.alert_signals(id,body) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, id, body)
	return err
}

// Consume atomically applies and removes at most 1000 signals in database arrival order.
func Consume(ctx context.Context, tx pg.Tx, apply func(Signal) error) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, signalLock); err != nil {
		return err
	}
	signals, err := readSignals(ctx, tx)
	if err != nil {
		return err
	}
	for _, p := range signals {
		if p.signal.Kind == "" {
			if _, err = tx.Exec(ctx, `UPDATE platform.alert_signals SET quarantined=true WHERE id=$1`, p.id); err != nil {
				return err
			}
			continue
		}
		if err = apply(p.signal); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM platform.alert_signals WHERE id=$1`, p.id); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bus) write(ctx context.Context, db *pg.DB, scope pg.Scope) {
	defer close(b.done)
	for p := range b.queue {
		for {
			attempt, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := db.Tx(attempt, scope, func(tx pg.Tx) error { return Persist(attempt, tx, p.id, p.signal) })
			cancel()
			if err == nil {
				break
			}
			if pg.IsDataError(err) {
				b.logger.ErrorContext(ctx, "alert signal rejected", "err", err)
				break
			}
			if ctx.Err() != nil {
				b.err = fmt.Errorf("alert: unpersisted signals at shutdown: %w", err)
				return
			}
			b.logger.ErrorContext(ctx, "persisting alert signal failed", "err", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		}
	}
}

// Close refuses new signals and drains the buffer; deadline expiry reports a failed durable handoff.
func (b *Bus) Close(ctx context.Context) error {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		close(b.queue)
	}
	b.mu.Unlock()
	select {
	case <-b.done:
	case <-ctx.Done():
		b.cancel()
		<-b.done
	}
	b.cancel()
	return b.err
}

func validText(v string, n int) bool {
	return len(v) > 0 && len(v) <= n && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
}

func readSignals(ctx context.Context, tx pg.Tx) ([]pending, error) {
	rows, err := tx.Query(ctx, `SELECT id,body FROM platform.alert_signals WHERE NOT quarantined ORDER BY seq LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	var signals []pending
	for rows.Next() {
		var p pending
		var raw []byte
		if err = rows.Scan(&p.id, &raw); err != nil {
			break
		}
		if jsonx.Unmarshal(raw, &p.signal) != nil || validate(p.signal) != nil {
			p.signal = Signal{}
		}

		signals = append(signals, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	return signals, nil
}
