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
	"axiom/internal/suppliers/internal/syncer"
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
	// ErrKeyUnusable is a pasted key the supplier lists as disabled, expired or out of quota: a group connected with it
	// would show a key in use that every call is refused with.
	ErrKeyUnusable = errors.New("suppliers: the supplier lists that key as not usable")
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

// MadeKey is the key a group is connected with when Axiom makes it: its ID there, the group's ref, its text, which is
// only sealed, and what the supplier lets it call as it lists the key. Claim is the group's claim the connection ends.
type MadeKey struct {
	Ref, GroupRef string
	Text          secret.Secret
	Policy        KeyPolicy
	Claim         uuid.UUID
}

// The ways a group's claim on making its key refuses a change (docs/domains/suppliers.md「一个分组只建一把」).
var (
	// ErrKeyInProgress is a group whose key Axiom may be making for it: another request or the sync holds its claim, or
	// a create was sent and its result is not known yet. The key is adopted once the supplier lists it, or a person
	// releases the claim (ReleaseKeyClaim).
	ErrKeyInProgress = errors.New("suppliers: a key for the group is being made")
	// ErrClaimChanged is a release of a claim that is no longer the group's: it was adopted, released, or taken over.
	ErrClaimChanged = errors.New("suppliers: the group's key claim changed since it was read")
)

// MakeKey obtains the dedicated key of one of a relay's groups without ever making a second one (syncer.ObtainKey): the
// key the group's claim knows, one of its erased credentials the supplier still has, one it lists under the marker name
// the sync gives, or one made now under the claim, and reads its text. Whichever it is, it is checked as a pasted key is,
// as the supplier lists it: one disabled, expired or out of quota is ErrKeyUnusable, and a claim with nothing sent is
// ended, while one whose create was sent keeps the key for a person to fix at the supplier or release. A group the supplier no longer lists is ErrGroupGone, and one with a credential is
// ErrGroupHasKey. ConnectGroup checks the claim and the credentials again under the lock.
func (a *Admin) MakeKey(ctx context.Context, r Reach, supplier, group uuid.UUID) (MadeKey, error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return MadeKey{}, err
	}
	var ref string
	if err := r.DB.ReadTx(ctx, r.Scope, func(tx pg.Tx) error {
		g, err := a.group(ctx, tx, supplier, group)
		ref = g.UpstreamRef
		return err
	}); err != nil {
		return MadeKey{}, err
	}
	m, err := a.manager(ctx, r, sup)
	if err != nil {
		return MadeKey{}, err
	}
	claims := persist.NewClaims(r.DB, r.Scope, supplier, a.now)
	got, err := syncer.ObtainKey(ctx, claims, m, syncer.KeyTarget{GroupRef: ref, Name: persist.KeyName(supplier, ref)})
	if err != nil {
		return MadeKey{}, obtainError(err)
	}
	if got.Key.Status != adapter.KeyActive {
		return MadeKey{}, errors.Join(ErrKeyUnusable, syncer.Abandon(ctx, claims, got))
	}
	return MadeKey{Ref: string(got.Key.Ref), GroupRef: ref, Text: got.Text, Policy: policyOf(got.Key.Policy), Claim: got.Claim.ID}, nil
}

// obtainError is a failure to obtain a group's key as the admin names it.
func obtainError(err error) error {
	switch {
	case errors.Is(err, syncer.ErrKeyInProgress):
		return ErrKeyInProgress
	case errors.Is(err, syncer.ErrGroupKeyed):
		return ErrGroupHasKey
	case errors.Is(err, syncer.ErrGroupUnlisted):
		return ErrGroupGone
	case errors.Is(err, adapter.ErrKeyMade):
		// The key exists and its id is not known yet: asking again adopts it once the supplier lists it.
		return ErrSupplierUnanswered
	}
	return keyError(err)
}

// FoundKey is which of the supplier's keys a pasted text is: its ID there, the ref of the group it is listed under,
// the status the supplier gives it, and what the supplier lets it call.
type FoundKey struct {
	Ref, GroupRef string
	Status        string
	Policy        KeyPolicy
}

// KeyPolicy is what the supplier lets a key call, as its listing says: Models nil is every model and an empty list
// none; ExpiresAt nil is never. A key stored with it keeps it until the sync's next complete listing says otherwise.
type KeyPolicy struct {
	Models    []string
	ExpiresAt *time.Time
}

