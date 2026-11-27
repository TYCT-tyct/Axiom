package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers"
)

// The keys of a relay's groups (docs/domains/suppliers.md「Key 管理」). A key a person pastes is a secret: it is sealed on
// arrival, never returned beyond its last four characters, and never in an idempotency fingerprint or an audit entry.

// supplierRequestLimit bounds a change that asks the supplier first: the requests to it, and the write after, outlive a
// client that went away, so a key made at the supplier is stored.
const supplierRequestLimit = time.Minute

// probed runs a change that needs answers from a supplier first, under its Idempotency-Key: a replay answers from the
// claim without asking the supplier again, the requests run outside the transaction so no connection waits on a third
// party, and the change commits with the claim. A first request that committed while this one was asking answers too.
func probed[T any](ctx context.Context, a adminAPI, claim idem.Claim, probe func(context.Context) error, write func(pg.Tx) (T, error)) (T, bool, error) {
	if body, found, err := replayed[T](ctx, a.db, claim); err != nil || found {
		return body, found, err
	}
	if err := probe(ctx); err != nil {
		if body, found, replayErr := replayed[T](ctx, a.db, claim); replayErr == nil && found {
			return body, true, nil
		}
		var zero T
		return zero, false, err
	}
	return idempotentlyClaimed(ctx, a.db, claim, write)
}

// reach is where the suppliers module finds the login and the shared rate buckets of a request to a supplier.
func (a adminAPI) reach(ctx context.Context) suppliers.Reach {
	return suppliers.Reach{DB: a.db, Scope: callOf(ctx).scope}
}

// leaveRouting rebuilds this node's endpoints and routing after a change that takes a credential out of routing or puts
// it back, so the next call this node admits sees it; every other node does at its next reload, within five seconds. A
// failure is not the change's: the reload loop brings it.
func (a adminAPI) leaveRouting(ctx context.Context, skip bool) {
	if skip || a.calls == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshLimit)
	defer cancel()
	if err := a.calls.Reload(ctx); err != nil {
		a.logger.WarnContext(ctx, "credential change saved but this node did not reload its endpoints", "err", err)
		return
	}
	if a.routes == nil {
		return
	}
	if err := a.routes.Rebuild(ctx); err != nil {
		a.logger.WarnContext(ctx, "credential change saved but this node did not rebuild its routing", "err", err)
	}
}

// keyProblem is a refusal of a key request as the contract answers it, pointing at the member the key came in.
func keyProblem(err error, pointer string) error {
	refused, isRefusal := errors.AsType[*suppliers.KeyRefusal](err)
	switch {
	case isRefusal:
		return unprocessable("the supplier would not make the key: " + refused.Reason)
	case errors.Is(err, suppliers.ErrKeyNotListed):
		return unprocessableFields(pointer, "the supplier does not list this key")
	case errors.Is(err, suppliers.ErrWrongGroup):
		return unprocessableFields(pointer, "the supplier lists this key under another group")
	case errors.Is(err, suppliers.ErrKeyUnusable):
		return unprocessableFields(pointer, "the supplier lists this key as disabled, expired or out of quota")
	}
	return err
}

// pastedKey reads a key a person pasted: a bearer token, with no space or control character in it.
func pastedKey(pointer, text string) (secret.Secret, error) {
	for _, c := range text {
		if c <= ' ' || c == unicode.ReplacementChar || unicode.IsSpace(c) {
			return secret.Secret{}, invalidField(pointer, "a key has no spaces or control characters")
		}
	}
	return secret.New(text), nil
}

// last4 is the last four characters of a key, which are all of it the admin ever shows.
func last4(text string) string {
	r := []rune(text)
	if len(r) <= 4 {
		return text
	}
	return string(r[len(r)-4:])
}

