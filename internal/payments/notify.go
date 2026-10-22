package payments

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

const callbackMax = 1 << 20

// Notify handles the merchant-specific public callback. The caller mounts its GET and POST routes without a user
// session: authentication is the channel's signature, checked before any event or payment can be written.
func (p *Payments) Notify() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), pollDeadline)
		defer cancel()
		id, err := merchantPrefix.Decode(r.PathValue("merchant"))
		if err != nil {
			http.Error(w, "unknown merchant", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, callbackMax))
		if err != nil || len(r.URL.RawQuery) > callbackMax {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		ch, err := p.callbackChannel(ctx, MerchantID(id))
		if err != nil {
			http.Error(w, "merchant unavailable", http.StatusServiceUnavailable)
			return
		}
		n, err := ch.Verify(ctx, Callback{Method: r.Method, RawQuery: r.URL.RawQuery, Body: body, Header: r.Header})
		if err != nil {
			http.Error(w, "invalid signature", http.StatusBadRequest)
			return
		}
		if n.Relevant {
			if err = p.notify(ctx, ch, MerchantID(id), n); err != nil {
				p.logger.Error("payments: callback could not commit", "merchant", MerchantID(id).TypeID(), "error", err)
				status, msg := ch.Ack(false)
				if errors.Is(err, ErrUnconfirmed) {
					status = http.StatusBadGateway
				} else if status < 300 {
					status = http.StatusInternalServerError
				}
				w.WriteHeader(status)
				p.writeAck(w, msg)
				return
			}
		}
		status, msg := ch.Ack(true)
		w.WriteHeader(status)
		p.writeAck(w, msg)
	})
}

func (p *Payments) writeAck(w http.ResponseWriter, msg []byte) {
	if _, err := w.Write(msg); err != nil {
		p.logger.Warn("payments: callback acknowledgement disconnected", "error", err)
	}
}

func (p *Payments) notify(ctx context.Context, ch Channel, merchant MerchantID, n Notice) error {
	state := PayPending
	if n.Success {
		state = PayPaid
	}
	c := confirmed{merchant: merchant, order: n.Order, payment: Payment{State: state, TradeNo: n.TradeNo, Paid: n.Paid}, source: "notify", raw: n.Raw}
	if !n.Success {
		return p.receive(ctx, c)
	}
	replay, err := p.replayedPayment(ctx, c)
	if err != nil || replay {
		return err
	}
	if ch.Caps().Has(CapQuery) {
		payment, err := ch.Query(ctx, n.Order, n.TradeNo)
		if err != nil {
			return errors.Join(ErrUnconfirmed, err)
		}
		if payment.State != PayPaid {
			return ErrUnconfirmed
		}
		if payment.TradeNo != n.TradeNo || payment.Paid != n.Paid {
			c.reason = "confirmation_mismatch"
		}
		c.payment = payment
	}
	return p.receive(ctx, c)
}

// A replay bypasses the gateway only when both identity and exact amounts match a committed, credited receipt.
func (p *Payments) replayedPayment(ctx context.Context, c confirmed) (bool, error) {
	id, err := orderPrefix.Decode(c.order)
	if err != nil {
		return false, nil
	}
	var replay bool
	err = p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		o, err := loadOrder(ctx, tx, OrderID(id))
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil || o.MerchantID != c.merchant || State(o.State).canMoveTo(Paid) {
			return err
		}
		r, err := queries.RecordedPayment(ctx, tx, RecordedPaymentParams{MerchantID: uuid.UUID(c.merchant), TradeNo: c.payment.TradeNo})
		if err == nil && len(r) == 1 {
			replay = r[0].OrderID == id && r[0].Amount == c.payment.Paid.Minor && r[0].Currency == c.payment.Paid.Currency.String()
		}
		return err
	})
	return replay, err
}

// callbackChannel caches decrypted configuration for a minute. The revision is still read on every request so saves
// take effect immediately; an out-of-band settings edit is caught when the cached binding expires.
func (p *Payments) callbackChannel(ctx context.Context, id MerchantID) (Channel, error) {
	var ch Channel
	err := p.db.ReadTx(ctx, p.platform, func(tx pg.Tx) error {
		rows, err := queries.MerchantConfigs(ctx, tx, []uuid.UUID{uuid.UUID(id)})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNotFound
		}
		r := rows[0]
		p.mu.Lock()
		cached, ok := p.channels[id]
		p.mu.Unlock()
		if ok && cached.rev == r.Rev && time.Since(cached.at) < validFor {
			ch = cached.channel
			return nil
		}
		ch, err = p.open(r)
		if err == nil {
			p.mu.Lock()
			p.channels[id] = channelBinding{r.Rev, time.Now(), ch}
			p.mu.Unlock()
		}
		return err
	})
	return ch, err
}

type channelBinding struct {
	rev     int64
	at      time.Time
	channel Channel
}