// policyOf is a listed key's policy. A listing that says none cannot prove any permission, so it permits nothing, as the
// sync reads it (persist.projectKeyPolicies).
func policyOf(p *adapter.KeyPolicy) KeyPolicy {
	if p == nil {
		return KeyPolicy{Models: []string{}}
	}
	return KeyPolicy{Models: p.Models, ExpiresAt: p.ExpiresAt}
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
	return FoundKey{Ref: string(k.Ref), GroupRef: k.GroupRef, Status: upstreamStatus(k.Status), Policy: policyOf(k.Policy)}, true, nil
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
	id        uuid.UUID
	origin    string
	apiFormat string // the family a compatible direct API speaks; empty for every other
	adapter   adapter.Adapter
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
	return reachable{id: supplier, origin: row.Origin, apiFormat: deref(row.ApiFormat), adapter: ad}, nil
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

// The causes a check can find.
const (
	CauseRejected          CheckCause = "credential_rejected"
	CauseNotPermitted      CheckCause = "not_permitted"
	CauseQuota             CheckCause = "quota_exhausted"
	CauseWrongGroup        CheckCause = "wrong_group"
	CauseRegionUnavailable CheckCause = "region_unavailable"
	CauseRateLimited       CheckCause = "rate_limited"
	CauseTimeout           CheckCause = "timeout"
	CauseTransport         CheckCause = "transport"
	CauseUpstreamError     CheckCause = "upstream_error"
	CauseUnknown           CheckCause = "unknown"
)

// KeyCheck is one check of a credential: a relay's key, a key pasted for a direct API, or a cloud credential in one
// region (CredentialCheck in the contract). Credential is the credential tested or stored, zero when nothing was
// stored; Index is the key's place in a request of pasted keys; Region is the cloud region checked, empty for any other;
// UpstreamStatus is the supplier's HTTP status, 0 when it answered nothing; CheckedAt is zero for a key that was not
// sent (a duplicate). The key itself is never in it.
type KeyCheck struct {
	Index          int
	Credential     uuid.UUID
	Last4          string
	Region         string
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
func (a *Admin) checkKey(ctx context.Context, exit *egress.Client, quirks adapter.CallQuirks, origin string, key secret.Secret) KeyCheck {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	check := KeyCheck{Last4: hintOf(key.Reveal())}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+checkPath, nil)
	if err != nil {
		check.Outcome, check.Cause, check.CheckedAt = CheckInconclusive, CauseUnknown, a.now()
		return check
	}
	req.Header.Set("Authorization", "Bearer "+key.Reveal())
	resp, err := exit.Do(req)
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
	return a.checkKey(ctx, a.egress, sup.adapter.Calls(), sup.origin, key), nil
}

// CheckCredential sends one check of a stored credential with its own key, through the exit its calls take. A
// credential that is disabled, erased or has no key yet, or whose proxy is disabled with no active fallback, is not
// checked (ErrStatusLocked).
func (a *Admin) CheckCredential(ctx context.Context, r Reach, supplier, credential uuid.UUID) (KeyCheck, error) {
	sup, err := a.supplierToReach(ctx, r, supplier)
	if err != nil {
		return KeyCheck{}, err
	}
	var key secret.Secret
	var last4, region string
	var settings adapter.Settings
	var exit *egress.Client
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
		last4, region, settings = deref(rows[0].Hint), deref(rows[0].Region), settingsOf(rows[0].Settings)
		if exit, err = a.exitFor(ctx, tx, deref(rows[0].EgressID)); err != nil {
			return err
		}
		key, err = a.openKey(ctx, tx, credential)
		return err
	})
	if err != nil {
		return KeyCheck{}, err
	}
	check := a.checkStored(ctx, exit, sup, region, key, settings)
	check.Credential, check.Last4 = credential, last4
	return check, nil
}

// checkStored is one check of a stored credential the way its supplier is reached: a cloud credential in its region, a
// direct API's key at the provider's model list with the provider's own header, a relay's key at /v1/models.
func (a *Admin) checkStored(ctx context.Context, exit *egress.Client, sup reachable, region string, key secret.Secret, s adapter.Settings) KeyCheck {
	switch ad := sup.adapter.(type) {
	case adapter.Cloud:
		got := ad.CheckRegion(ctx, exit, sup.origin, region, key, s, a.now())
		return checkOf(adapter.KeyCheck{Outcome: got.Outcome, Cause: got.Cause, Status: got.Status}, KeyCheck{Region: region}, a.now())
	case adapter.Direct:
		c, err := client.New(sup.origin, exit, client.NewLimiter(nil, a.now), a.now)
		if err != nil {
			return KeyCheck{Outcome: CheckInconclusive, Cause: CauseUnknown, CheckedAt: a.now()}
		}
		family, _ := adapter.FamilyOf(ad.Type(), adapter.Family(sup.apiFormat))
		return checkOf(ad.CheckKey(ctx, c, family, key, s), KeyCheck{}, a.now())
	}
	return a.checkKey(ctx, exit, sup.adapter.Calls(), sup.origin, key)
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
	key, err := a.keys.Open(sealedOf(sealed.Ciphertext, sealed.Nonce, sealed.WrappedDek, *sealed.KeyVersion), keyAt(credential))
	if err != nil {
		return secret.Secret{}, fmt.Errorf("open the key: %w", err)
	}
	return key, nil
}

// sealedOf is an envelope as its four columns hold it.
func sealedOf(ciphertext, nonce, wrapped []byte, version int64) secret.Sealed {
	return secret.Sealed{
		Ciphertext: ciphertext, Nonce: nonce, WrappedDEK: wrapped,
		KeyVersion: uint32(version), //nolint:gosec // the column holds 1 to 2³²-1
	}
}

// keyAt is where a credential's key is sealed to: its own row, so that it opens nowhere else.
func keyAt(credential uuid.UUID) secret.Location {
	return secret.Location{Table: "suppliers.credentials", RowID: credential.String(), Purpose: "key"}
}
