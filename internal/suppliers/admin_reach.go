package suppliers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// The admin's own requests to a supplier (README.md「管理接口」): making a key for a group, finding which of its keys a
// pasted text is, and checking a key with one request the supplier does not charge for. Each runs before the change it
// leads to is written, in no transaction of the caller's, so no connection is held while a third party answers; the
// management requests count in the supplier's shared rate buckets, as the sync's do.

// The ways a request to a supplier is refused.
var (
	// ErrCannotMakeKey is a supplier whose adapter cannot make keys.
	ErrCannotMakeKey = errors.New("suppliers: the supplier's adapter cannot make keys")
	// ErrCannotFindKey is a supplier whose adapter cannot tell which of its keys a text is, so a pasted key cannot be put
	// in a group.
	ErrCannotFindKey = errors.New("suppliers: the supplier's adapter cannot tell which group a key is in")
	// ErrKeyNotListed is a pasted key the supplier does not list.
	ErrKeyNotListed = errors.New("suppliers: the supplier does not list that key")
	// ErrWrongGroup is a pasted key the supplier lists under another group.
	ErrWrongGroup = errors.New("suppliers: the supplier lists that key under another group")
	// ErrSupplierRefused is a supplier that refused the login Axiom has for it: the sync has stopped it too.
	ErrSupplierRefused = errors.New("suppliers: the supplier refused the login")
	// ErrSupplierUnanswered is a supplier that did not answer, answered in a form the adapter does not know, or whose
	// limits leave no request now: nothing is changed, and asking again later may work.
	ErrSupplierUnanswered = errors.New("suppliers: the supplier did not answer in time")
)

// KeyRefusal is a supplier that would not make a key for a group, with the fixed reason: not_permitted (the login may
// not manage keys), group_unavailable (the account may not use the group) or refused.
type KeyRefusal struct{ Reason string }

func (e *KeyRefusal) Error() string {
	return "suppliers: the supplier would not make the key: " + e.Reason
}

// Reach is where an admin request to a supplier finds what it needs: the database its login and its shared rate buckets
// are in, and the scope that sees them (the platform's).
type Reach struct {
	DB    *pg.DB
	Scope pg.Scope
}

// MadeKey is a key made for a group at the supplier: its ID there, the group's ref, and its text, which is only sealed.
type MadeKey struct {
	Ref, GroupRef string
	Text          secret.Secret
}

// MakeKey makes a dedicated key for one of a relay's groups, named as the sync names the keys it makes, and reads its
// text. A key whose answer was lost is adopted by that name by the sync's next listing, not made twice.
func (a *Admin) MakeKey(ctx context.Context, r Reach, supplier, group uuid.UUID) (MadeKey, error) {
	sup, ref, err := a.groupToReach(ctx, r, supplier, group)
	if err != nil {
		return MadeKey{}, err
	}
	m, err := a.manager(ctx, r, sup)
	if err != nil {
		return MadeKey{}, err
	}
	maker, ok := m.(adapter.KeyMaker)
	if !ok {
		return MadeKey{}, ErrCannotMakeKey
	}
	made, err := maker.CreateKey(ctx, ref, persist.KeyName(supplier, ref))
	if err != nil {
		return MadeKey{}, keyError(err)
	}
	text, err := m.KeySecret(ctx, made)
	if err != nil {
		return MadeKey{}, keyError(err)
	}
	return MadeKey{Ref: string(made), GroupRef: ref, Text: text}, nil
}

// FoundKey is which of the supplier's keys a pasted text is: its ID there, the ref of the group it is listed under,
// and the status the supplier gives it.
type FoundKey struct {
	Ref, GroupRef string
	Status        string
}

