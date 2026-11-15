package accounts

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// ErrManagementBusy is a request that could not take a database admission slot.
var ErrManagementBusy = errors.New("accounts: management admission unavailable")

// ManagementRateLimit is a shared per-key minute budget exhausted on the database's clock.
type ManagementRateLimit struct{ RetryAfter time.Duration }

func (*ManagementRateLimit) Error() string { return "accounts: management request rate exceeded" }

// KeyManager is an authenticated account key administrator, valid only inside WithKeyManager's callback.
// Its scope and identity cannot be supplied by a caller; management keys themselves are never targets.
type KeyManager struct {
	service *Service
	tx      pg.Tx
	caller  Caller
	ceiling Key
}

// Ceiling is the current delegation ceiling; omitted create fields inherit it at the HTTP boundary.
func (m KeyManager) Ceiling() Key {
	k := m.ceiling
	k.Models, k.IPAllow = slices.Clone(k.Models), slices.Clone(k.IPAllow)
	if k.SpendLimit != nil {
		k.SpendLimit = new(*k.SpendLimit)
	}
	if k.ExpiresAt != nil {
		k.ExpiresAt = new(*k.ExpiresAt)
	}
	if k.UpdatedAt != nil {
		k.UpdatedAt = new(*k.UpdatedAt)
	}
	return k
}

// WithKeyManager charges a shared rate slot, then revalidates and locks mutable authorization in the operation's
// transaction. A failed operation rolls back its changes and audit, but cannot return the committed rate slot.
func (s *Service) WithKeyManager(ctx context.Context, c Caller, r AdmitRequest, fn func(KeyManager, pg.Tx) error) error {
	if c == (Caller{}) {
		return ErrUnknownKey
	}
	release, err := s.slot(ctx)
	if err != nil {
		return errors.Join(ErrManagementBusy, err)
	}
	defer release()
	scope := pg.AccountScopeIn(uuid.UUID(c.site), uuid.UUID(c.account))
	var rate ChargeManagementRequestRow
	err = s.db.Tx(ctx, scope, func(tx pg.Tx) error {
		if _, err := s.lockManager(ctx, tx, c, r); err != nil {
			return err
		}
		var err error
		rate, err = s.queries.ChargeManagementRequest(ctx, tx, uuid.UUID(c.key))
		return err
	})
	if err != nil {
		return err
	}
	if !rate.Allowed {
		return &ManagementRateLimit{time.Duration(rate.RetrySeconds) * time.Second}
	}
	return s.db.Tx(ctx, scope, func(tx pg.Tx) error {
		key, err := s.lockManager(ctx, tx, c, r)
		if err != nil {
			return err
		}
		s.observeKeyUse(c)
		return fn(KeyManager{s, tx, c, key}, tx)
	})
}

func (s *Service) lockManager(ctx context.Context, tx pg.Tx, c Caller, r AdmitRequest) (Key, error) {
	if _, err := s.queries.LockKeyKind(ctx, tx, LockKeyKindParams{ID: uuid.UUID(c.key), Kind: ManagementKey}); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return Key{}, ErrUnknownKey
		}
		return Key{}, err
	}
	row, err := s.queries.ValidManagementKey(ctx, tx, ValidManagementKeyParams{
		ID: uuid.UUID(c.key), Domain: strings.ToLower(r.Domain), ClientIp: r.ClientIP,
	})
	if errors.Is(err, pg.ErrNoRows) {
		return Key{}, ErrUnknownKey
	}
	if err != nil {
		return Key{}, err
	}
	return keyOf(row), nil
}

// ManagedKeyPage is OpenRouter's offset list selection; each page holds at most 100 call keys.
type ManagedKeyPage struct {
	Offset                          int64
	IncludeDisabled, IncludeExpired bool
	Workspace                       *WorkspaceID // nil: the default workspace
}

// List returns only call keys of this account, including disabled or expired keys when requested.
func (m KeyManager) List(ctx context.Context, p ManagedKeyPage) ([]Key, error) {
	var workspace *uuid.UUID
	if p.Workspace != nil {
		id := uuid.UUID(*p.Workspace)
		workspace = &id
	}
	rows, err := m.service.queries.ListManagedKeys(ctx, m.tx, ListManagedKeysParams{
		AccountID: uuid.UUID(m.caller.account), PageOffset: p.Offset, IncludeDisabled: p.IncludeDisabled,
		IncludeExpired: p.IncludeExpired, Workspace: workspace,
	})
	if err != nil {
		return nil, err
	}
	keys := make([]Key, len(rows))
	for i, row := range rows {
		keys[i] = keyOf(row)
	}
	return keys, nil
}

