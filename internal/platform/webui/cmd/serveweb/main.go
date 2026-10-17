// Command serveweb serves the embedded frontend on a public and an admin listener as the node does, without a
// database or an API, so the end-to-end tests (web/playwright.config.ts) run the builds under the server's own
// headers and caching. The tests answer the API themselves.
//
// usage: serveweb <public address> <admin address>
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/webui"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: serveweb <public address> <admin address>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(publicAddr, adminAddr string) error {
	frontend, err := webui.Embedded()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := slog.New(slog.DiscardHandler)
	servers := []*http.Server{httpserver.New(frontend.Public(), logger), httpserver.New(frontend.Admin(), logger)}
	// Both are bound before either serves, so the test runner, which waits for the public one, finds the admin too.
	var listeners []net.Listener
	for _, addr := range []string{publicAddr, adminAddr} {
		l, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		defer l.Close() //nolint:errcheck // the process is ending
		listeners = append(listeners, l)
	}
	failed := make(chan error, len(servers))
	for i, s := range servers {
		go func() { failed <- s.Serve(listeners[i]) }()
	}
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	var errs []error
	for _, s := range servers {
		errs = append(errs, s.Shutdown(context.WithoutCancel(ctx)))
	}
	return errors.Join(errs...)
}
