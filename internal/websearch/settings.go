package websearch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/secret"
)

// ErrInvalidConfig identifies an engine configuration the admin cannot save.
var ErrInvalidConfig = errors.New("websearch: invalid search backend configuration")

var searchLocation = secret.Location{Table: "platform.search_config", RowID: uuid.Nil().String(), Purpose: "config"}

func engineLocation(id uuid.UUID) secret.Location {
	return secret.Location{Table: "platform.search_engines", RowID: id.String(), Purpose: "config"}
}

// EngineSettings is one engine at the admin boundary. Key is nil when the caller did not send one, which keeps the
// saved key while the URL is unchanged; it is never returned. Cost and price are decimal text so they are exact.
type EngineSettings struct {
	ID         string  `json:"id,omitempty"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	URL        string  `json:"url"`
	Key        *string `json:"key,omitempty"`
	Enabled    bool    `json:"enabled"`
	Default    bool    `json:"default"`
	CostSearch string  `json:"cost_per_search"`
	CostResult string  `json:"cost_per_extra_result"`
	Markup     string  `json:"markup"`
}

// Settings is the whole list the admin edits. An empty list turns the plugin off and lets the saved legacy row or the
// environment be authoritative again.
type Settings struct {
	Engines []EngineSettings `json:"engines"`
}

// Loaded is what a node starts with: the engines and where they came from, which the admin page shows.
type Loaded struct {
	Engines []Engine
	Source  string // "database", "saved", "environment" or "none"
}

// maxEngines bounds the list: a deployment needs a few, and a bound keeps one save from filling the table.
const maxEngines = 16

// Load reads the engine list. With no rows it falls back to the old single platform.search_config row, then to the
// environment, so an existing deployment keeps its backend and a new one can still be configured from AXIOM_SEARCH_URL.
func Load(ctx context.Context, tx pg.Tx, keys *secret.Keyring, environment Config) (Loaded, error) {
	engines, err := loadRows(ctx, tx, keys)
	if err != nil {
		return Loaded{}, err
	}
	if len(engines) > 0 {
		return Loaded{Engines: engines, Source: "database"}, nil
	}
	cfg, found, err := StoredConfig(ctx, tx, keys)
	if err != nil {
		return Loaded{}, err
	}
	if found {
		if !cfg.Configured() {
			return Loaded{Source: "none"}, nil
		}
		return Loaded{Engines: []Engine{legacyEngine(cfg)}, Source: "saved"}, nil
	}
	if environment.Configured() {
		return Loaded{Engines: []Engine{legacyEngine(environment)}, Source: "environment"}, nil
	}
	return Loaded{Source: "none"}, nil
}

// legacyEngine is the one backend the old configuration named, as an engine. It has no published price, so its
// searches are sold at the model's web_search price, which is what the plugin charged before engines existed.
func legacyEngine(cfg Config) Engine {
	return Engine{Name: "searxng", Kind: KindSearXNG, URL: cfg.URL, Key: cfg.Key, Enabled: true, Default: true, Markup: "0", PriceVersion: 1}
}

// engineRecord is one row before its prices, kind and key are decoded. The row is scanned as stored so the decode
// stays one function with one error message per field.
type engineRecord struct {
	engine                   Engine
	kind                     string
	costSearch, costResult   string
	priceSearch, priceResult string
	sealed                   secret.Sealed
}

func (r engineRecord) decode(keys *secret.Keyring) (Engine, error) {
	e := r.engine
	var err error
	if e.Kind, err = ParseKind(r.kind); err != nil {
		return Engine{}, err
	}
	if e.CostSearch, err = platform.ParseUnitPrice(r.costSearch); err != nil {
		return Engine{}, fmt.Errorf("websearch: engine %q: %w", e.Name, err)
	}
	if e.CostResult, err = platform.ParseUnitPrice(r.costResult); err != nil {
		return Engine{}, fmt.Errorf("websearch: engine %q: %w", e.Name, err)
	}
	if e.PriceSearch, err = platform.ParseUnitPrice(r.priceSearch); err != nil {
		return Engine{}, fmt.Errorf("websearch: engine %q: %w", e.Name, err)
	}
	if e.PriceResult, err = platform.ParseUnitPrice(r.priceResult); err != nil {
		return Engine{}, fmt.Errorf("websearch: engine %q: %w", e.Name, err)
	}
	if markup, ok := new(big.Rat).SetString(e.Markup); !ok || markup.Sign() < 0 {
		return Engine{}, fmt.Errorf("websearch: engine %q: markup %q is not a nonnegative decimal", e.Name, e.Markup)
	}
	plain, err := keys.Open(r.sealed, engineLocation(e.ID))
	if err != nil {
		return Engine{}, err
	}
	e.Key = secret.New(plain.Reveal())
	return e, nil
}

// loadRows reads every engine, disabled ones included: the admin edits them, and only the node's OpenEngines leaves
// the disabled ones out.
func loadRows(ctx context.Context, tx pg.Tx, keys *secret.Keyring) ([]Engine, error) {
	rows, err := tx.Query(ctx, `SELECT id,name,kind,url,enabled,is_default,cost_search,cost_result,markup,
 price_search,price_result,price_version,ciphertext,nonce,wrapped_dek,key_version,updated_at
 FROM platform.search_engines ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Engine
	for rows.Next() {
		var r engineRecord
		if err := rows.Scan(&r.engine.ID, &r.engine.Name, &r.kind, &r.engine.URL, &r.engine.Enabled, &r.engine.Default,
			&r.costSearch, &r.costResult, &r.engine.Markup, &r.priceSearch, &r.priceResult, &r.engine.PriceVersion,
			&r.sealed.Ciphertext, &r.sealed.Nonce, &r.sealed.WrappedDEK, &r.sealed.KeyVersion, &r.engine.UpdatedAt); err != nil {
			return nil, err
		}
		e, err := r.decode(keys)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// StoredConfig opens the legacy single-backend configuration; absent means neither the admin nor the environment
// configured one.
func StoredConfig(ctx context.Context, tx pg.Tx, keys *secret.Keyring) (Config, bool, error) {
	var sealed secret.Sealed
	err := tx.QueryRow(ctx, `SELECT ciphertext,nonce,wrapped_dek,key_version FROM platform.search_config WHERE id=$1`, uuid.Nil()).
		Scan(&sealed.Ciphertext, &sealed.Nonce, &sealed.WrappedDEK, &sealed.KeyVersion)
	if errors.Is(err, pg.ErrNoRows) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	plain, err := keys.Open(sealed, searchLocation)
	if err != nil {
		return Config{}, false, err
	}
	var s struct {
		URL string  `json:"url"`
		Key *string `json:"key"`
	}
	if err = jsonx.Unmarshal([]byte(plain.Reveal()), &s); err != nil {
		return Config{}, false, err
	}
	key := ""
	if s.Key != nil {
		key = *s.Key
	}
	cfg, err := Parse(s.URL, key)
	return cfg, true, err
}

// Save validates the list, seals each engine's key, publishes its prices and writes the audit entry in one
// transaction. The list replaces the saved one: an engine left out is removed.
func Save(ctx context.Context, tx pg.Tx, keys *secret.Keyring, actor audit.Actor, in Settings, environment Config, reason string) (Loaded, error) {
	if len(in.Engines) > maxEngines {
		return Loaded{}, fmt.Errorf("%w: at most %d engines", ErrInvalidConfig, maxEngines)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('platform.search_engines'))`); err != nil {
		return Loaded{}, err
	}
	old, err := Load(ctx, tx, keys, environment)
	if err != nil {
		return Loaded{}, err
	}
	byID := make(map[uuid.UUID]Engine, len(old.Engines))
	for _, e := range old.Engines {
		if e.ID != uuid.Nil() {
			byID[e.ID] = e
		}
	}
	next, err := prepareEngines(in, byID, old.Engines)
	if err != nil {
		return Loaded{}, err
	}
	names, def, err := replaceEngines(ctx, tx, keys, next)
	if err != nil {
		return Loaded{}, err
	}
	err = audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "search_engines.update", Reason: reason,
		Target: audit.Target{Module: "platform", Kind: "search_engines", ID: uuid.Nil().String()},
		Detail: map[string]any{"engines": names, "default": def},
	})
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Engines: next, Source: "database"}, nil
}

// replaceEngines deletes the saved list and writes the prepared one, returning the audit names and the default.
func replaceEngines(ctx context.Context, tx pg.Tx, keys *secret.Keyring, next []Engine) ([]string, string, error) {
	names := make([]string, 0, len(next))
	def := ""
	if _, err := tx.Exec(ctx, `DELETE FROM platform.search_engines`); err != nil {
		return nil, "", err
	}
	if len(next) == 0 {
		// No row can say "no engines": an empty table is indistinguishable from one never saved, and Load would fall
		// back to the environment. The old single-backend row's empty form is the marker that says the admin turned
		// search off, which is what clearing the list meant before engines existed.
		if err := saveDisabled(ctx, tx, keys); err != nil {
			return nil, "", err
		}
	}
	for _, e := range next {
		sealed := keys.Seal(secret.New(e.Key.Reveal()), engineLocation(e.ID))
		if _, err := tx.Exec(ctx, `INSERT INTO platform.search_engines
 (id,name,kind,url,enabled,is_default,cost_search,cost_result,markup,price_search,price_result,price_version,
  ciphertext,nonce,wrapped_dek,key_version)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
			e.ID, e.Name, string(e.Kind), e.URL, e.Enabled, e.Default, e.CostSearch.String(), e.CostResult.String(),
			e.Markup, e.PriceSearch.String(), e.PriceResult.String(), e.PriceVersion,
			sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion); err != nil {
			return nil, "", err
		}
		names = append(names, e.Name)
		if e.Default {
			def = e.Name
		}
	}
	return names, def, nil
}