// ConnectSupplierGroup selects a group that has no key yet and gives it one: made at the supplier now, or pasted.
func (a adminAPI) ConnectSupplierGroup(ctx context.Context, request adminapi.ConnectSupplierGroupRequestObject) (adminapi.ConnectSupplierGroupResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	group, err := supplierIDOf(request.GroupId)
	if err != nil {
		return nil, suppliers.ErrNoGroup
	}
	b := request.Body
	text, tail, err := connectionKey(b)
	if err != nil {
		return nil, err
	}
	// The key is not in the fingerprint, only its last four characters: the fingerprint must not let it be guessed.
	claim, err := idempotencyClaim(ctx, "connectSupplierGroup", request.Params.IdempotencyKey, []any{request.SupplierId, request.GroupId, string(b.KeySource), tail, b.Reason})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	hold, err := a.suppliers.ClaimGroup(ctx, a.reach(ctx), group)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := hold.Release(context.WithoutCancel(ctx)); err != nil {
			a.logger.WarnContext(ctx, "a group's connection claim was not released; its connection frees it", "err", err)
		}
	}()
	var key suppliers.GroupKey
	body, replayed, err := probed(ctx, a, claim, func(ctx context.Context) (err error) {
		key, err = a.groupKey(ctx, supplier, group, b.KeySource, text)
		return keyProblem(err, "/key")
	}, func(tx pg.Tx) (adminapi.GroupConnected, error) {
		g, c, err := a.suppliers.ConnectGroup(ctx, tx, actor(ctx), supplier, group, key, b.Reason)
		if err != nil {
			return adminapi.GroupConnected{}, keyProblem(err, "/key")
		}
		return adminapi.GroupConnected{Group: groupBody(g), Credential: a.credentialBody(c)}, nil
	})
	if err != nil {
		return nil, err
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.ConnectSupplierGroup201JSONResponse{Body: body, Headers: adminapi.ConnectSupplierGroup201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}}, nil
}

// connectionKey reads the key a connection gives, which only paste has, and its last four characters.
func connectionKey(b *adminapi.GroupConnection) (secret.Secret, string, error) {
	switch {
	case b.KeySource == adminapi.Make && b.Key != nil:
		return secret.Secret{}, "", invalidField("/key", "refused with make")
	case b.KeySource == adminapi.Paste && b.Key == nil:
		return secret.Secret{}, "", invalidField("/key", "required with paste")
	case b.Key == nil:
		return secret.Secret{}, "", nil
	}
	text, err := pastedKey("/key", *b.Key)
	return text, last4(*b.Key), err
}

// groupKey asks the supplier for the key a group is connected with: one it makes now, or which of its keys the pasted
// text is, which must be listed, usable, and is stored with what the supplier lets it call.
func (a adminAPI) groupKey(ctx context.Context, supplier, group uuid.UUID, source adminapi.GroupConnectionKeySource, text secret.Secret) (suppliers.GroupKey, error) {
	if source == adminapi.Make {
		made, err := a.suppliers.MakeKey(ctx, a.reach(ctx), supplier, group)
		return suppliers.GroupKey{Source: "made", Ref: made.Ref, GroupRef: made.GroupRef, Text: made.Text}, err
	}
	found, listed, err := a.suppliers.FindKey(ctx, a.reach(ctx), supplier, text)
	switch {
	case err != nil:
	case !listed:
		err = suppliers.ErrKeyNotListed
	case found.Status != string(adminapi.KeyIdentificationStatusActive):
		err = suppliers.ErrKeyUnusable
	}
	return suppliers.GroupKey{Source: "pasted", Ref: found.Ref, GroupRef: found.GroupRef, Text: text, Policy: found.Policy}, err
}

