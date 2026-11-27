package suppliers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
)

// A relay's keys as a person manages them (docs/domains/suppliers.md「Key 管理」): a group connected with a key made for
// it or pasted, pasted keys added to a group, checks, the switch, and erasing. Every write takes the supplier's row
// first, as every other change of a supplier does, so it is ordered with the sync's step and never crosses it.

// The ways a change of a key is refused.
var (
	// ErrGroupHasKey is a group that already has a credential: it is paused and resumed by selecting it.
	ErrGroupHasKey = errors.New("suppliers: the group already has a key")
	// ErrTooManyCredentials is a check of every credential that would test more than MaxCredentialTests.
	ErrTooManyCredentials = fmt.Errorf("suppliers: more than %d credentials to test", MaxCredentialTests)
)

// MaxCredentialTests is the most credentials one check of every key tests: each is a request to a third party, and the
// answer has to come within the request.
const MaxCredentialTests = 200

// CredentialQuery chooses the credentials to list, newest first. Zero values are no filter; After is the last credential
// of the page before.
type CredentialQuery struct {
	Supplier uuid.UUID
	Status   string
	Mode     string
	After    uuid.UUID
	PageSize int
}

// Credentials lists a page of the platform's suppliers' credentials; more tells whether some remain beyond it.
func (*Admin) Credentials(ctx context.Context, tx pg.Tx, q CredentialQuery) (page []Credential, more bool, err error) {
	params := store.CredentialsPageParams{PageSize: int32(q.PageSize + 1)} // #nosec G115 -- a page is at most 200
	if q.Supplier != (uuid.UUID{}) {
		params.SupplierID = &q.Supplier
	}
	if q.Status != "" {
		params.Status = &q.Status
	}
	if q.Mode != "" {
		params.Mode = &q.Mode
	}
	if q.After != (uuid.UUID{}) {
		params.After = &q.After
	}
	rows, err := store.New().CredentialsPage(ctx, tx, params)
	if err != nil {
		return nil, false, fmt.Errorf("list the credentials: %w", err)
	}
	if more = len(rows) > q.PageSize; more {
		rows = rows[:q.PageSize]
	}
	page = make([]Credential, len(rows))
	for i, r := range rows {
		page[i] = credentialOf(credentialFacts(r))
	}
	return page, more, nil
}

// GroupKey is the key a group is connected with: one Axiom made at the supplier or found there under the marker name
// (Source made, from MakeKey) or one a person pasted that the supplier lists under the group (Source pasted). Ref is
// the supplier's ID of it; Text is only sealed; Policy is what the supplier lets it call, which a key Axiom made is not
// limited in. Claim is the group's claim a made key settles.
type GroupKey struct {
	Source   string
	Ref      string
	GroupRef string
	Text     secret.Secret
	Policy   KeyPolicy
	Claim    uuid.UUID
}

