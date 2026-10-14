package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"

	"axiom/internal/platform/config"
)

// healthcheck asks the node on this machine whether it is ready. The container image has no shell or curl for
// Docker's HEALTHCHECK, so the program checks itself. It reads only AXIOM_OPS_ADDR and does not report the other
// variables as unknown: they belong to serve, which validates them.
func healthcheck(ctx context.Context, environ []string) error {
	// An invalid AXIOM_OPS_ADDR reads as the default, where nothing listens: serve refused to start with it.
	host, port, err := net.SplitHostPort(opsAddr(config.New(environ)))
	if err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(host); host == "" || err == nil && ip.IsUnspecified() {
		host = "127.0.0.1" // a listener on every address answers on loopback too
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	var notReady error
	if resp.StatusCode != http.StatusOK {
		notReady = fmt.Errorf("not ready: %s", resp.Status)
	}
	return errors.Join(notReady, resp.Body.Close())
}
