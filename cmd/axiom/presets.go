package main

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

type managementPayload struct {
	keys    managedInput
	preset  []byte
	presets bool
}

func readManagementPayload(r *http.Request) (managementPayload, error) {
	in := managementPayload{presets: strings.HasPrefix(r.URL.Path, "/api/v1/presets")}
	if !in.presets {
		var err error
		in.keys, err = readManagementInput(r)
		return in, err
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch {
		var err error
		in.preset, err = io.ReadAll(r.Body)
		if err != nil {
			return in, errManagementInput
		}
	}
	return in, nil
}

func (in managementPayload) dispatch(r *http.Request, m accounts.KeyManager, tx pg.Tx, caller accounts.Caller) (any, int, error) {
	if in.presets {
		return dispatchPresets(r, tx, caller, in.preset)
	}
	return dispatchManagement(r, m, tx, in.keys)
}

func dispatchPresets(r *http.Request, tx pg.Tx, caller accounts.Caller, input []byte) (any, int, error) {
	slug := strings.TrimPrefix(r.URL.Path, "/api/v1/presets")
	slug = strings.TrimPrefix(slug, "/")
	if slug == "" {
		return presetCollection(r, tx, caller, input)
	}
	if strings.Contains(slug, "/") {
		return nil, 0, accounts.ErrNotFound
	}
	if r.Method == http.MethodDelete {
		err := accounts.DeletePreset(r.Context(), tx, audit.ManagementKey(uuid.UUID(caller.KeyID())), caller.SiteID(), caller.AccountID(), slug)
		return map[string]any{"deleted": true}, http.StatusOK, err
	}
	var p accounts.Preset
	var err error
	switch r.Method {
	case http.MethodGet:
		p, err = accounts.GetPreset(r.Context(), tx, caller.AccountID(), slug)
	case http.MethodPatch:
		p, err = patchPreset(r, tx, caller, slug, input)
	default:
		return nil, 0, errManagementMethod
	}
	return map[string]any{"data": p}, http.StatusOK, err
}

func patchPreset(r *http.Request, tx pg.Tx, c accounts.Caller, slug string, input []byte) (accounts.Preset, error) {
	var in struct {
		Name   *string   `json:"name"`
		Config jsonx.Raw `json:"config"`
	}
	if jsonx.UnmarshalKnown(input, &in) != nil || in.Name == nil && len(in.Config) == 0 {
		return accounts.Preset{}, accounts.ErrPreset
	}
	// The same account lock as replacement prevents a PATCH from losing a concurrent console write.
	var id uuid.UUID
	if err := tx.QueryRow(r.Context(), `SELECT id FROM accounts.accounts WHERE id=$1 FOR UPDATE`, uuid.UUID(c.AccountID())).Scan(&id); err != nil {
		return accounts.Preset{}, err
	}
	p, err := accounts.GetPreset(r.Context(), tx, c.AccountID(), slug)
	if err != nil {
		return p, err
	}
	if in.Name != nil {
		p.Name = *in.Name
	}
	if len(in.Config) > 0 {
		p.Config = in.Config
	}
	return accounts.SavePreset(r.Context(), tx, audit.ManagementKey(uuid.UUID(c.KeyID())), c.SiteID(), c.AccountID(), p, false)
}

func presetCollection(r *http.Request, tx pg.Tx, c accounts.Caller, input []byte) (any, int, error) {
	switch r.Method {
	case http.MethodGet:
		limit, offset := 50, 0
		for name, values := range r.URL.Query() {
			if len(values) != 1 {
				return nil, 0, accounts.ErrPreset
			}
			v, err := strconv.Atoi(values[0])
			if err != nil {
				return nil, 0, accounts.ErrPreset
			}
			switch name {
			case "limit":
				limit = v
			case "offset":
				offset = v
			default:
				return nil, 0, accounts.ErrPreset
			}
		}
		data, total, err := accounts.ListPresets(r.Context(), tx, c.AccountID(), limit, offset)
		return map[string]any{"data": data, "total_count": total}, http.StatusOK, err
	case http.MethodPost:
		var in struct {
			Slug   string    `json:"slug"`
			Name   string    `json:"name"`
			Config jsonx.Raw `json:"config"`
		}
		if jsonx.UnmarshalKnown(input, &in) != nil {
			return nil, 0, accounts.ErrPreset
		}
		p, err := accounts.SavePreset(r.Context(), tx, audit.ManagementKey(uuid.UUID(c.KeyID())), c.SiteID(), c.AccountID(), accounts.Preset{Slug: in.Slug, Name: in.Name, Config: in.Config}, true)
		return map[string]any{"data": p}, http.StatusCreated, err
	default:
		return nil, 0, errManagementMethod
	}
}