// ConnectGroup selects a group and gives it the key, in one change: the credential starts active with
// defaultMaxConcurrency calls at once and no per-minute ceiling. A pasted key must be listed under this group
// (ErrWrongGroup), and a group with a credential is ErrGroupHasKey: a person pauses and resumes it with SelectGroup,
// keeping its key. The one exception is a made key the group holds already, stored by the sync while the request asked
// for it: the connection completes with that credential. A made key is stored only under its own claim or the one
// that knows it, and a pasted key not while a claim may still lead to a made one (ErrKeyInProgress).
func (a *Admin) ConnectGroup(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, key GroupKey, reason string) (Group, Credential, error) {
	row, err := a.connectable(ctx, tx, supplier, group, key)
	if err != nil {
		return Group{}, Credential{}, err
	}
	c, err := a.storeConnection(ctx, tx, supplier, group, key)
	if err != nil {
		return Group{}, Credential{}, err
	}
	if !row.Selected {
		if err := a.chooseGroup(ctx, tx, group, true, row.Codename == nil); err != nil {
			return Group{}, Credential{}, err
		}
	}
	if err := store.New().BumpConfigGen(ctx, tx, supplier); err != nil {
		return Group{}, Credential{}, fmt.Errorf("raise the generation: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier_group.connect", supplier, reason, map[string]any{
		"group": group.String(), "credential": c.ID.String(), "key_source": key.Source, "upstream_key_ref": key.Ref,
	}); err != nil {
		return Group{}, Credential{}, err
	}
	if err := persist.ObserveInbox(ctx, tx, supplier, a.now(), persist.InboxGroups, false); err != nil {
		return Group{}, Credential{}, err
	}
	g, err := a.group(ctx, tx, supplier, group)
	if err != nil {
		return Group{}, Credential{}, err
	}
	g.KeyState, g.KeyReason = "ready", ""
	return g, c, nil
}

// connectable is the group, locked after its supplier, when the key can connect it: it is the supplier's, still listed,
// the key's, and has no credential yet, but the made key itself; and its claim allows the key (claimAllows).
func (a *Admin) connectable(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID, key GroupKey) (store.LockGroupRow, error) {
	row, err := lockedChild(ctx, a, tx, supplier, ErrNoGroup, "the group", func() ([]store.LockGroupRow, error) {
		return store.New().LockGroup(ctx, tx, store.LockGroupParams{ID: group, SupplierID: supplier})
	})
	switch {
	case err != nil:
		return store.LockGroupRow{}, err
	case !row.Present:
		return store.LockGroupRow{}, ErrGroupGone
	case key.GroupRef != row.UpstreamRef:
		return store.LockGroupRow{}, ErrWrongGroup
	}
	return row, a.claimAllows(ctx, tx, supplier, group, key)
}

// storedKey is a key to store as a credential of a group, sealed to its row, with what the supplier lets it call.
type storedKey struct {
	source, ref string
	text        secret.Secret
	policy      KeyPolicy
	limits      Limits
	check       *KeyCheck
}

// storeKey writes the credential of a key: ErrKeyBound when the supplier's key is stored already.
func (a *Admin) storeKey(ctx context.Context, tx pg.Tx, supplier, group uuid.UUID, k storedKey) (Credential, error) {
	if !k.limits.valid() {
		return Credential{}, ErrLimits
	}
	id := uuid.NewV7()
	sealed := a.keys.Seal(k.text, keyAt(id))
	version := int64(sealed.KeyVersion)
	concurrency, perMinute := k.limits.columns()
	params := store.InsertSealedCredentialParams{
		ID: id, SupplierID: supplier, SupplierGroupID: group, UpstreamKeyRef: k.ref, Ciphertext: sealed.Ciphertext,
		Nonce: sealed.Nonce, WrappedDek: sealed.WrappedDEK, KeyVersion: &version,
		MaxConcurrency: concurrency, RequestsPerMinute: perMinute, Source: &k.source,
		AllowedModels: k.policy.Models, ExpiresAt: k.policy.ExpiresAt,
	}
	if k.source == "pasted" {
		last4 := hintOf(k.text.Reveal())
		params.Hint = &last4
	}
	if k.check != nil {
		outcome, status := string(k.check.Outcome), statusColumn(k.check.UpstreamStatus)
		params.CheckedAt, params.CheckOutcome, params.CheckStatus = &k.check.CheckedAt, &outcome, &status
	}
	err := store.New().InsertSealedCredential(ctx, tx, params)
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Credential{}, ErrKeyBound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("store the key: %w", err)
	}
	return a.credential(ctx, tx, supplier, id)
}

// statusColumn is an HTTP status as the check_status column holds it; anything outside the three-digit range is no
// status and is stored as 0.
func statusColumn(status int) int32 {
	if status < 0 || status > 999 {
		return 0
	}
	return int32(status)
}

// credential reads one credential of the supplier.
func (*Admin) credential(ctx context.Context, tx pg.Tx, supplier, id uuid.UUID) (Credential, error) {
	rows, err := store.New().GetCredentialOf(ctx, tx, store.GetCredentialOfParams{SupplierID: supplier, ID: id})
	if err != nil {
		return Credential{}, fmt.Errorf("read the credential: %w", err)
	}
	if len(rows) == 0 {
		return Credential{}, ErrNoCredential
	}
	return credentialOf(credentialFacts(rows[0])), nil
}

