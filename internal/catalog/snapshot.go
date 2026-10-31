package catalog

import (
	"errors"
	"iter"
	"maps"
	"slices"
	"time"
	"uuid"
)

// ModelID identifies a model. It is defined here, not in platform: only modules above catalog refer to it.
type ModelID uuid.UUID

func (id ModelID) String() string { return uuid.UUID(id).String() }

// MarshalText and UnmarshalText write and read the id as text, as uuid.UUID does, which a defined type does not inherit.
func (id ModelID) MarshalText() ([]byte, error) { return uuid.UUID(id).MarshalText() }

// UnmarshalText reads the id from its text.
func (id *ModelID) UnmarshalText(b []byte) error { return (*uuid.UUID)(id).UnmarshalText(b) }

// Status is where a model is in its life. M1 has no scheduled retirement, so a model is drafted, listed, then retired.
type Status string

// The statuses a model goes through, in order.
const (
	StatusDraft   Status = "draft"
	StatusListed  Status = "listed"
	StatusRetired Status = "retired"
)

// VersionBasis is what makes a model one version of the vendor's: a dated name, or a name the vendor moves.
type VersionBasis string

// The version bases (docs/domains/catalog.md「模型与名字」).
const (
	VersionDated    VersionBasis = "dated"
	VersionFloating VersionBasis = "floating"
)

// PriceSource is where a model's reference prices come from.
type PriceSource string

// The price sources (docs/domains/catalog.md「目录基线」).
const (
	SourceLiteLLM    PriceSource = "litellm"
	SourceOpenRouter PriceSource = "openrouter"
	SourceOfficial   PriceSource = "official"
	SourceManual     PriceSource = "manual"
)

// Visibility says who may call a name: anyone, or only our own mappings and logs.
type Visibility string

// The name visibilities (docs/domains/catalog.md「模型与名字」).
const (
	VisibilityPublic   Visibility = "public"
	VisibilityInternal Visibility = "internal"
)

// Model is a listed or retired model as a snapshot holds it.
type Model struct {
	ID           ModelID
	Slug         string
	Author       string
	Status       Status
	VersionBasis VersionBasis
	PriceSource  PriceSource
	Rev          int64
	spec         string // the baseline's JSON, kept as a string so no caller can change what a snapshot holds
	reportable   Reportable
	serving      Serving
	serves       bool // the spec declares everything Serving reads
	directServes bool // context is declared; a direct-only format needs neither an output limit nor streaming
	toolUses     map[Item]int64
	dialect      Dialect
	speed        int64     // the fastest output, in tokens per second; 0 when the spec does not say
	reasoning    FormatSet // the call formats in which the model's stream shows reasoning progress
}

// Spec is a copy of the baseline's JSON.
func (m Model) Spec() []byte { return []byte(m.spec) }

// ToolUses returns a copy of the spec's default server tool bounds, so a caller cannot change the snapshot.
func (m Model) ToolUses() map[Item]int64 { return maps.Clone(m.toolUses) }

// Reportable is what the upstream may report for the model, from its spec, for the listing gate.
func (m Model) Reportable() Reportable { return m.reportable }

// OutputSpeed is the fastest output the model is known to produce, in tokens per second, from its spec. Billing sizes
// a segment's overshoot allowance from it, and routing reserves an entry whole while it is 0, which is a spec that does
// not say (README.md「规格」).
func (m Model) OutputSpeed() int64 { return m.speed }

// ReasoningVisible are the call formats in which the model's stream shows its reasoning as it is produced, from its
// spec, for a model whose spec lists the reasoning ability: one that does not reason has nothing to hide. The gateway
// must see that progress to meter it against a segment; an entry called in a format not in the set is reserved whole
// (README.md「规格」).
func (m Model) ReasoningVisible() FormatSet { return m.reasoning }

// CallFormat is a request format as a model's spec names it. It is catalog's own enumeration, not protocol's, because
// protocol is built on catalog: the two are mapped name by name where a routing snapshot is assembled.
type CallFormat uint8

// The call formats, in the order stored spec lists name them.
const (
	FormatMessages  CallFormat = iota // Anthropic Messages
	FormatResponses                   // OpenAI Responses
	FormatChat                        // OpenAI Chat Completions
	NumFormats
)

// formatNames are the formats' names in a spec, so they never change.
var formatNames = [NumFormats]string{"messages", "responses", "chat"}

// FormatSet is a set of call formats.
type FormatSet = set[CallFormat]

// A seventeenth format would have no bit, as with the items.
var _ [16 - NumFormats]struct{}

// Ability is a feature a model has that a request may need: what protocol's Capabilities are the bits of. The names are
// the spec's, and protocol turns them into its bits when a routing snapshot is built (README.md「规格」).
type Ability uint8

// The abilities, in the order stored spec lists name them.
const (
	AbilityTools Ability = iota
	AbilityImages
	AbilityDocuments
	AbilityReasoning
	AbilityPromptCache
	AbilityStructuredOutput
	AbilityWebSearch
	AbilityCodeExecution
	NumAbilities
)

