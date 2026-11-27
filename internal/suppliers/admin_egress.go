package suppliers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/store"
)

// The platform's egress proxies as a person manages them (docs/domains/suppliers.md「出口代理」), and which one a
// credential goes through. Every change here takes the proxies' lock first, so a check of a fallback chain or of a
// proxy's users holds until the change commits.

// The ways a change to a proxy, or to a credential's exit, is refused.
var (
	// ErrNoEgressProxy is a proxy that is not there.
	ErrNoEgressProxy = errors.New("suppliers: no such egress proxy")
	// ErrEgressProxyExists is a proxy id already taken.
	ErrEgressProxyExists = errors.New("suppliers: an egress proxy has that id")
	// ErrEgressProxy is a proxy that is not one: see EgressProxySpec.
	ErrEgressProxy = errors.New("suppliers: an egress proxy is http, https or socks5 at a host and port, with a username and password of at most 255 bytes")
	// ErrEgressFallback is a fallback that is not another proxy with no fallback of its own, or one set on a proxy
	// that is itself a fallback: a chain is one hop.
	ErrEgressFallback = errors.New("suppliers: a fallback is another proxy without a fallback, set on a proxy that is no proxy's fallback")
	// ErrUnknownEgress is an exit that names no proxy.
	ErrUnknownEgress = errors.New("suppliers: no egress proxy has that id")
	// ErrEgressInUse is a proxy that a credential that is not erased, or another proxy's fallback, still uses.
	ErrEgressInUse = errors.New("suppliers: the egress proxy is in use")
	// ErrEgressUnavailable is an exit whose proxy is disabled and has no active fallback, which a check cannot go
	// through: it is never sent direct instead. It is an ErrStatusLocked, as a disabled credential's check is.
	ErrEgressUnavailable = fmt.Errorf("%w: the egress proxy is disabled and has no active fallback", ErrStatusLocked)
)

// EgressProxy is a proxy as the admin shows it: never its password, only whether it has one.
type EgressProxy struct {
	ID, Name, Scheme, Host string
	Port                   int
	Username               string // empty for none
	HasPassword            bool
	FallbackID             string // empty for none
	Status                 string // active or disabled
}

// ProxyAuth is the username a proxy asks for, and its password, which is only sealed; nil Password is none.
type ProxyAuth struct {
	Username string
	Password *secret.Secret
}

// EgressProxySpec is a new proxy. Its id is what credentials name it by; its scheme, host and port do not change after,
// as a supplier's origin does not: another address is another proxy. Set is the rest, as a change sets it after.
type EgressProxySpec struct {
	ID, Scheme, Host string
	Port             int
	Set              EgressProxyChange
}

// EgressProxyChange is what a person sets on a proxy: its name, its fallback (empty for none), its status, and
// credentials (nil keeps the ones it has, and a new proxy has none).
type EgressProxyChange struct {
	Name, FallbackID, Status string
	Auth                     *ProxyAuth
}

var proxyID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

var proxyHost = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)

// valid holds the table's CHECKs, so a refusal is ErrEgressProxy rather than the database's.
func (s EgressProxySpec) valid() bool {
	return proxyID.MatchString(s.ID) && s.ID != string(egress.Direct) && slices.Contains([]string{"http", "https", "socks5"}, s.Scheme) &&
		proxyHost.MatchString(s.Host) && s.Port >= 1 && s.Port <= 65535 && s.Set.valid()
}

func (c EgressProxyChange) valid() bool {
	n := len([]rune(c.Name))
	return n >= 1 && n <= 200 && (c.Status == proxyActive || c.Status == "disabled") && c.Auth.valid()
}

func (a *ProxyAuth) valid() bool {
	return a == nil || a.Username != "" && len(a.Username) <= 255 && (a.Password == nil || a.Password.Reveal() != "" && len(a.Password.Reveal()) <= 255)
}