// IdentifySupplierKey asks the supplier which of its keys a pasted text is, and stores nothing: 10 a minute per session,
// because each is a request to a third party.
func (a adminAPI) IdentifySupplierKey(ctx context.Context, request adminapi.IdentifySupplierKeyRequestObject) (adminapi.IdentifySupplierKeyResponseObject, error) {
	if err := a.identifications.allow(uuid.UUID(callOf(ctx).principal.SessionID())); err != nil {
		return nil, err
	}
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	if request.Body.Key == nil {
		return nil, invalidField("/key", "required")
	}
	text, err := pastedKey("/key", *request.Body.Key)
	if err != nil {
		return nil, err
	}
	body := adminapi.KeyIdentification{Last4: last4(*request.Body.Key)}
	found, listed, err := a.suppliers.FindKey(ctx, a.reach(ctx), supplier, text)
	if err != nil || !listed {
		return adminapi.IdentifySupplierKey200JSONResponse(body), err
	}
	status := adminapi.KeyIdentificationStatus(found.Status)
	body.UpstreamKeyRef, body.Status = &found.Ref, &status
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error { return a.identified(ctx, tx, supplier, found, &body) }); err != nil {
		return nil, err
	}
	return adminapi.IdentifySupplierKey200JSONResponse(body), nil
}

// identified fills in the group a key the supplier lists is under, and the credential Axiom stores it as, if any.
func (a adminAPI) identified(ctx context.Context, tx pg.Tx, supplier uuid.UUID, found suppliers.FoundKey, body *adminapi.KeyIdentification) error {
	group, err := a.groupByRef(ctx, tx, supplier, found.GroupRef)
	if err != nil {
		return err
	}
	if group != (uuid.UUID{}) {
		body.GroupId = new(group.String())
	}
	stored, ok, err := a.suppliers.CredentialByRef(ctx, tx, supplier, found.Ref)
	if ok {
		body.CredentialId = new(stored.String())
	}
	return err
}

// groupByRef is the supplier's group with the supplier's ref, the zero ID when it has none.
func (a adminAPI) groupByRef(ctx context.Context, tx pg.Tx, supplier uuid.UUID, ref string) (uuid.UUID, error) {
	d, err := a.suppliers.Supplier(ctx, tx, supplier, suppliers.PlatformOwned)
	for _, g := range d.Groups {
		if g.UpstreamRef == ref {
			return g.ID, err
		}
	}
	return uuid.UUID{}, err
}

// AddSupplierKeys checks pasted keys and stores the ones that pass: a relay's for one of its groups, a direct API's for
// its one line. A cloud supplier's credential is replaced whole (409).
func (a adminAPI) AddSupplierKeys(ctx context.Context, request adminapi.AddSupplierKeysRequestObject) (adminapi.AddSupplierKeysResponseObject, error) {
	supplier, err := supplierIDOf(request.SupplierId)
	if err != nil {
		return nil, err
	}
	var mode string
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
		mode, err = a.suppliers.Mode(ctx, tx, supplier)
		return err
	}); err != nil {
		return nil, err
	}
	if mode != "relay" {
		return a.addDirectKeys(ctx, request, supplier)
	}
	b := request.Body
	group, texts, tails, err := pastedKeysOf(b)
	if err != nil {
		return nil, err
	}
	limits := suppliers.Limits{MaxConcurrency: b.MaxConcurrency, RequestsPerMinute: b.RequestsPerMinute}
	claim, err := idempotencyClaim(ctx, "addSupplierKeys", request.Params.IdempotencyKey,
		[]any{request.SupplierId, *b.GroupId, tails, b.MaxConcurrency, b.RequestsPerMinute, b.Reason})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	var checks []suppliers.KeyCheck
	var passed []suppliers.PastedKey
	body, replayed, err := probed(ctx, a, claim, func(ctx context.Context) (err error) {
		checks, passed, err = a.checkPasted(ctx, supplier, group, *b.Keys, texts)
		return err
	}, func(tx pg.Tx) (adminapi.CredentialCheckList, error) {
		stored, err := a.suppliers.AddKeys(ctx, tx, actor(ctx), supplier, group, passed, limits, b.Reason)
		if err != nil {
			return adminapi.CredentialCheckList{}, err
		}
		for _, s := range stored {
			checks[s.Index] = s
		}
		return checkListBody(checks), nil
	})
	if err != nil {
		return nil, err
	}
	return a.keysAdded(ctx, body, replayed), nil
}

