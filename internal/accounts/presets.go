package accounts

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// Preset is account-owned inference configuration, separate from a key's enforced routing restrictions.
type Preset struct {
	ID        uuid.UUID `json:"id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Config    jsonx.Raw `json:"config"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrPreset rejects malformed or oversized preset configuration.
var ErrPreset = errors.New("accounts: invalid preset")

// ErrPresetConflict is an occupied slug or an account at its preset limit.
var ErrPresetConflict = errors.New("accounts: preset slug exists or limit reached")

var presetSlug = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

// ValidatePresetConfig bounds stored configuration and excludes transient input and recursive references.
func ValidatePresetConfig(config []byte) error {
	var fields map[string]jsonx.Raw
	canonical, err := jsonx.MarshalCanonical(jsonx.Raw(config))
	if err != nil || len(canonical) > 16<<10 || jsonx.Unmarshal(canonical, &fields) != nil || fields == nil {
		return ErrPreset
	}
	for key, raw := range fields {
		if !presetFields[key] {
			return ErrPreset
		}
		if err := validatePresetMember(key, raw); err != nil {
			return err
		}
	}
	return nil
}

var presetFields = map[string]bool{
	"model":                 true,
	"models":                true,
	"provider":              true,
	"route":                 true,
	"temperature":           true,
	"top_p":                 true,
	"top_k":                 true,
	"min_p":                 true,
	"max_tokens":            true,
	"max_completion_tokens": true,
	"max_output_tokens":     true,
	"frequency_penalty":     true,
	"presence_penalty":      true,
	"repetition_penalty":    true,
	"seed":                  true,
	"stop":                  true,
	"stop_sequences":        true,
	"logit_bias":            true,
	"logprobs":              true,
	"top_logprobs":          true,
	"response_format":       true,
	"text":                  true,
	"reasoning":             true,
	"reasoning_effort":      true,
	"thinking":              true,
	"output_config":         true,
	"output_format":         true,
	"tools":                 true,
	"tool_choice":           true,
	"parallel_tool_calls":   true,
	"max_tool_calls":        true,
	"system":                true,
	"service_tier":          true,
	"speed":                 true,
	"cache_control":         true,
	"context_management":    true,
	"plugins":               true,
	"transforms":            true,
}

func validatePresetMember(key string, raw jsonx.Raw) error {
	switch key {
	case "model":
		return validatePresetModel(raw)
	case "models":
		var models []jsonx.Raw
		if jsonx.Unmarshal(raw, &models) != nil || len(models) > 8 {
			return ErrPreset
		}
		for _, model := range models {
			if err := validatePresetModel(model); err != nil {
				return err
			}
		}
	case "system":
		var system string
		if len(raw) == 0 || raw[0] != '"' || jsonx.Unmarshal(raw, &system) != nil {
			return ErrPreset
		}
	}
	return nil
}

func validatePresetModel(raw jsonx.Raw) error {
	var model string
	if jsonx.Unmarshal(raw, &model) != nil || len(model) == 0 || len(model) > 200 || strings.Contains(model, "@preset/") {
		return ErrPreset
	}
	return nil
}

// ListPresets reads one account's live configurations, newest first, with an exact total.
func ListPresets(ctx context.Context, tx pg.Tx, account AccountID, limit, offset int) ([]Preset, int, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, 0, ErrPreset
	}
	rows, err := tx.Query(ctx, `SELECT id,slug,name,config,created_at,updated_at FROM accounts.presets WHERE account_id=$1 AND deleted_at IS NULL ORDER BY updated_at DESC,id DESC LIMIT $2 OFFSET $3`, uuid.UUID(account), limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Preset{}
	for rows.Next() {
		var p Preset
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Config, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	rows.Close()
	var total int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM accounts.presets WHERE account_id=$1 AND deleted_at IS NULL`, uuid.UUID(account)).Scan(&total)
	return out, total, err
}

// GetPreset never resolves a slug outside the explicit account, even under a broader transaction scope.
func GetPreset(ctx context.Context, tx pg.Tx, account AccountID, slug string) (p Preset, err error) {
	err = tx.QueryRow(ctx, `SELECT id,slug,name,config,created_at,updated_at FROM accounts.presets WHERE account_id=$1 AND slug=$2 AND deleted_at IS NULL`, uuid.UUID(account), slug).Scan(&p.ID, &p.Slug, &p.Name, &p.Config, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pg.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

// SavePreset atomically creates or replaces a whole configuration; account locking also serializes the count bound.
func SavePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, account AccountID, p Preset, create bool) (Preset, error) {
	if !presetSlug.MatchString(p.Slug) || utf8.RuneCountInString(p.Name) < 1 || utf8.RuneCountInString(p.Name) > 100 || ValidatePresetConfig(p.Config) != nil {
		return Preset{}, ErrPreset
	}
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM accounts.accounts WHERE site_id=$1 AND id=$2 FOR UPDATE`, uuid.UUID(site), uuid.UUID(account)).Scan(&locked); err != nil {
		return Preset{}, err
	}
	action := "preset.update"
	if create {
		action = "preset.create"
		if err := insertPreset(ctx, tx, site, account, p); err != nil {
			return Preset{}, err
		}
	} else {
		tag, err := tx.Exec(ctx, `UPDATE accounts.presets SET name=$3,config=$4,updated_at=clock_timestamp() WHERE account_id=$1 AND slug=$2 AND deleted_at IS NULL`, locked, p.Slug, p.Name, []byte(p.Config))
		if err != nil {
			return Preset{}, err
		}
		if tag.RowsAffected() == 0 {
			return Preset{}, ErrNotFound
		}
	}
	out, err := GetPreset(ctx, tx, account, p.Slug)
	if err != nil {
		return Preset{}, err
	}
	return out, recordPreset(ctx, tx, actor, action, site, account, out.ID)
}

func insertPreset(ctx context.Context, tx pg.Tx, site SiteID, account AccountID, p Preset) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM accounts.presets WHERE account_id=$1 AND deleted_at IS NULL`, uuid.UUID(account)).Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return ErrPresetConflict
	}
	tag, err := tx.Exec(ctx, `INSERT INTO accounts.presets(id,site_id,account_id,slug,name,config) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id,slug) WHERE deleted_at IS NULL DO NOTHING`, uuid.NewV7(), uuid.UUID(site), uuid.UUID(account), p.Slug, p.Name, []byte(p.Config))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPresetConflict
	}
	return nil
}

// DeletePreset leaves a tombstone so rolling readers and old references cannot resurrect deleted rows.
func DeletePreset(ctx context.Context, tx pg.Tx, actor audit.Actor, site SiteID, account AccountID, slug string) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `UPDATE accounts.presets SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE site_id=$1 AND account_id=$2 AND slug=$3 AND deleted_at IS NULL RETURNING id`, uuid.UUID(site), uuid.UUID(account), slug).Scan(&id)
	if errors.Is(err, pg.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return recordPreset(ctx, tx, actor, "preset.delete", site, account, id)
}

func recordPreset(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, site SiteID, account AccountID, id uuid.UUID) error {
	return audit.Record(ctx, tx, audit.Event{Actor: actor, Action: action, Target: audit.Target{Module: "accounts", Kind: "preset", ID: id.String(), Site: uuid.UUID(site), Account: uuid.UUID(account)}, Detail: map[string]any{}})
}

type presetKey struct {
	site    SiteID
	account AccountID
	slug    string
}

func readPresets(ctx context.Context, tx pg.Tx) (map[presetKey]string, error) {
	rows, err := tx.Query(ctx, `SELECT site_id,account_id,slug,config FROM accounts.presets WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[presetKey]string{}
	for rows.Next() {
		var key presetKey
		var config string
		if err := rows.Scan(&key.site, &key.account, &key.slug, &config); err != nil {
			return nil, err
		}
		if err := ValidatePresetConfig([]byte(config)); err != nil {
			return nil, fmt.Errorf("load preset %s: %w", key.slug, err)
		}
		out[key] = config
	}
	return out, rows.Err()
}

// PresetConfig reads immutable configuration from the tenant-keyed, freshness-checked snapshot, without database I/O.
func (s *Service) PresetConfig(site SiteID, account AccountID, slug string) (string, error) {
	snap := s.snapshot.Load()
	if snap == nil || s.now().Sub(snap.loaded) > staleAfter {
		return "", ErrStale
	}
	config, ok := snap.presets[presetKey{site, account, slug}]
	if !ok {
		return "", ErrNotFound
	}
	return config, nil
}
