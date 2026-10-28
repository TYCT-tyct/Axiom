package main

import (
	"net/http"

	"axiom/internal/payments"
)

var paymentPlugins = []payments.Plugin{payments.EasyPay{}}

// A channel signs callbacks instead of authenticating as a console user. Only these two routes bypass that guard.
func withPaymentCallbacks(public, notify http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+payments.NotifyPath+"{merchant}", notify)
	mux.Handle("POST "+payments.NotifyPath+"{merchant}", notify)
	mux.Handle("/", public)
	return mux
}
