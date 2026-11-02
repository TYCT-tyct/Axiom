package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"

	openapi_types "github.com/oapi-codegen/runtime/types"
)

// The console's own provider keys (docs/domains/accounts.md「自带的供应商 key（BYOK）」). The handlers are thin: the
// sealing, storage and status rules are suppliers'. The plaintext key is read once for a test, put in one
// Authorization header and never logged, written to the response or kept.

// byokProbeURL is where a provider key is tested. Only providers the suppliers module can call are accepted, so this
// is the matching address.
const byokProbeURL = "https://api.openai.com/v1/models"

// ListProviderKeys reads the account's keys.
func (c consoleAPI) ListProviderKeys(ctx context.Context, _ consoleapi.ListProviderKeysRequestObject) (consoleapi.ListProviderKeysResponseObject, error) {
	cl := callOf(ctx)
	var keys []suppliers.ByokKey
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		keys, err = c.suppliers.ListByok(ctx, tx, uuid.UUID(cl.site.ID), uuid.UUID(cl.principal.AccountID()))
		return err
	}); err != nil {
		return nil, err
	}
	list := consoleapi.ProviderKeyList{Data: make([]consoleapi.ProviderKey, len(keys))}
	for i, k := range keys {
		list.Data[i] = providerKeyBody(k)
	}
	return consoleapi.ListProviderKeys200JSONResponse(list), nil
}

// CreateProviderKey saves a key. Its text is an input only: the response never repeats it.
func (c consoleAPI) CreateProviderKey(ctx context.Context, request consoleapi.CreateProviderKeyRequestObject) (consoleapi.CreateProviderKeyResponseObject, error) {
	cl := callOf(ctx)
	snap, err := c.catalog.Current()
	if err != nil {
		return nil, &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "the catalog is not ready"}
	}
	models := byokModels(snap, string(request.Body.Provider))
	if len(models) == 0 {
		return nil, byokProblem(suppliers.ErrByokModels)
	}
	fallback := true
	if request.Body.Fallback != nil {
		fallback = *request.Body.Fallback
	}
	var key suppliers.ByokKey
	err = c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		key, err = c.suppliers.CreateByok(ctx, tx, suppliers.ByokRequest{
			Site: uuid.UUID(cl.site.ID), Account: uuid.UUID(cl.principal.AccountID()), Name: request.Body.Name,
			Provider: string(request.Body.Provider), Key: request.Body.Key, Fallback: fallback,
			Actor: audit.User(uuid.UUID(cl.principal.UserID())),
		}, models)
		return err
	})
	if err != nil {
		return nil, byokProblem(err)
	}
	return consoleapi.CreateProviderKey201JSONResponse(providerKeyBody(key)), nil
}

// DeleteProviderKey takes a key out of routing and erases its secret.
func (c consoleAPI) DeleteProviderKey(ctx context.Context, request consoleapi.DeleteProviderKeyRequestObject) (consoleapi.DeleteProviderKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := providerKeyID(request.ProviderKeyId)
	if err != nil {
		return nil, err
	}
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) error {
		return c.suppliers.RemoveByok(ctx, tx, uuid.UUID(cl.site.ID), uuid.UUID(cl.principal.AccountID()), id, audit.User(uuid.UUID(cl.principal.UserID())))
	}); err != nil {
		return nil, byokProblem(err)
	}
	return consoleapi.DeleteProviderKey204Response{}, nil
}

// SetProviderKeyEnabled switches a key on or off.
func (c consoleAPI) SetProviderKeyEnabled(ctx context.Context, request consoleapi.SetProviderKeyEnabledRequestObject) (consoleapi.SetProviderKeyEnabledResponseObject, error) {
	cl := callOf(ctx)
	id, err := providerKeyID(request.ProviderKeyId)
	if err != nil {
		return nil, err
	}
	var key suppliers.ByokKey
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		key, err = c.suppliers.SetByokEnabled(ctx, tx, uuid.UUID(cl.site.ID), uuid.UUID(cl.principal.AccountID()), id, request.Body.Enabled, audit.User(uuid.UUID(cl.principal.UserID())))
		return err
	}); err != nil {
		return nil, byokProblem(err)
	}
	return consoleapi.SetProviderKeyEnabled200JSONResponse(providerKeyBody(key)), nil
}