// saveDisabled writes the empty legacy row that makes a cleared list authoritative over the environment.
func saveDisabled(ctx context.Context, tx pg.Tx, keys *secret.Keyring) error {
	plain, err := jsonx.MarshalCanonical(struct {
		URL string `json:"url"`
	}{})
	if err != nil {
		return err
	}
	sealed := keys.Seal(secret.New(string(plain)), searchLocation)
	_, err = tx.Exec(ctx, `INSERT INTO platform.search_config (id,ciphertext,nonce,wrapped_dek,key_version)
 VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO UPDATE SET ciphertext=EXCLUDED.ciphertext,nonce=EXCLUDED.nonce,
 wrapped_dek=EXCLUDED.wrapped_dek,key_version=EXCLUDED.key_version`,
		uuid.Nil(), sealed.Ciphertext, sealed.Nonce, sealed.WrappedDEK, sealed.KeyVersion)
	return err
}

// prepareEngines validates one list and fills in what the caller left to the saved state: an omitted key keeps the
// saved one while the URL is unchanged, and a price keeps its version until the cost or markup changes.
func prepareEngines(in Settings, old map[uuid.UUID]Engine, legacy []Engine) ([]Engine, error) {
	out := make([]Engine, 0, len(in.Engines))
	names := make(map[string]struct{}, len(in.Engines))
	defaults := 0
	for _, s := range in.Engines {
		e, err := prepareEngine(s, old, legacy)
		if err != nil {
			return nil, err
		}
		if _, ok := names[e.Name]; ok {
			return nil, fmt.Errorf("%w: engine %q is listed twice", ErrInvalidConfig, e.Name)
		}
		names[e.Name] = struct{}{}
		if e.Enabled && e.Default {
			defaults++
		}
		out = append(out, e)
	}
	switch {
	case len(out) == 0:
	case defaults != 1:
		return nil, fmt.Errorf("%w: exactly one enabled engine must be the default", ErrInvalidConfig)
	}
	return out, nil
}