// FindKey asks the supplier which of its keys text is. found is false when the supplier lists no such key.
func (a *Admin) FindKey(ctx context.Context, r Reach, supplier uuid.UUID, text secret.Secret) (key FoundKey, found bool, err error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return FoundKey{}, false, err
	}
	m, err := a.manager(ctx, r, sup)
	if err != nil {
		return FoundKey{}, false, err
	}
	finder, ok := m.(adapter.KeyFinder)
	if !ok {
		return FoundKey{}, false, ErrCannotFindKey
	}
	k, found, err := finder.FindKey(ctx, text)
	if err != nil || !found {
		return FoundKey{}, false, keyError(err)
	}
	return FoundKey{Ref: string(k.Ref), GroupRef: k.GroupRef, Status: upstreamStatus(k.Status)}, true, nil
}

// upstreamStatus is a key's status at the supplier as the contract names it.
func upstreamStatus(s adapter.KeyStatus) string {
	switch s {
	case adapter.KeyActive:
	case adapter.KeyDisabled:
		return "disabled"
	case adapter.KeyQuotaExhausted:
		return "quota_exhausted"
	case adapter.KeyExpired:
		return "expired"
	}
	return "active"
}

// keyError is a refusal of a key request as the admin names it; nil stays nil.
func keyError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, adapter.ErrKeyNotPermitted):
		return &KeyRefusal{Reason: "not_permitted"}
	case errors.Is(err, adapter.ErrKeyGroupUnavailable):
		return &KeyRefusal{Reason: "group_unavailable"}
	case errors.Is(err, adapter.ErrKeyRefused):
		return &KeyRefusal{Reason: "refused"}
	case errors.Is(err, adapter.ErrNoKeyMaker):
		return ErrCannotMakeKey
	case errors.Is(err, adapter.ErrNoKeyFinder):
		return ErrCannotFindKey
	case errors.Is(err, adapter.ErrAuthRejected):
		return ErrSupplierRefused
	}
	// What else a supplier answers says nothing a person can act on but to try again; its words stay out of the error.
	return ErrSupplierUnanswered
}

// reachable is a supplier as a request to it needs it.
type reachable struct {
	id      uuid.UUID
	origin  string
	adapter adapter.Adapter
}

// supplierToReach reads a platform supplier that a request can be sent to: one Axiom logs in to, in use or paused,
// with an adapter this node has.
func (a *Admin) supplierToReach(ctx context.Context, r Reach, supplier uuid.UUID) (reachable, error) {
	var row store.GetSupplierViewRow
	err := r.DB.ReadTx(ctx, r.Scope, func(tx pg.Tx) error {
		rows, err := store.New().GetSupplierView(ctx, tx, store.GetSupplierViewParams{ID: supplier})
		if err != nil {
			return fmt.Errorf("read the supplier: %w", err)
		}
		if len(rows) == 0 {
			return ErrNoSupplier
		}
		row = rows[0]
		return nil
	})
	if err != nil {
		return reachable{}, err
	}
	if !slices.Contains([]Status{Active, LowBalance, Paused}, Status(row.Status)) {
		return reachable{}, fmt.Errorf("%w: it is %s", ErrStatusLocked, row.Status)
	}
	ad, ok := a.registry.Get(adapter.Kind(row.Kind))
	if !ok {
		return reachable{}, ErrNoAdapter
	}
	return reachable{id: supplier, origin: row.Origin, adapter: ad}, nil
}

// groupToReach is supplierToReach for a group of the supplier, with the group's ref there.
func (a *Admin) groupToReach(ctx context.Context, r Reach, supplier, group uuid.UUID) (reachable, string, error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return reachable{}, "", err
	}
	var ref string
	err = r.DB.ReadTx(ctx, r.Scope, func(tx pg.Tx) error {
		g, err := a.group(ctx, tx, supplier, group)
		ref = g.UpstreamRef
		return err
	})
	return sup, ref, err
}

