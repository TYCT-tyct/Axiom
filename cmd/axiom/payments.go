package main

import "net/http"

// A channel signs callbacks instead of authenticating as a console user. Only these two routes bypass that guard.
func withPaymentCallbacks(public, notify http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/payments/v1/notify/{merchant}", notify)
	mux.Handle("POST /api/payments/v1/notify/{merchant}", notify)
	mux.Handle("/", public)
	return mux
}
