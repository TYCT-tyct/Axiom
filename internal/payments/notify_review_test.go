package payments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"uuid"

	"axiom/internal/platform/clientip"
)

func (w *world) callbackFrom(id MerchantID, remote string, v url.Values) *httptest.ResponseRecorder {
	w.t.Helper()
	r := httptest.NewRequestWithContext(w.t.Context(), http.MethodGet, "/?"+v.Encode(), nil)
	r.RemoteAddr = remote
	r.SetPathValue("merchant", id.TypeID())
	rw := httptest.NewRecorder()
	w.p.Notify().ServeHTTP(rw, r)
	return rw
}

func TestRandomMerchantFloodDoesNotBlockRealCallbacks(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout, verify: signedNotice, noQuery: true})
	w.waiting()
	o := w.mustPlace(yuan, "flood")
	for i := range callbackBuckets + 1 {
		r := w.callbackFrom(MerchantID(uuid.NewV7()), "203.0.113.1:1234", nil)
		want := http.StatusServiceUnavailable
		if i >= callbackIPLimit {
			want = http.StatusTooManyRequests
		}
		if r.Code != want {
			t.Fatalf("random merchant request %d: %d, want %d", i, r.Code, want)
		}
	}
	if len(w.p.callbacks) != 1 {
		t.Fatalf("random merchants allocated %d buckets", len(w.p.callbacks))
	}
	if r := w.callbackFrom(o.MerchantID, "198.51.100.1:1234", callbackValues(o)); r.Code != 200 {
		t.Fatalf("real gateway locked out: %d %s", r.Code, r.Body)
	}
	w.assertCredit(o, o.Credits)
}

func TestCallbackFullTableEvictsOldestAndGroupsIPv6(t *testing.T) {
	w := newTestWorld(t, fakePlugin{verify: func(Callback, string) (Notice, error) { return Notice{}, nil }})
	m := w.waiting()
	now := time.Now()
	oldest := callbackKey{ip: netip.MustParseAddr("192.0.2.1")}
	w.p.allowCallback(oldest, callbackIPLimit, now)
	for i := 1; i < callbackBuckets; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
		w.p.allowCallback(callbackKey{ip: ip}, callbackIPLimit, now)
	}
	if r := w.callbackFrom(MerchantID(m.ID), "198.51.100.1:1234", nil); r.Code != 200 {
		t.Fatalf("full table rejected a real callback: %d", r.Code)
	}
	if len(w.p.callbacks) != callbackBuckets || w.p.callbacks[oldest] != nil {
		t.Fatal("oldest bucket was not evicted within the capacity bound")
	}
	key := callbackKey{ip: clientip.Group(netip.MustParseAddr("2001:db8:1::1"))}
	for range callbackIPLimit {
		w.p.allowCallback(key, callbackIPLimit, now)
	}
	if r := w.callbackFrom(MerchantID(m.ID), "[2001:db8:1::2]:1234", nil); r.Code != 429 {
		t.Fatal("IPv6 address rotation bypassed the /64 budget", r.Code)
	}
	if r := w.callbackFrom(MerchantID(m.ID), "[2001:db8:2::1]:1234", nil); r.Code != 200 {
		t.Fatal("another IPv6 allocation was blocked", r.Code)
	}
}

func TestUnknownUnpaidCallbackAcknowledged(t *testing.T) {
	for _, state := range []PayState{PayPending, PayClosed} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{
				create: goodCheckout, verify: signedNotice,
				query: func(context.Context, string, string) (Payment, error) { return Payment{State: state}, nil },
			})
			w.waiting()
			o := w.mustPlace(yuan, "unknown-unpaid")
			v := callbackValues(o)
			v.Set("out_trade_no", "foreign-order")
			v.Set("sign", epaySignature(v, theKey))
			if r := w.callback(o.MerchantID, http.MethodGet, v); r.Code != 200 {
				t.Fatal("unmatched unpaid callback remained retryable", r.Code)
			}
			if r := w.callback(o.MerchantID, http.MethodGet, callbackValues(o)); r.Code != 502 {
				t.Fatal("known order accepted contradictory gateway status", r.Code)
			}
			if w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies") != 0 || !strings.Contains(w.logs.String(), "unmatched callback is not paid") {
				t.Fatal("unpaid unknown order was not warned without a discrepancy")
			}
			w.assertCredit(o, 0)
		})
	}
}

func TestCallbackChannelFailuresAreLogged(t *testing.T) {
	for _, scenario := range []string{"unknown", "plugin absent", "open failed"} {
		t.Run(scenario, func(t *testing.T) {
			w := newTestWorld(t, fakePlugin{})
			m := w.waiting()
			id, level := MerchantID(m.ID), "level=ERROR"
			switch scenario {
			case "unknown":
				id, level = MerchantID(uuid.NewV7()), "level=WARN"
			case "plugin absent":
				delete(w.p.plugins, "fake")
			case "open failed":
				w.p.plugins["fake"] = fakePlugin{open: func() error { return errors.New("fake: configuration unavailable") }}
			}
			if r := w.callback(id, http.MethodGet, nil); r.Code != 503 {
				t.Fatal("unexpected channel failure status", r.Code)
			}
			log := w.logs.String()
			if !strings.Contains(log, level) || !strings.Contains(log, id.TypeID()) || !strings.Contains(log, "error=") {
				t.Fatal("channel failure lacks level, merchant or error", log)
			}
		})
	}
}

func TestPaymentQueryHasItsOwnDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), pollDeadline)
		defer cancel()
		ch := fakeChannel{query: func(ctx context.Context, _, _ string) (Payment, error) {
			<-ctx.Done()
			return Payment{}, ctx.Err()
		}}
		start := time.Now()
		_, err := queryPayment(ctx, ch, "order", "trade")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != closeTimeout {
			t.Fatal("query did not stop at its own deadline", err, time.Since(start))
		}
	})
}
