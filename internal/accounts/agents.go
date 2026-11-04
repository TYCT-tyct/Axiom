package accounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// An operations credential is the third kind of operator (README.md「运营凭证」): the AI agent's, presented only on the
// operations listener. Its token is an API key's shape behind its own fixed prefix, so the same checksum refuses a
// mistyped one before anything is hashed.
const agentTokenPrefix = "op-ax-v1-"

const (
	// AgentOverlap is the longest the token a rotation replaces still works, for the MCP bridge to swap its file.
	AgentOverlap = 24 * time.Hour
	// agentTouchEvery is how stale the last use may be before a request writes it again: a write per request would be
	// a write per tool call.
	agentTouchEvery = time.Minute
	// agentBurst and agentRefill are a credential's requests on one node: 60 at once, 600 a minute.
	agentBurst  = 60
	agentRefill = 100 * time.Millisecond
)

var (
	// ErrUnknownAgent is a token that is malformed, unknown, retired, of a revoked credential or of an expired one: the
	// listener answers each the same way.
	ErrUnknownAgent = errors.New("accounts: unknown operations credential")
	// ErrAgentNotFound is a credential the control plane names that does not exist or is already revoked.
	ErrAgentNotFound = errors.New("accounts: no such operations credential")
)

// agentRefused are the permissions no credential may hold, whatever the owner asks: the control plane's own, and
// granting platform roles, through which an agent could hand them to a user it controls (docs/domains/operations.md
// 「控制面」). The table's check refuses them too.
var agentRefused = []Permission{OperationsManage, ManagePlatformGrants}