// EgressProxies lists the proxies by id.
func (*Admin) EgressProxies(ctx context.Context, tx pg.Tx) ([]EgressProxy, error) {
	rows, err := store.New().ListEgressProxies(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the egress proxies: %w", err)
	}
	out := make([]EgressProxy, len(rows))
	for i, r := range rows {
		out[i] = egressProxyOf(r)
	}
	return out, nil
}

// CreateEgressProxy adds a proxy and records it; no credential uses it until one is set to.
func (a *Admin) CreateEgressProxy(ctx context.Context, tx pg.Tx, actor audit.Actor, s EgressProxySpec, reason string) (EgressProxy, error) {
	if !s.valid() {
		return EgressProxy{}, ErrEgressProxy
	}
	all, err := lockedProxies(ctx, tx)
	if err != nil {
		return EgressProxy{}, err
	}
	if _, taken := all[s.ID]; taken {
		return EgressProxy{}, ErrEgressProxyExists
	}
	row := store.SuppliersEgressProxy{ID: s.ID, Scheme: s.Scheme, Host: s.Host}
	if err := a.change(&row, all, s.Set); err != nil {
		return EgressProxy{}, err
	}
	if err := store.New().InsertEgressProxy(ctx, tx, store.InsertEgressProxyParams{
		ID: row.ID, Name: row.Name, Scheme: row.Scheme, Host: row.Host, Port: int64(s.Port), Username: row.Username,
		Ciphertext: row.Ciphertext, Nonce: row.Nonce, WrappedDek: row.WrappedDek, KeyVersion: row.KeyVersion,
		FallbackID: row.FallbackID, Status: row.Status,
	}); err != nil {
		return EgressProxy{}, fmt.Errorf("add the egress proxy: %w", err)
	}
	p := egressProxyOf(row)
	p.Port = s.Port
	return p, recordProxy(ctx, tx, actor, "egress_proxy.create", p, reason)
}

// UpdateEgressProxy sets a proxy's name, fallback, status and, when given, its credentials, and records it. The nodes
// take the change at their next Reload; calls in flight end on the connection they have.
func (a *Admin) UpdateEgressProxy(ctx context.Context, tx pg.Tx, actor audit.Actor, id string, c EgressProxyChange, reason string) (EgressProxy, error) {
	if !c.valid() {
		return EgressProxy{}, ErrEgressProxy
	}
	all, err := lockedProxies(ctx, tx)
	if err != nil {
		return EgressProxy{}, err
	}
	row, ok := all[id]
	if !ok {
		return EgressProxy{}, ErrNoEgressProxy
	}
	if err := a.change(&row, all, c); err != nil {
		return EgressProxy{}, err
	}
	if err := store.New().UpdateEgressProxy(ctx, tx, store.UpdateEgressProxyParams{
		Name: row.Name, Username: row.Username, Ciphertext: row.Ciphertext, Nonce: row.Nonce, WrappedDek: row.WrappedDek,
		KeyVersion: row.KeyVersion, FallbackID: row.FallbackID, Status: row.Status, ID: row.ID,
	}); err != nil {
		return EgressProxy{}, fmt.Errorf("change the egress proxy: %w", err)
	}
	p := egressProxyOf(row)
	return p, recordProxy(ctx, tx, actor, "egress_proxy.update", p, reason)
}

// change sets c on row, checking its fallback against the other proxies, all: a chain is one hop.
func (a *Admin) change(row *store.SuppliersEgressProxy, all map[string]store.SuppliersEgressProxy, c EgressProxyChange) error {
	row.Name, row.Status, row.FallbackID = c.Name, c.Status, nil
	if c.FallbackID != "" {
		f, ok := all[c.FallbackID]
		if !ok || f.ID == row.ID || f.FallbackID != nil || fallbackOfAny(all, row.ID) {
			return ErrEgressFallback
		}
		row.FallbackID = &f.ID
	}
	if c.Auth != nil {
		a.authorize(row, *c.Auth)
	}
	return nil
}