// abilityNames are the abilities' names in a spec, so they never change.
var abilityNames = [NumAbilities]string{
	"tools", "images", "documents", "reasoning", "prompt_cache", "structured_output", "web_search", "code_execution",
}

func (a Ability) String() string { return abilityNames[a] }

// ParseAbility returns the ability a spec's name denotes: the inverse of Ability.String.
func ParseAbility(name string) (Ability, error) { return lookup[Ability](abilityNames[:], name) }

// AbilitySet is a set of abilities.
type AbilitySet = set[Ability]

// A seventeenth ability would have no bit, as with the items.
var _ [16 - NumAbilities]struct{}

// Serving is what an endpoint of a model must offer before routing sends requests to it (docs/domains/routing.md
// 「一个请求的路由步骤」). It is read from the model's spec, never guessed: a spec that does not declare these says
// nothing about them, which is not the same as saying they are zero.
type Serving struct {
	Context   int64 // the most tokens a request may take, input plus output
	MaxOutput int64 // the model's output limit, also its default
	Streams   bool  // the vendor streams the model's replies
	Abilities AbilitySet
}

// Serving reads the spec keys an endpoint needs. ok is false unless the spec declares all of them: an endpoint whose
// model does not say how much it takes must not be routed to, and a plausible default would make it routable with the
// wrong limit. It is all of them or none, so a caller that ignores ok cannot act on half a reading.
func (m Model) Serving() (Serving, bool) {
	if !m.serves {
		return Serving{}, false
	}
	return m.serving, true
}

// ServingDirect reads the spec key an endpoint of a direct-only format needs: its context length. It does not require
// an output limit, streaming or abilities, none of which a model that generates nothing uses. A model that also serves
// a generation format must satisfy Serving instead: its output limit and streaming are then not optional.
func (m Model) ServingDirect() (Serving, bool) {
	if !m.directServes {
		return Serving{}, false
	}
	return m.serving, true
}

// Claim is what a spec says of a yes-or-no matter: nothing, no, or yes. Absent and false are different claims, so the
// zero value is that the spec says nothing, which a caller must not take for a no (nor a yes).
type Claim uint8

// The claims.
const (
	ClaimUnset Claim = iota
	ClaimNo
	ClaimYes
)

func claimOf(b *bool) Claim {
	switch {
	case b == nil:
		return ClaimUnset
	case *b:
		return ClaimYes
	}
	return ClaimNo
}

// Thinking is how a spec says a model is asked to reason, as protocol's conversions need it (internal/protocol/README.md
// 「跨格式转换」).
type Thinking uint8

// The ways to ask. ThinkingNone is a model that does not think, which is a claim; unset is a spec that says nothing.
const (
	ThinkingUnset Thinking = iota
	ThinkingNone
	// ThinkingAdaptive asks with thinking.type adaptive and output_config.effort.
	ThinkingAdaptive
	// ThinkingManual asks with a thinking budget, budget_tokens, which the newer models refuse.
	ThinkingManual
)

// thinkingByName are the thinking modes' names in a spec, so they never change.
var thinkingByName = map[string]Thinking{"none": ThinkingNone, "adaptive": ThinkingAdaptive, "manual": ThinkingManual}

// Dialect is how a request to the model is written: what a conversion or a same-format rewrite needs of the model beyond
// what it can take in (Serving). Each key is optional and independent: a spec that leaves one out has not said no, so
// what a caller does with ClaimUnset is its own to decide, and never a default for the model.
type Dialect struct {
	// Thinking is the spec's "thinking": "adaptive", "manual" or "none".
	Thinking Thinking
	// EncryptedReasoning is "encrypted_reasoning": the model returns its reasoning encrypted on request.
	EncryptedReasoning Claim
	// MidSystem is "mid_system": the model takes a system message after the conversation has started.
	MidSystem Claim
	// UntrustedSystemAfterTools is "untrusted_system_after_tools": the model reads a system message right after tool
	// results as text it may not trust.
	UntrustedSystemAfterTools Claim
	// DefaultSamplingOnly is "default_sampling_only": the model refuses a temperature or top_p other than the default.
	DefaultSamplingOnly Claim
	// ForcedToolChoice is "forced_tool_choice": the model takes a tool choice that forces a call, when it is not
	// thinking. The vendor decides this apart from sampling (Claude Haiku 5.5 refuses the one and takes the other).
	ForcedToolChoice Claim
	// CacheBreakpoints is "cache_breakpoints": the model takes explicit cache breakpoints.
	CacheBreakpoints Claim
}

// Dialect reads the spec keys that say how requests to the model are written. Unlike Serving it needs no ok: a key the
// spec leaves out is unset, and each field says so on its own.
func (m Model) Dialect() Dialect { return m.dialect }

// ReferencePrice is the current reference price of a model in one currency.
type ReferencePrice struct {
	ID          uuid.UUID
	Model       ModelID
	Currency    string
	Source      PriceSource
	Schedule    Schedule
	PublishedAt time.Time
}

// Resolution is the model a public name stands for. Floating is set when the vendor moves the version behind the name.
type Resolution struct {
	Model    ModelID
	Floating bool
}

