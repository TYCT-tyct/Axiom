package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"axiom/internal/payments"
	"axiom/internal/platform/consoleapi"
)

// TestReturnAddressIsAConsolePage: the address the channel is told to send the browser back to is served by the
// console's app on the public routes, not by the website.
func TestReturnAddressIsAConsolePage(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	f.order(f.owner(), "return-1", http.StatusCreated)
	back, err := url.Parse(f.gateway.created[0].ReturnURL)
	if err != nil || back.Host != consoleHost {
		t.Fatalf("the return address %q: %v", f.gateway.created[0].ReturnURL, err)
	}
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+back.Host+back.Path, nil)
	r.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	f.routes.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "<console>" {
		t.Errorf("GET %s: %d %q, want the console's page", back.Path, rec.Code, rec.Body)
	}
}

// TestOrderMovedWhileTheChannelIsCalled: an order that is cancelled or credited between its commit and the save of its
// checkout is answered by what is stored, not by the waiting order the first request placed.
func TestOrderMovedWhileTheChannelIsCalled(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		during func(f consoleFixture, s session, r payments.CreateRequest)
		want   consoleapi.OrderState
	}{
		"cancelled": {func(f consoleFixture, s session, r payments.CreateRequest) {
			expect(f.t, "cancel meanwhile", f.post(s, "/orders/"+r.Order.TypeID()+"/cancel"), http.StatusOK, "")
		}, consoleapi.Cancelled},
		"credited": {func(f consoleFixture, s session, r payments.CreateRequest) {
			f.gateway.pays(r.Order.TypeID(), r.Total)
			expect(f.t, "verify meanwhile", f.post(s, "/orders/"+r.Order.TypeID()+"/verify"), http.StatusOK, "")
		}, consoleapi.Paid},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newConsoleFixture(t)
			f.openTopUps()
			s := f.owner()
			f.gateway.meanwhile(func(r payments.CreateRequest) { c.during(f, s, r) })
			o := f.order(s, "moved-1", http.StatusOK)
			if o.State != c.want || o.Checkout != nil {
				t.Errorf("the first answer: %+v, want a %s order without a checkout", o, c.want)
			}
			if again := f.order(s, "moved-1", http.StatusOK); again.Id != o.Id || again.State != c.want {
				t.Errorf("its replay: %+v", again)
			}
		})
	}
}

// TestRefusalThatCannotBeSaved: when the channel refuses an order but the refusal cannot be recorded, the order is still
// waiting, so the first answer is a fault to retry with the same key and not a refusal to start over from, and the replay
// finds the waiting order.
func TestRefusalThatCannotBeSaved(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	f.openTopUps()
	s := f.owner()
	f.gateway.fails(&payments.Rejected{Code: "declined", Message: "no"})
	f.run(`CREATE FUNCTION payments.refuse_failures() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected counting fault'; END $$;
		CREATE TRIGGER refuse_failures BEFORE INSERT OR UPDATE ON payments.ip_usage
		FOR EACH ROW WHEN (NEW.failures > 0) EXECUTE FUNCTION payments.refuse_failures()`)
	first := f.createOrder(s, "unsaved-1", topUpBody)
	expect(t, "a refusal that could not be saved", first, http.StatusInternalServerError, consoleapi.ErrorCodeInternal)
	if !strings.Contains(f.logs.String(), "API handler failed") {
		t.Errorf("the fault was not logged: %s", f.logs)
	}
	if got := f.text("SELECT string_agg(state, ',') FROM payments.orders"); got != "pending" {
		t.Fatalf("the order is %q, want it still waiting", got)
	}
	f.gateway.fails(nil)
	f.order(s, "unsaved-1", http.StatusAccepted)
}
