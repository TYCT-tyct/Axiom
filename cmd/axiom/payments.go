package main

import (
	"log/slog"
	"net/http"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"

	"axiom/internal/payments"
)

var paymentPlugins = []payments.Plugin{payments.EasyPay{}, payments.Stripe{}}

// A channel signs callbacks instead of authenticating as a console user. Only these two routes bypass that guard.
func withPaymentCallbacks(public, notify http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET "+payments.NotifyPath+"{merchant}", notify)
	mux.Handle("POST "+payments.NotifyPath+"{merchant}", notify)
	mux.Handle("/", public)
	return mux
}

func registerPaymentJobs(db *pg.DB, keys *secret.Keyring, exit *egress.Client, logger *slog.Logger, queue *pg.Queue) error {
	store, err := payments.Open(payments.Config{DB: db, Platform: pg.PlatformScope(), Keyring: keys, Client: exit, Logger: logger, Plugins: paymentPlugins})
	if err != nil {
		return err
	}
	cfg := payments.PollConfig{Payments: store, Queue: queue, Logger: logger}
	if err = payments.RegisterPoll(cfg); err != nil {
		return err
	}
	return payments.RegisterReconcile(cfg)
}