// fallbackOfAny says whether any proxy falls back to id.
func fallbackOfAny(all map[string]store.SuppliersEgressProxy, id string) bool {
	for _, p := range all {
		if p.FallbackID != nil && *p.FallbackID == id {
			return true
		}
	}
	return false
}

// authorize sets the row's credentials, its password sealed to the row.
func (a *Admin) authorize(row *store.SuppliersEgressProxy, auth ProxyAuth) {
	row.Username, row.Ciphertext, row.Nonce, row.WrappedDek, row.KeyVersion = &auth.Username, nil, nil, nil, nil
	if auth.Password != nil {
		sealed := a.keys.Seal(*auth.Password, proxyAt(row.ID))
		version := int64(sealed.KeyVersion)
		row.Ciphertext, row.Nonce, row.WrappedDek, row.KeyVersion = sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, &version
	}
}

// RemoveEgressProxy removes a proxy no credential uses (an erased one lets go of it) and that is no proxy's fallback,
// and records it; ErrEgressInUse otherwise. It returns the proxy as it was.
func (*Admin) RemoveEgressProxy(ctx context.Context, tx pg.Tx, actor audit.Actor, id, reason string) (EgressProxy, error) {
	all, err := lockedProxies(ctx, tx)
	if err != nil {
		return EgressProxy{}, err
	}
	row, ok := all[id]
	if !ok {
		return EgressProxy{}, ErrNoEgressProxy
	}
	q := store.New()
	users, err := q.EgressProxyUsers(ctx, tx, id)
	switch {
	case err != nil:
		return EgressProxy{}, fmt.Errorf("read the egress proxy's users: %w", err)
	case users.Credentials > 0 || users.Fallbacks > 0:
		return EgressProxy{}, fmt.Errorf("%w: by %d credentials and %d proxies", ErrEgressInUse, users.Credentials, users.Fallbacks)
	}
	if err := q.ReleaseErasedEgress(ctx, tx, id); err != nil {
		return EgressProxy{}, fmt.Errorf("release the erased credentials: %w", err)
	}
	if err := q.DeleteEgressProxy(ctx, tx, id); err != nil {
		return EgressProxy{}, fmt.Errorf("remove the egress proxy: %w", err)
	}
	p := egressProxyOf(row)
	return p, recordProxy(ctx, tx, actor, "egress_proxy.remove", p, reason)
}

// SetCredentialEgress sets the proxy a credential's calls and checks go through, empty for direct, and records it.
// Already as asked: nothing changes and nothing is recorded. The nodes take it at their next Reload.
func (a *Admin) SetCredentialEgress(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, credential uuid.UUID, egressID, reason string) (Credential, error) {
	row, err := a.lockedCredential(ctx, tx, supplier, credential)
	if err != nil {
		return Credential{}, err
	}
	if row.Status == "erased" {
		return Credential{}, fmt.Errorf("%w: the credential is erased", ErrStatusLocked)
	}
	from := deref(row.EgressID)
	if from == egressID {
		return credentialOf(credentialFacts(row)), nil
	}
	if err := lockedEgress(ctx, tx, egressID); err != nil {
		return Credential{}, err
	}
	to := orNull(egressID)
	if err := store.New().SetCredentialEgress(ctx, tx, store.SetCredentialEgressParams{EgressID: to, SupplierID: supplier, ID: credential}); err != nil {
		return Credential{}, fmt.Errorf("set the credential's exit: %w", err)
	}
	if err := a.record(ctx, tx, actor, "credential.egress", supplier, reason, map[string]any{
		"credential": credential.String(), "from": exitName(from), "to": exitName(egressID),
	}); err != nil {
		return Credential{}, err
	}
	row.EgressID = to
	return credentialOf(credentialFacts(row)), nil
}