// Get locks a live call key before reading, so PATCH's read/merge/write cannot lose a concurrent console change.
func (m KeyManager) Get(ctx context.Context, id KeyID) (Key, error) {
	if _, err := m.service.queries.LockKeyKind(ctx, m.tx, LockKeyKindParams{ID: uuid.UUID(id), Kind: CallKey}); err != nil {
		if errors.Is(err, pg.ErrNoRows) {
			return Key{}, ErrKeyNotFound
		}
		return Key{}, err
	}
	return m.service.GetKey(ctx, m.tx, id)
}

// Create issues a call key within this manager's current authority. Workspace is resolved inside its account scope.
func (m KeyManager) Create(ctx context.Context, workspace *WorkspaceID, expires *time.Time, k KeySettings) (CreatedKey, error) {
	if expires == nil {
		expires = m.ceiling.ExpiresAt
	}
	if err := m.permits(k, expires); err != nil {
		return CreatedKey{}, err
	}
	created, err := m.service.createKey(ctx, m.tx, audit.ManagementKey(uuid.UUID(m.caller.key)), KeyRequest{
		Site: m.caller.site, Account: m.caller.account, Workspace: workspace, CreatedBy: m.ceiling.CreatedBy,
		ExpiresAt: expires, KeySettings: k,
	}, false)
	if errors.Is(err, ErrWorkspaceNotFound) || errors.Is(err, ErrWorkspaceDisabled) {
		// The management API has answered an unusable workspace as an unknown key since before workspaces were managed.
		return CreatedKey{}, ErrKeyNotFound
	}
	return created, err
}

// Update permits bounded grants and reductions of existing authority, including disabling older permanent keys. A new
// routing preset is no reduction: it may route to dearer endpoints, so the key it leaves must be within the ceiling.
func (m KeyManager) Update(ctx context.Context, id KeyID, k KeySettings) (Key, error) {
	before, err := m.Get(ctx, id)
	if err != nil {
		return Key{}, err
	}
	reducing := (!before.Disabled || k.Disabled) && settingsWithin(k, before.KeySettings) == nil &&
		(k.Preset == nil || sameOptional(k.Preset, before.Preset))
	if !reducing {
		if err := m.permits(k, before.ExpiresAt); err != nil {
			return Key{}, err
		}
	}
	return m.service.UpdateKey(ctx, m.tx, audit.ManagementKey(uuid.UUID(m.caller.key)), id, k)
}

// Delete irreversibly removes a call key; a manager cannot target itself or any other management key.
func (m KeyManager) Delete(ctx context.Context, id KeyID) error {
	if _, err := m.Get(ctx, id); err != nil {
		return err
	}
	return m.service.DeleteKey(ctx, m.tx, audit.ManagementKey(uuid.UUID(m.caller.key)), id)
}

func (m KeyManager) permits(k KeySettings, expires *time.Time) error {
	if expires == nil || expires.After(*m.ceiling.ExpiresAt) {
		return ErrForbidden
	}
	return settingsWithin(k, m.ceiling.KeySettings)
}

func settingsWithin(k, p KeySettings) error {
	if p.SpendLimit != nil && (k.SpendLimit == nil || *k.SpendLimit > *p.SpendLimit || k.LimitReset != p.LimitReset) {
		return ErrForbidden
	}
	if len(p.Models) > 0 {
		if len(k.Models) == 0 {
			return ErrForbidden
		}
		for _, model := range k.Models {
			if !slices.Contains(p.Models, model) {
				return ErrForbidden
			}
		}
	}
	if !networksWithin(k.IPAllow, p.IPAllow) {
		return ErrForbidden
	}
	return nil
}

func networksWithin(child, parent []netip.Prefix) bool {
	if len(parent) == 0 {
		return true
	}
	if len(child) == 0 {
		return false
	}
	for _, c := range child {
		contained := false
		for _, p := range parent {
			if p.Addr().BitLen() == c.Addr().BitLen() && p.Bits() <= c.Bits() && p.Contains(c.Addr()) {
				contained = true
				break
			}
		}
		if !contained {
			return false
		}
	}
	return true
}
