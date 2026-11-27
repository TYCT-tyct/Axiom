package suppliers

import (
	"cmp"
	"context"
	"errors"
	"fmt"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/store"
)

// The exit a credential's requests leave by (docs/domains/suppliers.md「出口代理」): direct when it names no proxy, else
// its proxy, or the proxy's fallback while the proxy is disabled. Calls, checks and the re-list pick it by the same
// rule from the same rows, so a key is never checked by one route and called by another.

// errNoExit is a credential whose proxy is disabled and has no active fallback: it is not called, nor checked, until
// one of them is enabled or its exit is changed. It is never sent direct instead.
var errNoExit = errors.New("suppliers: the credential's proxy is disabled and has no active fallback")

// exits are the clients one reading of the proxies hands out: each proxy's, its password opened once per reading, and
// taken from an earlier reading when its row is unchanged, so a node's connections to a proxy outlive a Reload.
type exits struct {
	direct  *egress.Client
	keys    *secret.Keyring
	proxies map[string]store.SuppliersEgressProxy
	byID    map[string]*egress.Client // this reading's, by proxy id
	earlier map[egress.Proxy]*egress.Client
	built   map[egress.Proxy]*egress.Client
}

// readExits reads the proxies in tx; earlier is the clients a reading before built, nil for none.
func readExits(ctx context.Context, tx pg.Tx, direct *egress.Client, keys *secret.Keyring, earlier map[egress.Proxy]*egress.Client) (*exits, error) {
	rows, err := store.New().ListEgressProxies(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the egress proxies: %w", err)
	}
	e := &exits{
		direct: direct, keys: keys, proxies: make(map[string]store.SuppliersEgressProxy, len(rows)), byID: map[string]*egress.Client{},
		earlier: earlier, built: map[egress.Proxy]*egress.Client{},
	}
	for _, r := range rows {
		e.proxies[r.ID] = r
	}
	return e, nil
}

// client is the exit for a credential naming egressID, with the id of the proxy it goes through, the fallback's while
// the named one is disabled: the direct client and no proxy for none, errNoExit when its proxy cannot be used now, and
// an error for a proxy row a client cannot be built from (its password does not open), which leaves the credential's
// supplier out rather than calling it another way.
func (e *exits) client(egressID *string) (c *egress.Client, proxy string, err error) {
	if egressID == nil {
		return e.direct, "", nil
	}
	row, ok := e.route(*egressID)
	if !ok {
		return nil, "", errNoExit
	}
	if c = e.byID[row.ID]; c != nil {
		return c, row.ID, nil
	}
	p, err := e.open(row)
	if err != nil {
		return nil, "", err
	}
	if c = cmp.Or(e.built[p], e.earlier[p]); c == nil {
		if c, err = e.direct.Via(p); err != nil {
			return nil, "", fmt.Errorf("egress proxy %s: %w", row.ID, err)
		}
	}
	e.byID[row.ID], e.built[p] = c, c
	return c, row.ID, nil
}

// route is the proxy a credential naming id goes through: the proxy while it is active, else its fallback while that
// is. One hop only: a fallback's own fallback is never followed.
func (e *exits) route(id string) (store.SuppliersEgressProxy, bool) {
	p, ok := e.proxies[id]
	if !ok || p.Status == proxyActive {
		return p, ok
	}
	if p.FallbackID == nil {
		return p, false
	}
	f, ok := e.proxies[*p.FallbackID]
	return f, ok && f.Status == proxyActive
}

// open is a row as egress takes it, its password opened.
func (e *exits) open(r store.SuppliersEgressProxy) (egress.Proxy, error) {
	p := egress.Proxy{Scheme: r.Scheme, Host: r.Host, Port: int(r.Port), Username: deref(r.Username)}
	if r.KeyVersion == nil {
		return p, nil
	}
	password, err := e.keys.Open(sealedOf(r.Ciphertext, r.Nonce, r.WrappedDek, *r.KeyVersion), proxyAt(r.ID))
	if err != nil {
		return p, fmt.Errorf("egress proxy %s: its password does not open: %w", r.ID, err)
	}
	p.Password = password.Reveal()
	return p, nil
}

// release closes the idle connections of the clients an earlier reading built that this one no longer hands out;
// calls still using one finish on it.
func (e *exits) release() {
	for p, c := range e.earlier {
		if e.built[p] != c {
			c.CloseIdleConnections()
		}
	}
}

// proxyAt is where a proxy's password is sealed to: its own row.
func proxyAt(id string) secret.Location {
	return secret.Location{Table: "suppliers.egress_proxies", RowID: id, Purpose: "password"}
}

// directEgress names the exit of a credential that goes through no proxy, as the call log's egress column does; no
// proxy's id may be it.
const directEgress = "direct"

// proxyActive is the status of a proxy in use; the other is disabled.
const proxyActive = "active"
