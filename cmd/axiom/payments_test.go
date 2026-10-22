package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPaymentCallbacksBypassOnlyConsoleAuthentication(t *testing.T) {
	notify := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("merchant") != "mch_example" {
			t.Error("callback lost its merchant identity")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	console := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	routes := withPaymentCallbacks(console, notify)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/payments/v1/notify/mch_example", 202},
		{http.MethodPost, "/api/payments/v1/notify/mch_example", 202},
		{http.MethodGet, "/api/console/v1/me", 401},
		{http.MethodPost, "/api/payments/v1/orders", 401},
	} {
		rw := httptest.NewRecorder()
		routes.ServeHTTP(rw, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil))
		if rw.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rw.Code, tc.status)
		}
	}
}
