package main

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/platform/clientip"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// managementAPI is the public, Bearer-authenticated key administration surface, outside the console's cookie guard.
type managementAPI struct {
	accounts *accounts.Service
	proxies  []netip.Prefix
	logger   *slog.Logger
}

func withKeyManagement(next http.Handler, api managementAPI) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/v1/keys", api)
	mux.Handle("/api/v1/keys/", api)
	mux.Handle("/", next)
	return mux
}

func (a managementAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	for _, bound := range []func(time.Time) error{controller.SetReadDeadline, controller.SetWriteDeadline} {
		if err := bound(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			a.failure(w, r, err)
			return
		}
	}
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	caller, err := a.managementCaller(r)
	status := http.StatusOK
	var body []byte
	if err == nil {
		// Client I/O must finish before the transaction takes the manager's row lock or a database slot.
		input, inputErr := readManagementInput(r)
		ip := clientip.Addr(r, a.proxies)
		err = a.accounts.WithKeyManager(ctx, caller, accounts.AdmitRequest{Domain: r.Host, ClientIP: ip}, func(m accounts.KeyManager, tx pg.Tx) error {
			if inputErr != nil {
				return inputErr
			}
			out, code, err := dispatchManagement(r, m, tx, input)
			if err != nil {
				return err
			}
			status = code
			body, err = jsonx.MarshalCanonical(out)
			return err
		})
	}
	if err != nil {
		a.failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body) //nolint:errcheck,gosec // the transaction has committed; the client may have disconnected
}

func (a managementAPI) managementCaller(r *http.Request) (accounts.Caller, error) {
	site, surface, err := a.accounts.SiteForHost(r.Host)
	if errors.Is(err, accounts.ErrStale) {
		return accounts.Caller{}, err
	}
	if err != nil || surface != accounts.API {
		return accounts.Caller{}, accounts.ErrUnknownKey
	}
	scheme, token, bearer := strings.Cut(r.Header.Get("Authorization"), " ")
	if !bearer || !strings.EqualFold(scheme, "Bearer") {
		return accounts.Caller{}, accounts.ErrUnknownKey
	}
	return a.accounts.AuthenticateKey(r.Context(), site.ID, strings.TrimSpace(token))
}

func dispatchManagement(r *http.Request, m accounts.KeyManager, tx pg.Tx, input managedInput) (any, int, error) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/keys")
	if path == "" || path == "/" {
		return managementCollection(r, m, tx, input)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(path, "/"))
	if err != nil || len(raw) != 16 {
		return nil, 0, accounts.ErrKeyNotFound
	}
	id := accounts.KeyID(uuid.UUID(raw))
	if r.Method == http.MethodDelete {
		if err := m.Delete(r.Context(), id); err != nil {
			return nil, 0, err
		}
		return map[string]any{"deleted": true}, http.StatusOK, nil
	}
	key, err := m.Get(r.Context(), id)
	if err != nil {
		return nil, 0, err
	}
	key, err = patchManagedKey(r, m, key, input)
	if err != nil {
		return nil, 0, err
	}
	data, err := managedKeyBody(r.Context(), tx, key)
	return map[string]any{"data": data}, http.StatusOK, err
}

func patchManagedKey(r *http.Request, m accounts.KeyManager, key accounts.Key, input managedInput) (accounts.Key, error) {
	if r.Method == http.MethodGet {
		return key, nil
	}
	if r.Method != http.MethodPatch {
		return accounts.Key{}, errManagementMethod
	}
	settings, _, err := input.settings(key.KeySettings, key.ExpiresAt, false)
	if err != nil {
		return accounts.Key{}, err
	}
	return m.Update(r.Context(), key.ID, settings)
}

func managementCollection(r *http.Request, m accounts.KeyManager, tx pg.Tx, input managedInput) (any, int, error) {
	switch r.Method {
	case http.MethodGet:
		page, err := managedPage(r.URL.Query())
		if err != nil {
			return nil, 0, err
		}
		keys, err := m.List(r.Context(), page)
		if err != nil {
			return nil, 0, err
		}
		data := make([]any, len(keys))
		for i, key := range keys {
			data[i], err = managedKeyBody(r.Context(), tx, key)
			if err != nil {
				return nil, 0, err
			}
		}
		return map[string]any{"data": data}, http.StatusOK, nil
	case http.MethodPost:
		return createManagedKey(r, m, tx, input)
	default:
		return nil, 0, errManagementMethod
	}
}