// CredentialByRef is the credential the supplier's key ref is stored as, if any.
func (*Admin) CredentialByRef(ctx context.Context, tx pg.Tx, supplier uuid.UUID, ref string) (uuid.UUID, bool, error) {
	ids, err := store.New().CredentialByRef(ctx, tx, store.CredentialByRefParams{SupplierID: supplier, UpstreamKeyRef: ref})
	if err != nil || len(ids) == 0 {
		return uuid.UUID{}, false, err
	}
	return ids[0], true, nil
}

// PastedKey is a pasted key that passed its check, to be stored under the ref the supplier lists it by, with what the
// supplier lets it call.
type PastedKey struct {
	Ref    string
	Text   secret.Secret
	Policy KeyPolicy
	Check  KeyCheck
}

// AddKeys stores pasted keys of one group, each as its own credential with the limits given. A key whose ref is stored
// already, by the sync or earlier, is not stored again: its check comes back duplicate. checks are in the order of keys.
func (a *Admin) AddKeys(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, group uuid.UUID, keys []PastedKey, limits Limits, reason string) ([]KeyCheck, error) {
	if !limits.valid() {
		return nil, ErrLimits
	}
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return nil, err
	}
	if _, err := a.group(ctx, tx, supplier, group); err != nil {
		return nil, err
	}
	if err := unclaimed(persist.Unclaimed(ctx, tx, supplier, group, a.now())); err != nil {
		return nil, err
	}
	checks := make([]KeyCheck, len(keys))
	var stored []string
	for i, k := range keys {
		check := k.Check
		c, err := a.storeKey(ctx, tx, supplier, group, storedKey{source: "pasted", ref: k.Ref, text: k.Text, policy: k.Policy, limits: limits, check: &check})
		switch {
		case errors.Is(err, ErrKeyBound):
			check.Outcome, check.Cause = CheckDuplicate, ""
		case err != nil:
			return nil, err
		default:
			check.Credential = c.ID
			stored = append(stored, c.ID.String())
		}
		checks[i] = check
	}
	if len(stored) == 0 {
		return checks, nil
	}
	if err := a.record(ctx, tx, actor, "credential.paste", supplier, reason, map[string]any{
		"group": group.String(), "credentials": stored,
	}); err != nil {
		return nil, err
	}
	return checks, nil
}

// RecordCheck writes what a check of a stored credential found: a pass puts a rejected credential back to active, and
// a refusal as not valid makes it rejected, so routing stops picking it; any other outcome changes no status. A
// credential a person disabled or erased since the check was sent keeps its status, and its check is recorded. The
// queue is brought in line every time, so a check also mends an item a failed write of a call's refusal left out.
func (a *Admin) RecordCheck(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, check KeyCheck, reason string) (Credential, error) {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Credential{}, err
	}
	q := store.New()
	rows, err := q.LockCredentialOf(ctx, tx, store.LockCredentialOfParams{SupplierID: supplier, ID: check.Credential})
	if err != nil {
		return Credential{}, fmt.Errorf("lock the credential: %w", err)
	}
	if len(rows) == 0 {
		return Credential{}, ErrNoCredential
	}
	from := rows[0].Status
	to := checkedStatus(from, check)
	params := store.RecordCredentialCheckParams{
		SupplierID: supplier, ID: check.Credential, Status: to, CheckedAt: &check.CheckedAt,
		Outcome: new(string(check.Outcome)), UpstreamStatus: new(statusColumn(check.UpstreamStatus)),
	}
	if check.Cause != "" {
		params.Cause = new(string(check.Cause))
	}
	if err := q.RecordCredentialCheck(ctx, tx, params); err != nil {
		return Credential{}, fmt.Errorf("record the check: %w", err)
	}
	if err := a.record(ctx, tx, actor, "credential.test", supplier, reason, map[string]any{
		"credential": check.Credential.String(), "outcome": string(check.Outcome), "cause": string(check.Cause), "from": from, "to": to,
	}); err != nil {
		return Credential{}, err
	}
	if err := persist.ObserveInbox(ctx, tx, supplier, a.now(), persist.InboxKeys, false); err != nil {
		return Credential{}, err
	}
	return a.credential(ctx, tx, supplier, check.Credential)
}

