package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"axiom/internal/platform/config"
)

// probeTimeout is shorter than the image's HEALTHCHECK --timeout, so the check reports its own failure.
const probeTimeout = 2 * time.Second

// probe asks the node itself: never through a proxy from the environment, and never following a redirect, so only
// the node's own answer to /readyz counts.
var probe = &http.Client{
	Transport:     &http.Transport{Proxy: nil},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       probeTimeout,
}

// healthcheck asks the node on this machine whether it is ready. The container image has no shell or curl for
// Docker's HEALTHCHECK, so the program checks itself. It reads only AXIOM_OPS_ADDR and does not report the other
// variables as unknown: they belong to serve, which validates them.
func healthcheck(ctx context.Context, environ []string) error {
	// An invalid AXIOM_OPS_ADDR reads as the default, where nothing listens: serve refused to start with it.
	host, port, err := net.SplitHostPort(opsAddr(config.New(environ)))
	if err != nil {
		return err
	}
	// Go dials an empty or unspecified host on the local system (net.Dial), so a listener on every address is probed
	// as configured. url.URL escapes an IPv6 zone such as %eth0, which a plain string would turn into an invalid escape.
	target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/readyz"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := probe.Do(req)
	if err != nil {
		return err
	}
	var notReady error
	if resp.StatusCode != http.StatusOK {
		notReady = fmt.Errorf("not ready: %s", resp.Status)
	}
	return errors.Join(notReady, resp.Body.Close())
}