func createManagedKey(r *http.Request, m accounts.KeyManager, tx pg.Tx, input managedInput) (any, int, error) {
	ceiling := m.Ceiling()
	settings, expires, err := input.settings(ceiling.KeySettings, ceiling.ExpiresAt, true)
	if err != nil {
		return nil, 0, err
	}
	if input.Creator != nil && *input.Creator != uuid.UUID(ceiling.CreatedBy).String() {
		return nil, 0, accounts.ErrForbidden
	}
	var workspace accounts.WorkspaceID
	if input.Workspace != nil {
		id, err := uuid.Parse(*input.Workspace)
		if err != nil {
			return nil, 0, errManagementInput
		}
		workspace = accounts.WorkspaceID(id)
	}
	created, err := m.Create(r.Context(), workspace, expires, settings)
	if err != nil {
		return nil, 0, err
	}
	key, err := m.Get(r.Context(), created.ID)
	if err != nil {
		return nil, 0, err
	}
	data, err := managedKeyBody(r.Context(), tx, key)
	return map[string]any{"data": data, "key": created.Text.Reveal()}, http.StatusCreated, err
}

func managedKeyBody(ctx context.Context, tx pg.Tx, k accounts.Key) (map[string]any, error) {
	use, err := billing.ReadKeyUsage(ctx, tx, uuid.UUID(k.ID), billing.Reset(k.LimitReset))
	if err != nil {
		return nil, err
	}
	id := uuid.UUID(k.ID)
	data := map[string]any{
		"hash": hex.EncodeToString(id[:]), "name": k.Name, "label": k.Prefix + "..." + k.Last4,
		"created_at": k.CreatedAt, "updated_at": k.UpdatedAt, "expires_at": k.ExpiresAt,
		"creator_user_id": uuid.UUID(k.CreatedBy).String(), "workspace_id": uuid.UUID(k.Workspace).String(),
		"disabled": k.Disabled, "limit": nil, "limit_reset": nil, "limit_remaining": nil,
		"usage": float64(use.Total) / 1e9, "usage_daily": float64(use.Day) / 1e9,
		"usage_weekly": float64(use.Week) / 1e9, "usage_monthly": float64(use.Month) / 1e9,
		"include_byok_in_limit": true, "byok_usage": 0, "byok_usage_daily": 0, "byok_usage_weekly": 0, "byok_usage_monthly": 0,
	}
	if k.SpendLimit != nil {
		data["limit"], data["limit_remaining"] = float64(*k.SpendLimit)/1e9, float64(max(0, *k.SpendLimit-use.Counted))/1e9
		if k.LimitReset != "" {
			data["limit_reset"] = k.LimitReset
		}
	}
	return data, nil
}

var (
	errManagementInput  = errors.New("invalid key management request")
	errManagementMethod = errors.New("unsupported key management method")
)

func (a managementAPI) failure(w http.ResponseWriter, r *http.Request, err error) {
	status := managementStatus(err)
	if rate, ok := errors.AsType[*accounts.ManagementRateLimit](err); ok {
		status = http.StatusTooManyRequests
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(rate.RetryAfter)))
	}
	if keys, ok := errors.AsType[*accounts.TooManyKeys](err); ok {
		status = http.StatusConflict
		if keys.RetryAfter > 0 {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(keys.RetryAfter)))
		}
	}
	if status == http.StatusUnauthorized {
		w.Header().Set("Connection", "close")
	}
	if status >= 500 && a.logger != nil {
		a.logger.ErrorContext(r.Context(), "key management failed", "error", err)
	}
	body, encodeErr := jsonx.MarshalCanonical(map[string]any{"error": map[string]any{"code": status, "message": http.StatusText(status)}})
	if encodeErr != nil {
		panic(encodeErr)
	} // fixed scalar fields are always encodable
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body) //nolint:errcheck,gosec // the client may have disconnected
}

func managementStatus(err error) int {
	switch {
	case errors.Is(err, accounts.ErrUnknownKey):
		return http.StatusUnauthorized
	case errors.Is(err, accounts.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, accounts.ErrKeyNotFound), errors.Is(err, accounts.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, errManagementInput):
		return http.StatusBadRequest
	case errors.Is(err, errManagementMethod):
		return http.StatusMethodNotAllowed
	case errors.Is(err, accounts.ErrStale), errors.Is(err, accounts.ErrManagementBusy), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable
	default:
		if _, unavailable := pg.Unavailable(err); unavailable {
			return http.StatusServiceUnavailable
		}
		return http.StatusInternalServerError
	}
}
