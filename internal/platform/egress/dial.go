package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"axiom/internal/platform/egresstest"
)

const connectTimeout = 10 * time.Second

type dialer struct {
	lookup  func(context.Context, string, string) ([]netip.Addr, error)
	connect func(context.Context, string, string) (net.Conn, error)
	policy  egresstest.Policy
}

func (d *dialer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	// Transport may detach a dial from the request deadline to reuse its result for another request.
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("egress split destination: %w", err)
	}
	ips, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i, ip := range ips {
		conn, err := d.connectOne(ctx, network, net.JoinHostPort(ip.String(), port), len(ips)-i)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("egress connect to validated address: %w", lastErr)
}

// connectOne gives each address an equal share of the remaining time, like net.Dialer's partialDeadline, so a
// blackholed first address (often a broken IPv6 route) cannot use up the budget of the addresses after it. Unlike
// net.Dialer there is no 2s floor: the budget is at most connectTimeout, and the last address gets all that is left.
func (d *dialer) connectOne(ctx context.Context, network, address string, left int) (net.Conn, error) {
	deadline, _ := ctx.Deadline() // dialContext always sets one
	ctx, cancel := context.WithTimeout(ctx, time.Until(deadline)/time.Duration(left))
	defer cancel()
	return d.connect(ctx, network, address)
}

// resolve names the host in a denial so operators can tell which upstream was refused; userinfo never reaches here.
func (d *dialer) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ips, err := d.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("egress %s: %w", host, ErrAddressDenied)
	}
	for _, ip := range ips {
		if !allowedAddress(ip, d.policy) {
			return nil, fmt.Errorf("egress %s: %w", host, ErrAddressDenied)
		}
	}
	return ips, nil
}

// A mapped literal in the URL is refused as written. Resolver answers are unmapped instead: Go's pure resolver
// returns IPv4 entries of /etc/hosts as IPv4-mapped addresses, and the IPv4 checks then apply to what is dialed.
func (d *dialer) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	if numericHost(host) {
		return nil, nil // denied like an empty answer
	}
	ips, err := d.lookup(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("egress resolve destination: %w", err)
	}
	for i := range ips {
		ips[i] = ips[i].Unmap()
	}
	return ips, nil
}