// lockedEgress takes the proxies' lock for a credential to name id, empty for direct: ErrUnknownEgress for a proxy
// that is not there.
func lockedEgress(ctx context.Context, tx pg.Tx, id string) error {
	if id == "" {
		return nil
	}
	all, err := lockedProxies(ctx, tx)
	if err != nil {
		return err
	}
	if _, ok := all[id]; !ok {
		return ErrUnknownEgress
	}
	return nil
}

// exitFor is the client a check of a credential naming id goes through, as its calls would: read in tx, ErrUnknownEgress
// for a proxy that is not there and ErrEgressUnavailable for one with no exit now.
func (a *Admin) exitFor(ctx context.Context, tx pg.Tx, id string) (*egress.Client, error) {
	if id == "" {
		return a.egress, nil
	}
	e, err := a.readExits(ctx, tx)
	if err != nil {
		return nil, err
	}
	defer a.keepExits(e)
	if _, ok := e.proxies[id]; !ok {
		return nil, ErrUnknownEgress
	}
	c, _, err := e.client(&id)
	if errors.Is(err, errNoExit) {
		return nil, ErrEgressUnavailable
	}
	return c, err
}

// readExits reads the proxies in tx with the clients the admin side's readings built before, so its checks and
// re-lists share one client per proxy, and its connections, rather than each building one; keepExits gives back what
// the reading built.
func (a *Admin) readExits(ctx context.Context, tx pg.Tx) (*exits, error) {
	a.exitsMu.Lock()
	earlier := maps.Clone(a.exits)
	a.exitsMu.Unlock()
	return readExits(ctx, tx, a.egress, a.keys, earlier)
}

// keepExits keeps the clients a reading built for the readings after it. A client it replaced, because its proxy
// changed, and the client of a proxy no longer there have their idle connections closed; calls still on one finish.
func (a *Admin) keepExits(e *exits) {
	a.exitsMu.Lock()
	defer a.exitsMu.Unlock()
	if a.exits == nil {
		a.exits = map[string]proxyClient{}
	}
	for id, b := range e.built {
		if old, ok := a.exits[id]; ok && old != b {
			old.client.CloseIdleConnections()
		}
		a.exits[id] = b
	}
	for id, b := range a.exits {
		if _, ok := e.proxies[id]; !ok {
			b.client.CloseIdleConnections()
			delete(a.exits, id)
		}
	}
}

// lockedProxies takes the proxies' lock and reads them all by id.
func lockedProxies(ctx context.Context, tx pg.Tx) (map[string]store.SuppliersEgressProxy, error) {
	q := store.New()
	if err := q.LockEgressProxies(ctx, tx); err != nil {
		return nil, fmt.Errorf("wait for the egress proxies: %w", err)
	}
	rows, err := q.ListEgressProxies(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the egress proxies: %w", err)
	}
	all := make(map[string]store.SuppliersEgressProxy, len(rows))
	for _, r := range rows {
		all[r.ID] = r
	}
	return all, nil
}

func egressProxyOf(r store.SuppliersEgressProxy) EgressProxy {
	return EgressProxy{
		ID: r.ID, Name: r.Name, Scheme: r.Scheme, Host: r.Host, Port: int(r.Port), Username: deref(r.Username),
		HasPassword: r.KeyVersion != nil, FallbackID: deref(r.FallbackID), Status: r.Status,
	}
}

// exitName is an exit as an audit entry names it: a proxy's id, or direct.
func exitName(id string) string {
	if id == "" {
		return string(egress.Direct)
	}
	return id
}

// recordProxy records a change of a proxy with what is not secret about it: never its username or password, only
// whether it has them.
func recordProxy(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, p EgressProxy, reason string) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason, Target: audit.Target{Module: "suppliers", Kind: "egress_proxy", ID: p.ID},
		Detail: map[string]any{
			"name": p.Name, "scheme": p.Scheme, "host": p.Host, "port": p.Port, "fallback": p.FallbackID,
			"status": p.Status, "authenticated": p.Username != "",
		},
	})
}