// TestProviderKey asks the provider whether the key works. A provider that cannot be asked leaves the status alone:
// an outage must not mark a working key rejected.
func (c consoleAPI) TestProviderKey(ctx context.Context, request consoleapi.TestProviderKeyRequestObject) (consoleapi.TestProviderKeyResponseObject, error) {
	cl := callOf(ctx)
	id, err := providerKeyID(request.ProviderKeyId)
	if err != nil {
		return nil, err
	}
	var key suppliers.ByokKey
	var credential uuid.UUID
	var plain secret.Secret
	if err := c.db.ReadTx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		key, credential, plain, err = c.suppliers.RevealByok(ctx, tx, uuid.UUID(cl.site.ID), uuid.UUID(cl.principal.AccountID()), id)
		return err
	}); err != nil {
		return nil, byokProblem(err)
	}
	ok, inconclusive := c.probeProvider(ctx, plain)
	if inconclusive {
		yes := true
		return consoleapi.TestProviderKey200JSONResponse{
			Ok: false, Status: consoleapi.ProviderKeyTestStatus(key.Status), Inconclusive: &yes,
		}, nil
	}
	if err := c.db.Tx(ctx, cl.scope, func(tx pg.Tx) (err error) {
		key, err = c.suppliers.SetByokTestResult(ctx, tx, uuid.UUID(cl.site.ID), uuid.UUID(cl.principal.AccountID()), id, credential, ok, audit.User(uuid.UUID(cl.principal.UserID())))
		return err
	}); err != nil {
		return nil, byokProblem(err)
	}
	return consoleapi.TestProviderKey200JSONResponse{Ok: ok, Status: consoleapi.ProviderKeyTestStatus(key.Status)}, nil
}

// probeProvider sends one small request with the key. ok is whether the provider accepted it; inconclusive is a
// provider that did not answer, or answered something that says nothing about the key. The response body is drained
// and dropped: it can hold account data and is never read.
func (c consoleAPI) probeProvider(ctx context.Context, key secret.Secret) (ok, inconclusive bool) {
	if c.exit == nil {
		return false, true
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, byokProbeURL, nil)
	if err != nil {
		return false, true
	}
	req.Header.Set("Authorization", "Bearer "+key.Reveal())
	resp, err := c.exit.Do(req)
	if err != nil {
		return false, true
	}
	defer resp.Body.Close()                                      //nolint:errcheck // the body is drained and dropped; a close error says nothing about the key
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) //nolint:errcheck // the body is drained and dropped
	switch resp.StatusCode {
	case http.StatusOK:
		return true, false
	case http.StatusUnauthorized:
		return false, false // the key is invalid; 403 may be a region block and says nothing about the key
	}
	return false, true
}

// providerKeyBody is the API's view: never the key, never its ciphertext, never the credential id.
func providerKeyBody(k suppliers.ByokKey) consoleapi.ProviderKey {
	out := consoleapi.ProviderKey{
		Id: openapi_types.UUID(k.ID), Name: k.Name, Provider: k.Provider, Status: consoleapi.ProviderKeyStatus(k.Status),
		Enabled: k.Enabled, Rejected: k.Rejected, Fallback: k.Fallback, CreatedAt: k.CreatedAt,
	}
	if k.Hint != "" {
		out.Hint = &k.Hint
	}
	return out
}