// checkedStatus is the status a check leaves: only rejected and the statuses a key serves in move.
func checkedStatus(from string, check KeyCheck) string {
	switch {
	case slices.Contains([]string{"disabled", "erased", "binding"}, from):
		return from
	case check.Outcome == CheckPassed && from == "rejected":
		return "active"
	case check.Outcome == CheckFailed && check.Cause == CauseRejected:
		return "rejected"
	}
	return from
}

// CredentialsToTest are the credentials a check of every key tests, of one supplier or of all (zero), in ID order, with
// how many were left out as disabled. More than MaxCredentialTests is ErrTooManyCredentials.
func (*Admin) CredentialsToTest(ctx context.Context, tx pg.Tx, supplier uuid.UUID) (credentials []CredentialRef, disabled int, err error) {
	q := store.New()
	params := store.CredentialsToTestParams{MaxCount: MaxCredentialTests + 1}
	var of *uuid.UUID
	if supplier != (uuid.UUID{}) {
		of = &supplier
		params.SupplierID = of
	}
	rows, err := q.CredentialsToTest(ctx, tx, params)
	if err != nil {
		return nil, 0, fmt.Errorf("list the credentials to test: %w", err)
	}
	if len(rows) > MaxCredentialTests {
		return nil, 0, ErrTooManyCredentials
	}
	n, err := q.CountDisabledCredentials(ctx, tx, of)
	if err != nil {
		return nil, 0, fmt.Errorf("count the disabled credentials: %w", err)
	}
	credentials = make([]CredentialRef, len(rows))
	for i, r := range rows {
		credentials[i] = CredentialRef{Supplier: r.SupplierID, ID: r.ID}
	}
	return credentials, int(n), nil
}

// CredentialRef names one credential of one supplier.
type CredentialRef struct{ Supplier, ID uuid.UUID }

// SetCredentialEnabled is a person's switch of one credential: off makes it disabled, so routing stops picking it while
// calls in flight end with it; on makes a disabled one active again. A rejected credential stays rejected when switched
// on, until a check passes; an erased one, or one still waiting for its key, is ErrStatusLocked. One already as asked
// changes nothing and records nothing.
func (a *Admin) SetCredentialEnabled(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, credential uuid.UUID, enabled bool, reason string) (Credential, error) {
	row, err := a.lockedCredential(ctx, tx, supplier, credential)
	if err != nil {
		return Credential{}, err
	}
	from, to := row.Status, row.Status
	switch {
	case from == "erased" || from == "binding":
		return Credential{}, fmt.Errorf("%w: the credential is %s", ErrStatusLocked, from)
	case !enabled:
		to = "disabled"
	case from == "disabled":
		to = "active"
	}
	if to == from {
		return credentialOf(credentialFacts(row)), nil
	}
	if err := store.New().SetCredentialStatusOf(ctx, tx, store.SetCredentialStatusOfParams{SupplierID: supplier, ID: credential, Status: to}); err != nil {
		return Credential{}, fmt.Errorf("switch the credential: %w", err)
	}
	action := "credential.disable"
	if enabled {
		action = "credential.enable"
	}
	if err := a.record(ctx, tx, actor, action, supplier, reason, map[string]any{"credential": credential.String(), "from": from, "to": to}); err != nil {
		return Credential{}, err
	}
	if err := persist.ObserveInbox(ctx, tx, supplier, a.now(), persist.InboxKeys, false); err != nil {
		return Credential{}, err
	}
	row.Status = to
	return credentialOf(credentialFacts(row)), nil
}

// RemoveCredential erases one credential at once and for good: its sealed key is cleared and the row stays, because
// calls made with it point at it. Axiom does not revoke the key at the supplier; the audit entry names its ref there. A
// credential erased already is returned as it is.
func (a *Admin) RemoveCredential(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier, credential uuid.UUID, reason string) (Credential, error) {
	row, err := a.lockedCredential(ctx, tx, supplier, credential)
	if err != nil {
		return Credential{}, err
	}
	if row.Status == "erased" {
		return credentialOf(credentialFacts(row)), nil
	}
	if err := a.erase(ctx, tx, actor, supplier, []store.LockCredentialOfRow{row}, "credential.remove", reason); err != nil {
		return Credential{}, err
	}
	row.Status = "erased"
	return credentialOf(credentialFacts(row)), nil
}