// addDirectKeys is AddSupplierKeys for a direct API: its keys take no group, are checked at the provider's model list
// as a new direct API's are, and each that passes is a credential of the line with the models it lists.
func (a adminAPI) addDirectKeys(ctx context.Context, request adminapi.AddSupplierKeysRequestObject, supplier uuid.UUID) (adminapi.AddSupplierKeysResponseObject, error) {
	b := request.Body
	switch {
	case b.GroupId != nil:
		return nil, invalidField("/group_id", "refused for a direct API")
	case b.Keys == nil || len(*b.Keys) == 0:
		return nil, invalidField("/keys", "at least one key is required")
	}
	tails := make([]string, len(*b.Keys))
	for i, k := range *b.Keys {
		tails[i] = suppliers.Last4(k)
	}
	claim, err := idempotencyClaim(ctx, "addSupplierKeys", request.Params.IdempotencyKey,
		[]any{request.SupplierId, "", tails, b.MaxConcurrency, b.RequestsPerMinute, b.Reason})
	if err != nil {
		return nil, err
	}
	limits := suppliers.Limits{MaxConcurrency: b.MaxConcurrency, RequestsPerMinute: b.RequestsPerMinute}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), supplierRequestLimit)
	defer cancel()
	var plan *suppliers.DirectPlan
	var snap *catalog.Snapshot
	body, replayed, err := probed(ctx, a, claim, func(ctx context.Context) (err error) {
		if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
			plan, err = a.suppliers.PlanDirectKeys(ctx, tx, supplier, *b.Keys, limits, b.Reason)
			return err
		}); err != nil {
			return directProblem(err)
		}
		if snap, err = a.catalog.Current(); err != nil {
			return &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
		}
		return directProblem(a.suppliers.CheckDirect(ctx, plan))
	}, func(tx pg.Tx) (adminapi.CredentialCheckList, error) {
		checks, err := a.suppliers.AddDirectKeys(ctx, tx, actor(ctx), snap, supplier, plan)
		return checkListBody(checks), err
	})
	if err != nil {
		return nil, err
	}
	return a.keysAdded(ctx, body, replayed), nil
}

// checkListBody is the API's view of the checks of pasted keys, in the order sent.
func checkListBody(checks []suppliers.KeyCheck) adminapi.CredentialCheckList {
	list := adminapi.CredentialCheckList{Data: make([]adminapi.CredentialCheck, len(checks))}
	for i, c := range checks {
		list.Data[i] = keyCheckBody(c, true)
	}
	return list
}

// keysAdded answers 201 when a key was stored, after this node has put it in its routing, and 200 when none was.
func (a adminAPI) keysAdded(ctx context.Context, body adminapi.CredentialCheckList, replayed bool) adminapi.AddSupplierKeysResponseObject {
	headers := adminapi.AddSupplierKeys201ResponseHeaders{IdempotencyReplayed: replayedHeader(replayed)}
	if !slices.ContainsFunc(body.Data, func(c adminapi.CredentialCheck) bool { return c.CredentialId != nil }) {
		return adminapi.AddSupplierKeys200JSONResponse{Body: body, Headers: adminapi.AddSupplierKeys200ResponseHeaders(headers)}
	}
	a.leaveRouting(ctx, replayed)
	return adminapi.AddSupplierKeys201JSONResponse{Body: body, Headers: headers}
}

// pastedKeysOf reads the group and the keys a request pastes, with each key's last four characters.
func pastedKeysOf(b *adminapi.SupplierKeysAdd) (uuid.UUID, []secret.Secret, []string, error) {
	if b.Keys == nil || len(*b.Keys) == 0 {
		return uuid.UUID{}, nil, nil, invalidField("/keys", "at least one key is required")
	}
	if b.GroupId == nil {
		return uuid.UUID{}, nil, nil, invalidField("/group_id", "required for a relay")
	}
	group, err := idIn("/group_id", *b.GroupId)
	if err != nil {
		return uuid.UUID{}, nil, nil, err
	}
	texts := make([]secret.Secret, len(*b.Keys))
	tails := make([]string, len(*b.Keys))
	for i, k := range *b.Keys {
		if texts[i], err = pastedKey("/keys/"+strconv.Itoa(i), k); err != nil {
			return uuid.UUID{}, nil, nil, err
		}
		tails[i] = last4(k)
	}
	return group, texts, tails, nil
}