func prepareEngine(s EngineSettings, old map[uuid.UUID]Engine, legacy []Engine) (Engine, error) {
	e := Engine{
		Name: strings.TrimSpace(s.Name), Enabled: s.Enabled, Default: s.Default,
		Markup: strings.TrimSpace(s.Markup),
	}
	kind, err := ParseKind(strings.TrimSpace(s.Kind))
	if err != nil {
		return Engine{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	e.Kind = kind
	if !ValidName(e.Name) {
		return Engine{}, fmt.Errorf("%w: %q is not an engine name (lowercase letters, digits, '-' and '_', at most 32, not native)", ErrInvalidConfig, e.Name)
	}
	e.URL = strings.TrimSpace(s.URL)
	if e.URL == "" && e.Kind == KindFirecrawl {
		e.URL = firecrawlBase
	}
	if e.URL, err = checkURL(e.URL); err != nil {
		return Engine{}, fmt.Errorf("%w: %w", ErrInvalidConfig, err)
	}
	id, err := parseID(s.ID)
	if err != nil {
		return Engine{}, err
	}
	e.ID = id
	previous, found := savedEngine(s, e, old, legacy)
	key, err := engineKey(s, e.URL, previous, found, e.Kind)
	if err != nil {
		return Engine{}, err
	}
	e.Key = secret.New(key)
	if err := applyEnginePrices(&e, s, previous, found); err != nil {
		return Engine{}, err
	}
	return e, nil
}

// applyEnginePrices validates the cost and markup and derives the published price, keeping its version while the
// inputs are unchanged.
func applyEnginePrices(e *Engine, s EngineSettings, previous Engine, found bool) error {
	if err := applyEngineCosts(e, s); err != nil {
		return err
	}
	markup, err := engineMarkup(e.Markup)
	if err != nil {
		return err
	}
	if err := scaleEnginePrices(e, markup); err != nil {
		return err
	}
	e.PriceVersion = enginePriceVersion(previous, *e, found)
	return nil
}

func applyEngineCosts(e *Engine, s EngineSettings) error {
	var err error
	if e.CostSearch, err = parsePrice(s.CostSearch); err != nil {
		return fmt.Errorf("%w: cost_per_search: %w", ErrInvalidConfig, err)
	}
	if e.CostResult, err = parsePrice(s.CostResult); err != nil {
		return fmt.Errorf("%w: cost_per_extra_result: %w", ErrInvalidConfig, err)
	}
	return nil
}

func engineMarkup(text string) (*big.Rat, error) {
	markup, ok := new(big.Rat).SetString(text)
	if !ok || markup.Sign() < 0 || markup.Cmp(big.NewRat(10, 1)) > 0 {
		return nil, fmt.Errorf("%w: markup must be a decimal from 0 to 10", ErrInvalidConfig)
	}
	return markup, nil
}

func scaleEnginePrices(e *Engine, markup *big.Rat) error {
	onePlus := new(big.Rat).Add(big.NewRat(1, 1), markup)
	var err error
	if e.PriceSearch, err = e.CostSearch.Scale(onePlus); err != nil {
		return fmt.Errorf("%w: price_per_search: %w", ErrInvalidConfig, err)
	}
	if e.PriceResult, err = e.CostResult.Scale(onePlus); err != nil {
		return fmt.Errorf("%w: price_per_extra_result: %w", ErrInvalidConfig, err)
	}
	return nil
}

// enginePriceVersion keeps the version while cost and markup are unchanged, and raises it when they change.
func enginePriceVersion(previous Engine, next Engine, found bool) int64 {
	if !found {
		return 1
	}
	if previous.CostSearch == next.CostSearch && previous.CostResult == next.CostResult && previous.Markup == next.Markup {
		return previous.PriceVersion
	}
	return previous.PriceVersion + 1
}

// savedEngine finds what an input edits: the saved row it names, or, for an input that names none, a saved row with
// the same name, or the old single backend or environment configuration at the same destination. That last match is
// what lets an admin save the backend the page shows from the environment without retyping its key.
func savedEngine(s EngineSettings, e Engine, byID map[uuid.UUID]Engine, legacy []Engine) (Engine, bool) {
	if id := strings.TrimSpace(s.ID); id != "" {
		if parsed, err := uuid.Parse(id); err == nil && parsed != uuid.Nil() {
			previous, found := byID[parsed]
			return previous, found
		}
	}
	for _, previous := range legacy {
		if previous.ID != uuid.Nil() && previous.Name == e.Name {
			return previous, true
		}
	}
	for _, previous := range legacy {
		if previous.ID == uuid.Nil() && previous.URL == e.URL && previous.Kind == e.Kind {
			return previous, true
		}
	}
	return Engine{}, false
}

// engineKey is the key the saved engine carries: the caller's when it sent one, the saved one when the URL and kind
// are unchanged, and nothing when the caller cleared it. A new destination always needs the key again, so a saved
// credential never authenticates to a backend it was not entered for.
func engineKey(s EngineSettings, url string, old Engine, found bool, kind Kind) (string, error) {
	key := ""
	if s.Key != nil {
		key = strings.TrimSpace(*s.Key)
	} else if found && old.URL == url && old.Kind == kind {
		key = old.Key.Reveal()
	}
	// An explicit empty key is still an empty key: Firecrawl cannot open without one, so accepting it here would
	// save a configuration that makes the next node fail to start. The final key is checked, not just its presence.
	if kind == KindFirecrawl && key == "" {
		return "", fmt.Errorf("%w: firecrawl needs an API key", ErrInvalidConfig)
	}
	return key, nil
}

func parsePrice(s string) (platform.UnitPrice, error) {
	return platform.ParseUnitPrice(strings.TrimSpace(s))
}

func parseID(s string) (uuid.UUID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return uuid.NewV7(), nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil(), fmt.Errorf("%w: %q is not an engine id", ErrInvalidConfig, s)
	}
	if id == uuid.Nil() {
		// A zero UUID is how an older page spells "no entity id"; it must create an engine, not look up a row that
		// cannot exist, or the first save from that page would lose the legacy key.
		return uuid.NewV7(), nil
	}
	return id, nil
}

// checkURL is the shape every engine's base URL must have. A Firecrawl URL already defaulted to its documented base.
func checkURL(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("the backend URL is required")
	}
	cfg, err := Parse(raw, "")
	if err != nil {
		return "", err
	}
	return cfg.URL, nil
}

// TestEngine runs one fixed search through a configured engine and returns only a stable, non-sensitive outcome: the
// query is fixed, and the error is logged without the backend's body.
func TestEngine(ctx context.Context, e Engine, client *egress.Client, logger *slog.Logger) string {
	if !e.Enabled || e.URL == "" {
		return "disabled"
	}
	backend, err := openEngine(e, client)
	if err != nil {
		logger.WarnContext(ctx, "search engine test failed", "request_id", httpserver.RequestID(ctx), "err", err)
		return "failed"
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if _, err := backend.Search(ctx, Query{Text: "axiom", MaxResults: 1}); err != nil {
		logger.WarnContext(ctx, "search engine test failed", "request_id", httpserver.RequestID(ctx), "err", err)
		return "failed"
	}
	return "accepted"
}