// vendorPattern is a model vendor as the credential names the proposer's, such as deepseek or openai.
var vendorPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,39}$`)

// Agent is an authenticated operations credential: what it may do and where. Only AuthenticateAgent makes one.
type Agent struct {
	id          AgentID
	name        string
	permissions []string // sorted
	sites       []SiteID // nil: every site
	platform    bool
	vendors     []string
	expiresAt   time.Time
}

// ID is the credential's ID.
func (a Agent) ID() AgentID { return a.id }

// AgentSpec is what the owner chooses for a new credential. Permissions are names, as roles hold them.
type AgentSpec struct {
	Name        string
	Permissions []string
	Sites       []SiteID // nil or empty: every site
	Platform    bool
	Vendors     []string
	ExpiresAt   time.Time // the owner's choice for each credential; the code holds no default and no maximum
	CreatedBy   UserID
	Reason      string // the owner's, for the audit log
}

// AgentCredential is a credential as the control plane lists it. It never carries a token.
type AgentCredential struct {
	ID          AgentID
	Name        string
	Permissions []string
	Sites       []SiteID // nil: every site
	Platform    bool
	Vendors     []string
	CreatedBy   UserID
	CreatedAt   time.Time
	ExpiresAt   time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	// Last4 is the current token's last four characters; PreviousUntil is when the token a rotation replaced stops
	// working, nil once it has.
	Last4         string
	PreviousUntil *time.Time
}

// AgentToken is a credential's token, shown once when it is made.
type AgentToken struct {
	ID    AgentID
	Token secret.Secret
}

// CreateAgent makes a credential and its first token in tx, which must be in the platform scope, and audits it as
// actor. A permission the code does not declare, one of agentRefused, a site that does not exist, a vendor that is
// malformed, none at all, or an expiry that is not in the future is an *AgentSpecError.
func (s *Service) CreateAgent(ctx context.Context, tx pg.Tx, actor audit.Actor, spec AgentSpec) (AgentToken, error) {
	now := s.now()
	perms, sites, expires, err := s.checkAgentSpec(spec, now)
	if err != nil {
		return AgentToken{}, err
	}
	id := AgentID(uuid.NewV7())
	if err := s.queries.InsertAgentCredential(ctx, tx, InsertAgentCredentialParams{
		ID: uuid.UUID(id), Name: spec.Name, Permissions: perms, Sites: sites, Platform: spec.Platform,
		ProposerVendors: spec.Vendors, CreatedBy: uuid.UUID(spec.CreatedBy), CreatedAt: now, ExpiresAt: expires,
	}); err != nil {
		return AgentToken{}, fmt.Errorf("create an operations credential: %w", err)
	}
	token, err := s.insertAgentToken(ctx, tx, id, now)
	if err != nil {
		return AgentToken{}, err
	}
	detail := map[string]any{"name": spec.Name, "permissions": perms, "platform": spec.Platform, "vendors": spec.Vendors, "expires_at": expires}
	if sites != nil {
		detail["sites"] = sites
	}
	return AgentToken{id, token}, recordAgent(ctx, tx, actor, "operations_credential.create", id, spec.Reason, detail)
}

// ErrInvalidAgentSpec is a credential the owner described wrongly; the error is an *AgentSpecError that says where.
var ErrInvalidAgentSpec = errors.New("accounts: invalid operations credential")

// AgentSpecError is what is wrong with an AgentSpec: Field is the JSON pointer of the part the request gave, and the
// message never repeats its value.
type AgentSpecError struct{ Field, Message string }

func (e *AgentSpecError) Error() string {
	return ErrInvalidAgentSpec.Error() + ": " + e.Field + ": " + e.Message
}

// Is makes errors.Is(err, ErrInvalidAgentSpec) true for it.
func (*AgentSpecError) Is(target error) bool { return target == ErrInvalidAgentSpec }

func (s *Service) checkAgentSpec(spec AgentSpec, now time.Time) (perms []string, sites []uuid.UUID, expires time.Time, err error) {
	if perms, err = agentPermissions(spec.Permissions); err != nil {
		return nil, nil, time.Time{}, err
	}
	if sites, err = s.agentSites(spec.Sites); err != nil {
		return nil, nil, time.Time{}, err
	}
	if err = checkVendors(spec.Vendors); err != nil {
		return nil, nil, time.Time{}, err
	}
	if !spec.ExpiresAt.After(now) {
		return nil, nil, time.Time{}, &AgentSpecError{"/expires_at", "must be in the future"}
	}
	return perms, sites, spec.ExpiresAt, nil
}

// agentPermissions is names with operations.read, sorted and once each, or what is wrong with one of them.
func agentPermissions(names []string) ([]string, error) {
	perms := []string{OperationsRead.name}
	for i, name := range names {
		p, ok := permissions[name]
		if !ok {
			return nil, &AgentSpecError{fmt.Sprintf("/permissions/%d", i), "not a permission this program declares"}
		}
		if slices.Contains(agentRefused, p) {
			return nil, &AgentSpecError{fmt.Sprintf("/permissions/%d", i), "belongs to the control plane, which no credential may hold"}
		}
		perms = append(perms, name)
	}
	slices.Sort(perms)
	return slices.Compact(perms), nil
}

// agentSites is the sites once each, nil for none (every site), or the first that does not exist.
func (s *Service) agentSites(spec []SiteID) ([]uuid.UUID, error) {
	var sites []uuid.UUID
	known := s.snapshot.Load().sites
	for i, site := range spec {
		if _, ok := known[site]; !ok {
			return nil, &AgentSpecError{fmt.Sprintf("/sites/%d", i), "no such site"}
		}
		if !slices.Contains(sites, uuid.UUID(site)) {
			sites = append(sites, uuid.UUID(site))
		}
	}
	return sites, nil
}

// checkVendors refuses no vendor at all, and one that is not a lower-case name.
func checkVendors(vendors []string) error {
	if len(vendors) == 0 {
		return &AgentSpecError{"/proposer_vendors", "name the vendors whose models propose through it"}
	}
	for i, v := range vendors {
		if !vendorPattern.MatchString(v) {
			return &AgentSpecError{fmt.Sprintf("/proposer_vendors/%d", i), "not a lower-case vendor name"}
		}
	}
	return nil
}

// insertAgentToken makes a new current token of credential id.
func (s *Service) insertAgentToken(ctx context.Context, tx pg.Tx, id AgentID, now time.Time) (secret.Secret, error) {
	text := newKeyText(agentTokenPrefix)
	version := s.versions[0]
	hash := s.hashes.Sum(version, []byte(text))
	if err := s.queries.InsertAgentToken(ctx, tx, InsertAgentTokenParams{
		SecretHash: hash[:], SecretVersion: int64(version), CredentialID: uuid.UUID(id), Last4: text[len(text)-4:], CreatedAt: now,
	}); err != nil {
		return secret.Secret{}, fmt.Errorf("make an operations token: %w", err)
	}
	return secret.New(text), nil
}

// RotateAgent gives a live credential a new token in tx. The token it replaces keeps working for overlap, at most
// AgentOverlap; one an earlier rotation replaced stops now.
func (s *Service) RotateAgent(ctx context.Context, tx pg.Tx, actor audit.Actor, id AgentID, overlap time.Duration, reason string) (AgentToken, error) {
	if overlap < 0 || overlap > AgentOverlap {
		return AgentToken{}, &AgentSpecError{"/overlap_hours", "at most a day"}
	}
	now := s.now()
	if err := s.lockLiveAgent(ctx, tx, id, now); err != nil {
		return AgentToken{}, err
	}
	until := now.Add(overlap)
	if err := s.queries.EndAgentOverlap(ctx, tx, EndAgentOverlapParams{Now: &now, CredentialID: uuid.UUID(id)}); err != nil {
		return AgentToken{}, fmt.Errorf("rotate an operations credential: %w", err)
	}
	if err := s.queries.RetireAgentToken(ctx, tx, RetireAgentTokenParams{RetiresAt: &until, CredentialID: uuid.UUID(id)}); err != nil {
		return AgentToken{}, fmt.Errorf("rotate an operations credential: %w", err)
	}
	token, err := s.insertAgentToken(ctx, tx, id, now)
	if err != nil {
		return AgentToken{}, err
	}
	return AgentToken{id, token}, recordAgent(ctx, tx, actor, "operations_credential.rotate", id, reason, map[string]any{"previous_until": until})
}

// lockLiveAgent locks credential id's row for the rest of tx, or is ErrAgentNotFound when it is revoked, expired or
// unknown.
func (s *Service) lockLiveAgent(ctx context.Context, tx pg.Tx, id AgentID, now time.Time) error {
	rows, err := s.queries.LockAgentCredential(ctx, tx, uuid.UUID(id))
	if err != nil {
		return fmt.Errorf("lock an operations credential: %w", err)
	}
	if len(rows) == 0 || rows[0].RevokedAt != nil || !rows[0].ExpiresAt.After(now) {
		return ErrAgentNotFound
	}
	return nil
}

// RevokeAgent ends credential id in tx: from the next request its tokens are refused, and it never comes back.
func (s *Service) RevokeAgent(ctx context.Context, tx pg.Tx, actor audit.Actor, id AgentID, reason string) error {
	revoked, err := s.queries.RevokeAgentCredential(ctx, tx, RevokeAgentCredentialParams{Now: new(s.now()), ID: uuid.UUID(id)})
	if err != nil {
		return fmt.Errorf("revoke an operations credential: %w", err)
	}
	if len(revoked) == 0 {
		return ErrAgentNotFound
	}
	return recordAgent(ctx, tx, actor, "operations_credential.revoke", id, reason, nil)
}

// StopAgents is the emergency stop's part in accounts (docs/domains/operations.md「紧急停止」): every live credential
// revoked in tx, one audit entry each and one for the stop. It returns how many it revoked; none is not an error, as a
// second press of the button changes nothing.
func (s *Service) StopAgents(ctx context.Context, tx pg.Tx, actor audit.Actor, reason string) (int, error) {
	revoked, err := s.queries.RevokeAllAgentCredentials(ctx, tx, new(s.now()))
	if err != nil {
		return 0, fmt.Errorf("stop the operations agents: %w", err)
	}
	for _, id := range revoked {
		if err := recordAgent(ctx, tx, actor, "operations_credential.revoke", AgentID(id), reason, map[string]any{"stop": true}); err != nil {
			return 0, err
		}
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "operations_agent.stop", Reason: reason,
		Target: audit.Target{Module: "accounts", Kind: "operations_agent", ID: "all"},
		Detail: map[string]any{"revoked": len(revoked)},
	}); err != nil {
		return 0, err
	}
	return len(revoked), nil
}

func recordAgent(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, id AgentID, reason string, detail map[string]any) error {
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason, Detail: detail,
		Target: audit.Target{Module: "accounts", Kind: "operations_credential", ID: id.TypeID()},
	})
}

// AgentCredentials is a page of credentials in tx, newest first, after the credential after (the zero ID: from the
// start), and whether more follow.
func (s *Service) AgentCredentials(ctx context.Context, tx pg.Tx, after AgentID, pageSize int) ([]AgentCredential, bool, error) {
	rows, err := s.queries.ListAgentCredentials(ctx, tx, ListAgentCredentialsParams{After: uuid.UUID(after), PageSize: int32(pageSize + 1)}) // #nosec G115 -- the API bounds the page to 200
	if err != nil {
		return nil, false, fmt.Errorf("list the operations credentials: %w", err)
	}
	more := len(rows) > pageSize
	rows = rows[:min(len(rows), pageSize)]
	now := s.now()
	list := make([]AgentCredential, len(rows))
	for i, r := range rows {
		c := AgentCredential{
			ID: AgentID(r.ID), Name: r.Name, Permissions: r.Permissions, Sites: siteIDs(r.Sites), Platform: r.Platform,
			Vendors: r.ProposerVendors, CreatedBy: UserID(r.CreatedBy), CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt,
			RevokedAt: r.RevokedAt, LastUsedAt: r.LastUsedAt, Last4: r.Last4,
		}
		if r.PreviousRetiresAt != nil && r.PreviousRetiresAt.After(now) && r.RevokedAt == nil && r.ExpiresAt.After(now) {
			c.PreviousUntil = r.PreviousRetiresAt
		}
		list[i] = c
	}
	return list, more, nil
}

func siteIDs(ids []uuid.UUID) []SiteID {
	if ids == nil {
		return nil
	}
	sites := make([]SiteID, len(ids))
	for i, id := range ids {
		sites[i] = SiteID(id)
	}
	return sites
}

// AuthenticateAgent recognises raw as a live credential's token, read from the database on every call, so a revocation,
// a stop or an expiry holds from the next request; no answer is cached. It also spends one of the credential's
// requests on this node: past the burst it is *TooManyAttempts with the wait.
func (s *Service) AuthenticateAgent(ctx context.Context, raw string) (Agent, error) {
	if !strings.HasPrefix(raw, agentTokenPrefix) || !wellFormedKey(raw) {
		return Agent{}, ErrUnknownAgent
	}
	hashes := make([][]byte, len(s.versions))
	for i, v := range s.versions {
		h := s.hashes.Sum(v, []byte(raw))
		hashes[i] = h[:]
	}
	now := s.now()
	var a Agent
	err := s.db.Tx(ctx, s.platform, func(tx pg.Tx) error {
		rows, err := s.queries.AgentByToken(ctx, tx, AgentByTokenParams{Hashes: hashes, Now: &now})
		if err != nil || len(rows) == 0 {
			return err
		}
		r := rows[0]
		a = Agent{
			id: AgentID(r.ID), name: r.Name, permissions: r.Permissions, sites: siteIDs(r.Sites), platform: r.Platform,
			vendors: r.ProposerVendors, expiresAt: r.ExpiresAt,
		}
		slices.Sort(a.permissions)
		if r.LastUsedAt == nil || now.Sub(*r.LastUsedAt) >= agentTouchEvery {
			return s.queries.TouchAgent(ctx, tx, TouchAgentParams{Now: &now, ID: r.ID})
		}
		return nil
	})
	if err != nil {
		return Agent{}, fmt.Errorf("authenticate an operations credential: %w", err)
	}
	if a.id == (AgentID{}) {
		return Agent{}, ErrUnknownAgent
	}
	if wait := s.agentRequests.take(a.id, agentBurst, agentRefill, now); wait > 0 {
		return Agent{}, &TooManyAttempts{RetryAfter: wait}
	}
	return a, nil
}

// AuthorizeAgent returns the scope a may act in with perm: the platform's for an operation on the platform's own
// resources, which needs the credential's platform grant, otherwise the credential's sites. A permission it does not
// hold is ErrForbidden; the platform without the grant is ErrNotFound, as if the resource did not exist.
func (s *Service) AuthorizeAgent(a Agent, perm Permission, platform bool) (pg.Scope, error) {
	if _, held := slices.BinarySearch(a.permissions, perm.name); !held {
		return pg.Scope{}, ErrForbidden
	}
	if platform {
		if !a.platform {
			return pg.Scope{}, ErrNotFound
		}
		return s.platform, nil
	}
	sites := a.sites
	if sites == nil {
		for id := range s.snapshot.Load().sites {
			sites = append(sites, id)
		}
	}
	ids := make([]uuid.UUID, len(sites))
	for i, id := range sites {
		ids[i] = uuid.UUID(id)
	}
	return pg.SitesScope(ids...), nil
}

// AgentInfo is what a credential may know of itself (GET /me): never its token.
type AgentInfo struct {
	ID          AgentID
	Name        string
	Permissions []string
	Sites       []SiteID // nil: every site
	Platform    bool
	Vendors     []string
	ExpiresAt   time.Time
}

// Info is a's own description.
func (a Agent) Info() AgentInfo {
	return AgentInfo{a.id, a.name, a.permissions, a.sites, a.platform, a.vendors, a.expiresAt}
}