// manager logs in to the supplier with the login the sync uses. A login the adapter would refresh is not stored from
// here: the sync's lease owns that, so such a request fails and the sync's next round refreshes it.
func (a *Admin) manager(ctx context.Context, r Reach, sup reachable) (adapter.Manager, error) {
	raw, _, err := persist.ReadLogin(ctx, r.DB, r.Scope, a.keys, sup.id)
	if err != nil {
		return nil, err
	}
	buckets := client.NewSharedLimiter(r.DB, r.Scope, sup.adapter.Buckets(),
		client.Scopes{Address: "ip:" + sup.origin + "|" + string(egress.Direct), Account: "account:" + sup.id.String()}, a.now)
	c, err := client.New(sup.origin, a.egress, buckets, a.now)
	if err != nil {
		return nil, err
	}
	m, err := sup.adapter.Open(ctx, c, staticLogin{raw: raw})
	if err != nil {
		return nil, keyError(err)
	}
	return m, nil
}

// staticLogin gives an adapter the login as it was read, as the usage collector does; a refreshed one is not stored.
type staticLogin struct{ raw []byte }

func (s staticLogin) Load(context.Context) ([]byte, int64, error) { return s.raw, 0, nil }
func (staticLogin) Swap(context.Context, int64, []byte) error     { return adapter.ErrStaleLogin }

// CheckOutcome is what a check of a key found (CredentialCheckOutcome in the contract).
type CheckOutcome string

// The outcomes.
const (
	CheckPassed       CheckOutcome = "passed"
	CheckFailed       CheckOutcome = "failed"
	CheckInconclusive CheckOutcome = "inconclusive"
	CheckDuplicate    CheckOutcome = "duplicate"
)

// CheckCause is why a check did not pass (CredentialCheckCause in the contract); empty when it passed.
type CheckCause string

// The causes a check of a relay's key can find.
const (
	CauseRejected      CheckCause = "credential_rejected"
	CauseNotPermitted  CheckCause = "not_permitted"
	CauseQuota         CheckCause = "quota_exhausted"
	CauseWrongGroup    CheckCause = "wrong_group"
	CauseRateLimited   CheckCause = "rate_limited"
	CauseTimeout       CheckCause = "timeout"
	CauseTransport     CheckCause = "transport"
	CauseUpstreamError CheckCause = "upstream_error"
	CauseUnknown       CheckCause = "unknown"
)

// KeyCheck is one key's check. Credential is the credential tested or stored, zero when nothing was stored; Index is the
// key's place in a request of pasted keys.
type KeyCheck struct {
	Index          int
	Credential     uuid.UUID
	Last4          string
	Outcome        CheckOutcome
	Cause          CheckCause
	UpstreamStatus int
	CheckedAt      time.Time
}

// checkTimeout bounds a check: one small read that answers at once when the key is good.
const checkTimeout = 10 * time.Second

// checkPath is what a check asks for: the models the key may call, which a relay answers without charging, and which
// needs the key alone (docs/domains/suppliers.md「凭证检测」).
const checkPath = "/v1/models"

// checkKey sends one check of key to origin. The answer's body is read only as far as the adapter needs to tell a
// refusal's kind, and is dropped: it can hold the account's data.
func (a *Admin) checkKey(ctx context.Context, quirks adapter.CallQuirks, origin string, key secret.Secret) KeyCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	check := KeyCheck{Last4: hintOf(key.Reveal())}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+checkPath, nil)
	if err != nil {
		check.Outcome, check.Cause, check.CheckedAt = CheckInconclusive, CauseUnknown, a.now()
		return check
	}
	req.Header.Set("Authorization", "Bearer "+key.Reveal())
	resp, err := a.egress.Do(req)
	check.CheckedAt = a.now()
	if err != nil {
		check.Outcome, check.Cause = CheckInconclusive, CauseTransport
		if ctx.Err() != nil {
			check.Cause = CauseTimeout
		}
		return check
	}
	defer resp.Body.Close()                                        //nolint:errcheck // the body was read or abandoned; closing it says nothing about the key
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody)) //nolint:errcheck // a body that fails to read is judged by its status
	check.UpstreamStatus = resp.StatusCode
	check.Outcome, check.Cause = judgeCheck(quirks, resp.StatusCode, resp.Header, body)
	return check
}