// providerKeyID reads the path's UUID. A malformed one is the same 404 a key of another account gets: neither says
// whether the id exists.
func providerKeyID(id openapi_types.UUID) (uuid.UUID, error) {
	parsed, err := uuid.Parse(id.String())
	if err != nil {
		return uuid.UUID{}, &apiError{status: http.StatusNotFound, code: codeNotFound, message: "no such resource"}
	}
	return parsed, nil
}

// byokProblem maps the suppliers module's refusals onto the contract.
func byokProblem(err error) error {
	var fields []errorField
	switch {
	case errors.Is(err, suppliers.ErrByokProvider):
		fields = []errorField{{Pointer: "/provider", Message: "this node has no adapter for that provider"}}
	case errors.Is(err, suppliers.ErrByokName):
		fields = []errorField{{Pointer: "/name", Message: "1 to 200 characters"}}
	case errors.Is(err, suppliers.ErrByokKey):
		fields = []errorField{{Pointer: "/key", Message: "not a key the provider would accept"}}
	case errors.Is(err, suppliers.ErrByokModels):
		fields = []errorField{{Pointer: "/provider", Message: "no listed catalog model can be served with this key"}}
	case errors.Is(err, suppliers.ErrByokNotFound):
		return &apiError{status: http.StatusNotFound, code: codeNotFound, message: "no such resource"}
	case errors.Is(err, suppliers.ErrByokDisabled):
		return &apiError{status: http.StatusServiceUnavailable, code: codeUnavailable, message: "provider keys are not enabled on this deployment"}
	default:
		return err
	}
	return &apiError{status: http.StatusUnprocessableEntity, code: codeUnprocessable, message: "the provider key cannot be saved", fields: fields}
}

// byokModels reads what the catalog lists for a provider: only models whose own billable items say they generate (chat
// and responses), generate without streaming or an output limit (legacy completions), or embed. A model with no listed
// price has no schedule to scale, so the key does not serve it either.
func byokModels(snap *catalog.Snapshot, provider string) []suppliers.ByokModel {
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		return nil
	}
	var out []suppliers.ByokModel
	for m := range snap.Models() {
		if m.Author != provider || m.Free() {
			continue
		}
		formats := byokFormats(m)
		if len(formats) == 0 {
			continue
		}
		schedule, ok := byokSchedule(snap, m.ID, usd)
		if !ok {
			continue
		}
		upstream, ok := snap.OfficialName(m.ID)
		if !ok {
			continue // no vendor name means the slug would be a guess and the provider would refuse it
		}
		out = append(out, suppliers.ByokModel{
			ID: uuid.UUID(m.ID), Upstream: upstream, Formats: formats, Schedule: schedule,
		})
	}
	return out
}

// byokSchedule is the zero reference schedule a key's endpoint carries, or false when the catalog has no reference
// price for the model or its schedule cannot be rendered.
func byokSchedule(snap *catalog.Snapshot, id catalog.ModelID, usd platform.Currency) ([]byte, bool) {
	ref, ok := snap.ReferencePrice(id, "USD")
	if !ok {
		return nil, false
	}
	schedule, err := ref.Schedule.Scaled("0", usd)
	if err != nil {
		return nil, false
	}
	encoded, err := schedule.MarshalJSON()
	return encoded, err == nil
}

// byokFormats is the call formats an OpenAI-compatible endpoint serves a model in, from the model's own billable
// items: output means it generates (chat and responses, or completions when it has no output limit or stream), input
// alone means embeddings. Nothing is guessed from the name.
func byokFormats(m catalog.Model) []protocol.Format {
	items := m.Reportable().Items
	if items.Has(catalog.ItemOutput) {
		if _, ok := m.Serving(); ok {
			return []protocol.Format{protocol.Chat, protocol.Responses}
		}
		if _, ok := m.ServingDirect(); ok {
			return []protocol.Format{protocol.Completions}
		}
		return nil
	}
	if items.Has(catalog.ItemInput) {
		if _, ok := m.ServingDirect(); ok {
			return []protocol.Format{protocol.Embeddings}
		}
	}
	return nil
}