// checkPasted checks each pasted key in order: a key that came earlier in the request, or that the supplier lists under
// a ref Axiom stores already, is a duplicate and is not checked; one the supplier lists under another group fails with
// wrong_group, and so does one it does not list at all that it accepts, which is a key of another account. The rest are
// sent one request each. passed are the keys to store, with their checks.
func (a adminAPI) checkPasted(ctx context.Context, supplier, group uuid.UUID, keys []string, texts []secret.Secret) ([]suppliers.KeyCheck, []suppliers.PastedKey, error) {
	var groupRef string
	if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) error {
		g, err := a.suppliers.GroupOf(ctx, tx, supplier, group)
		groupRef = g.UpstreamRef
		return bodyGroup(err)
	}); err != nil {
		return nil, nil, err
	}
	checks := make([]suppliers.KeyCheck, len(keys))
	var passed []suppliers.PastedKey
	seen := make(map[string]bool, len(keys))
	for i, k := range keys {
		check, found, err := a.checkOnePasted(ctx, supplier, groupRef, texts[i], seen[k])
		if err != nil {
			return nil, nil, err
		}
		seen[k] = true
		check.Index, check.Last4 = i, last4(k)
		checks[i] = check
		if check.Outcome == suppliers.CheckPassed {
			passed = append(passed, suppliers.PastedKey{Ref: found.Ref, Text: texts[i], Policy: found.Policy, Check: check})
		}
	}
	return checks, passed, nil
}

// checkOnePasted is one key's check, and which of the supplier's keys it is. An error is the whole request's: the
// supplier cannot be asked at all.
func (a adminAPI) checkOnePasted(ctx context.Context, supplier uuid.UUID, groupRef string, text secret.Secret, repeated bool) (suppliers.KeyCheck, suppliers.FoundKey, error) {
	if repeated {
		return suppliers.KeyCheck{Outcome: suppliers.CheckDuplicate}, suppliers.FoundKey{}, nil
	}
	found, listed, err := a.suppliers.FindKey(ctx, a.reach(ctx), supplier, text)
	switch {
	case errors.Is(err, suppliers.ErrSupplierUnanswered):
		return suppliers.KeyCheck{Outcome: suppliers.CheckInconclusive, Cause: suppliers.CauseUpstreamError}, suppliers.FoundKey{}, nil
	case err != nil:
		return suppliers.KeyCheck{}, suppliers.FoundKey{}, err
	case listed && found.GroupRef != groupRef:
		return suppliers.KeyCheck{Outcome: suppliers.CheckFailed, Cause: suppliers.CauseWrongGroup}, suppliers.FoundKey{}, nil
	case listed:
		var stored bool
		if err := a.db.ReadTx(ctx, callOf(ctx).scope, func(tx pg.Tx) (err error) {
			_, stored, err = a.suppliers.CredentialByRef(ctx, tx, supplier, found.Ref)
			return err
		}); err != nil {
			return suppliers.KeyCheck{}, suppliers.FoundKey{}, err
		}
		if stored {
			return suppliers.KeyCheck{Outcome: suppliers.CheckDuplicate}, suppliers.FoundKey{}, nil
		}
	}
	check, err := a.suppliers.CheckKey(ctx, a.reach(ctx), supplier, text)
	if err != nil {
		return suppliers.KeyCheck{}, suppliers.FoundKey{}, err
	}
	if !listed && check.Outcome == suppliers.CheckPassed {
		check.Outcome, check.Cause = suppliers.CheckFailed, suppliers.CauseWrongGroup
	}
	return check, found, nil
}