// judgeCheck is what an answer to a check says of the key. 401 is the key refused, as for an account's own key; 403 is
// the key's own quota when the adapter says so and otherwise inconclusive, because it can be a block of the region; the
// rest say nothing of the key.
func judgeCheck(quirks adapter.CallQuirks, status int, h http.Header, body []byte) (CheckOutcome, CheckCause) {
	switch {
	case status >= 200 && status < 300:
		return CheckPassed, ""
	case status == http.StatusUnauthorized:
		return CheckFailed, CauseRejected
	case status == http.StatusForbidden:
		if _, quota := quirks.Classify(status, h, body).Cause.(adapter.CredentialQuotaExhausted); quota {
			return CheckFailed, CauseQuota
		}
		return CheckInconclusive, CauseNotPermitted
	case status == http.StatusTooManyRequests:
		return CheckInconclusive, CauseRateLimited
	case status >= http.StatusInternalServerError:
		return CheckInconclusive, CauseUpstreamError
	}
	return CheckInconclusive, CauseUnknown
}

// CheckKey sends one check of a key a person pasted for the supplier, which is not stored.
func (a *Admin) CheckKey(ctx context.Context, r Reach, supplier uuid.UUID, key secret.Secret) (KeyCheck, error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return KeyCheck{}, err
	}
	return a.checkKey(ctx, sup.adapter.Calls(), sup.origin, key), nil
}

// CheckCredential sends one check of a stored credential with its own key. A credential that is disabled, erased or has
// no key yet is not checked (ErrStatusLocked).
func (a *Admin) CheckCredential(ctx context.Context, r Reach, supplier, credential uuid.UUID) (KeyCheck, error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return KeyCheck{}, err
	}
	var key secret.Secret
	var last4 string
	err = r.DB.ReadTx(ctx, r.Scope, func(tx pg.Tx) error {
		rows, err := store.New().GetCredentialOf(ctx, tx, store.GetCredentialOfParams{SupplierID: supplier, ID: credential})
		if err != nil {
			return fmt.Errorf("read the credential: %w", err)
		}
		if len(rows) == 0 {
			return ErrNoCredential
		}
		if status := rows[0].Status; status == "disabled" || status == "erased" || status == "binding" {
			return fmt.Errorf("%w: the credential is %s", ErrStatusLocked, status)
		}
		last4 = deref(rows[0].Hint)
		key, err = a.openKey(ctx, tx, credential)
		return err
	})
	if err != nil {
		return KeyCheck{}, err
	}
	check := a.checkKey(ctx, sup.adapter.Calls(), sup.origin, key)
	check.Credential, check.Last4 = credential, last4
	return check, nil
}

// openKey opens a credential's sealed key for one request. The caller sends it in one header and keeps it nowhere.
func (a *Admin) openKey(ctx context.Context, tx pg.Tx, credential uuid.UUID) (secret.Secret, error) {
	sealed, err := store.New().GetCredentialSecret(ctx, tx, credential)
	if err != nil {
		return secret.Secret{}, fmt.Errorf("read the key: %w", err)
	}
	if sealed.KeyVersion == nil {
		return secret.Secret{}, fmt.Errorf("%w: the credential holds no key", ErrStatusLocked)
	}
	key, err := a.keys.Open(secret.Sealed{
		Ciphertext: sealed.Ciphertext, Nonce: sealed.Nonce, WrappedDEK: sealed.WrappedDek,
		KeyVersion: uint32(*sealed.KeyVersion), //nolint:gosec // the column holds 1 to 2³²-1
	}, keyAt(credential))
	if err != nil {
		return secret.Secret{}, fmt.Errorf("open the key: %w", err)
	}
	return key, nil
}

// keyAt is where a credential's key is sealed to: its own row, so that it opens nowhere else.
func keyAt(credential uuid.UUID) secret.Location {
	return secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"}
}