// erase clears the keys of the credentials of one supplier and records it once, naming the refs to revoke there.
func (a *Admin) erase(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, rows []store.LockCredentialOfRow, action, reason string) error {
	q := store.New()
	erased := make([]map[string]string, len(rows))
	for i, r := range rows {
		if err := q.EraseCredentialOf(ctx, tx, store.EraseCredentialOfParams{SupplierID: supplier, ID: r.ID}); err != nil {
			return fmt.Errorf("erase the credential: %w", err)
		}
		erased[i] = map[string]string{"credential": r.ID.String(), "upstream_key_ref": r.UpstreamKeyRef, "from": r.Status}
	}
	if err := a.record(ctx, tx, actor, action, supplier, reason, map[string]any{"credentials": erased}); err != nil {
		return err
	}
	return persist.ObserveInbox(ctx, tx, supplier, a.now(), persist.InboxKeys, false)
}

// RemoveInvalid erases each named credential of a platform supplier that is still rejected when the change runs, in one
// transaction, and keeps the rest: one a check passed since the page was read, one that is not rejected, and an ID that
// names none. removed and kept are in ID order, each ID once.
func (a *Admin) RemoveInvalid(ctx context.Context, tx pg.Tx, actor audit.Actor, ids []uuid.UUID, reason string) (removed, kept []uuid.UUID, err error) {
	ids = slices.Clone(ids)
	slices.SortFunc(ids, uuid.UUID.Compare)
	ids = slices.Compact(ids)
	q := store.New()
	found, err := q.RejectedCredentials(ctx, tx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("read the refused credentials: %w", err)
	}
	bySupplier := map[uuid.UUID][]uuid.UUID{}
	for _, f := range found {
		bySupplier[f.SupplierID] = append(bySupplier[f.SupplierID], f.ID)
	}
	for _, supplier := range slices.SortedFunc(maps.Keys(bySupplier), uuid.UUID.Compare) {
		erased, err := a.eraseRejected(ctx, tx, actor, supplier, bySupplier[supplier], reason)
		if err != nil {
			return nil, nil, err
		}
		removed = append(removed, erased...)
	}
	slices.SortFunc(removed, uuid.UUID.Compare)
	for _, id := range ids {
		if _, gone := slices.BinarySearchFunc(removed, id, uuid.UUID.Compare); !gone {
			kept = append(kept, id)
		}
	}
	return removed, kept, nil
}

// eraseRejected erases the credentials of one supplier that are still rejected under its lock. The supplier's row is taken
// before its credentials', as every change of a supplier takes them, and the credentials are read again under it, so
// one a check moved on meanwhile is kept. A supplier being removed is left alone: the removal erases every key.
func (a *Admin) eraseRejected(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, ids []uuid.UUID, reason string) ([]uuid.UUID, error) {
	if _, err := a.locked(ctx, tx, supplier); errors.Is(err, ErrStatusLocked) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var rows []store.LockCredentialOfRow
	var erased []uuid.UUID
	for _, id := range ids {
		locked, err := store.New().LockCredentialOf(ctx, tx, store.LockCredentialOfParams{SupplierID: supplier, ID: id})
		if err != nil {
			return nil, fmt.Errorf("lock the credential: %w", err)
		}
		if len(locked) == 1 && locked[0].Status == "rejected" {
			rows, erased = append(rows, locked[0]), append(erased, id)
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return erased, a.erase(ctx, tx, actor, supplier, rows, "credential.remove_invalid", reason)
}

// lockedCredential is a credential of the supplier, locked for a change after the supplier's row.
func (a *Admin) lockedCredential(ctx context.Context, tx pg.Tx, supplier, credential uuid.UUID) (store.LockCredentialOfRow, error) {
	return lockedChild(ctx, a, tx, supplier, ErrNoCredential, "the credential", func() ([]store.LockCredentialOfRow, error) {
		return store.New().LockCredentialOf(ctx, tx, store.LockCredentialOfParams{SupplierID: supplier, ID: credential})
	})
}