// Recognition is the model a name stands for, public or internal. Exact is set when the name itself is evidence of the
// version: it carries a date, or the model's version is the name's.
type Recognition struct {
	Model ModelID
	Exact bool
}

// ErrModelNotFound is what a name that no public name or alias matches gets; ErrRetired is for the name of a retired
// model.
var (
	ErrModelNotFound = errors.New("the model does not exist")
	ErrRetired       = errors.New("the model is retired")
)

// Snapshot is the catalog as of one revision, built whole and never changed, so a request that holds one sees one
// consistent state (docs/glossary.md「快照」). It holds listed and retired models, their names and aliases, and the
// current reference prices of the listed ones.
type Snapshot struct {
	revision int64
	models   map[ModelID]Model
	listed   []ModelID // in id order
	names    map[string]nameEntry
	aliases  map[string]ModelID
	prices   map[priceKey]ReferencePrice
}

type nameEntry struct {
	model    ModelID
	public   bool
	retired  bool
	floating bool
	exact    bool
}

type priceKey struct {
	model    ModelID
	currency string
}

// Revision is the database revision the snapshot was loaded at.
func (s *Snapshot) Revision() int64 { return s.revision }

// ResolvePublic finds the model a public name or alias from a user stands for. Names are exact: :free names its own
// catalog entry and a suffix never lands on the base model. Aliases resolve to the concrete model their latest
// revision names. Only public names are callable. It does not allocate.
func (s *Snapshot) ResolvePublic(name string) (Resolution, error) {
	if e, ok := s.lookup(name); ok {
		switch {
		case !e.public:
			return Resolution{}, ErrModelNotFound
		case e.retired:
			return Resolution{}, ErrRetired
		}
		return Resolution{Model: e.model, Floating: e.floating}, nil
	}
	id, ok := s.lookupAlias(name)
	if !ok {
		return Resolution{}, ErrModelNotFound
	}
	m, ok := s.models[id]
	switch {
	case !ok || m.Status == StatusDraft:
		return Resolution{}, ErrModelNotFound
	case m.Status == StatusRetired:
		return Resolution{}, ErrRetired
	}
	return Resolution{Model: id, Floating: m.VersionBasis == VersionFloating}, nil
}

// Recognize finds the model a name stands for among public and internal names, retired models' included, so that a
// supplier does not map a retired model's name to another model. Aliases are not supplier names and are not recognized.
func (s *Snapshot) Recognize(name string) (Recognition, bool) {
	e, ok := s.lookup(name)
	return Recognition{Model: e.model, Exact: e.exact}, ok
}

func (s *Snapshot) lookup(name string) (nameEntry, bool) {
	if len(name) > maxNameLen {
		return nameEntry{}, false
	}
	var buf [maxNameLen]byte
	e, ok := s.names[string(appendNormalized(buf[:0], name))] // the compiler does not allocate for this lookup
	return e, ok
}

func (s *Snapshot) lookupAlias(name string) (ModelID, bool) {
	if len(name) > maxNameLen {
		return ModelID{}, false
	}
	var buf [maxNameLen]byte
	id, ok := s.aliases[string(appendNormalized(buf[:0], name))]
	return id, ok
}

// addNames indexes every name of the models this snapshot holds: a public one is callable and an internal one is only
// recognized, and each carries its model's status and version basis for resolution.
func (s *Snapshot) addNames(rows []CatalogName) {
	for _, n := range rows {
		m := s.models[ModelID(n.ModelID)]
		s.names[n.Norm] = nameEntry{
			model:    m.ID,
			public:   Visibility(n.Visibility) == VisibilityPublic,
			retired:  m.Status == StatusRetired,
			floating: m.VersionBasis == VersionFloating,
			exact:    m.VersionBasis == VersionFloating || hasDate(n.Norm),
		}
	}
}

// addAliases records the current alias revisions whose target this snapshot holds, so an alias never resolves to a model
// the snapshot cannot route.
func (s *Snapshot) addAliases(rows []ListAliasesForSnapshotRow) {
	for _, a := range rows {
		id := ModelID(a.ModelID)
		if _, ok := s.models[id]; ok {
			s.aliases[a.Name] = id
		}
	}
}

// Model returns a listed or retired model.
func (s *Snapshot) Model(id ModelID) (Model, bool) {
	m, ok := s.models[id]
	return m, ok
}

// ReferencePrice returns the current reference price of a listed model in a currency.
func (s *Snapshot) ReferencePrice(id ModelID, currency string) (ReferencePrice, bool) {
	p, ok := s.prices[priceKey{id, currency}]
	return p, ok
}

// Models returns the listed models in id order.
func (s *Snapshot) Models() iter.Seq[Model] {
	return func(yield func(Model) bool) {
		for _, id := range s.listed {
			if !yield(s.models[id]) {
				return
			}
		}
	}
}

// sortModels orders ids as bytes, the order of the database's uuids.
func sortModels(ids []ModelID) {
	slices.SortFunc(ids, func(a, b ModelID) int { return uuid.UUID(a).Compare(uuid.UUID(b)) })
}
