package catalog

import (
	"errors"
	"iter"
	"slices"
	"strings"
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
	Spec         []byte // the baseline's JSON
	Rev          int64
	reportable   Reportable
}

// Reportable is what the upstream may report for the model, from its spec, for the listing gate.
func (m Model) Reportable() Reportable { return m.reportable }

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

// ErrModelNotFound is what a name that no public name matches gets, the ones with a ':' suffix included; ErrRetired is
// for the name of a retired model.
var (
	ErrModelNotFound = errors.New("the model does not exist")
	ErrRetired       = errors.New("the model is retired")
)

// Snapshot is the catalog as of one revision, built whole and never changed, so a request that holds one sees one
// consistent state (docs/glossary.md「快照」). It holds listed and retired models, their names, and the current
// reference prices of the listed ones.
type Snapshot struct {
	revision int64
	models   map[ModelID]Model
	listed   []ModelID // in id order
	names    map[string]nameEntry
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

// ResolvePublic finds the model a name from a user stands for. A name with a ':' is never found: a suffix such as
// :free belongs to a supplier alias and must not land on the base model. A public name only, so the internal ones
// are not callable. It does not allocate.
func (s *Snapshot) ResolvePublic(name string) (Resolution, error) {
	e, ok := s.lookup(name)
	switch {
	case !ok || !e.public:
		return Resolution{}, ErrModelNotFound
	case e.retired:
		return Resolution{}, ErrRetired
	}
	return Resolution{Model: e.model, Floating: e.floating}, nil
}

// Recognize finds the model a name stands for among public and internal names, retired models' included, so that a
// supplier does not map a retired model's name to another model.
func (s *Snapshot) Recognize(name string) (Recognition, bool) {
	e, ok := s.lookup(name)
	return Recognition{Model: e.model, Exact: e.exact}, ok
}

func (s *Snapshot) lookup(name string) (nameEntry, bool) {
	if len(name) > maxNameLen || strings.Contains(name, ":") {
		return nameEntry{}, false
	}
	var buf [maxNameLen]byte
	e, ok := s.names[string(appendNormalized(buf[:0], name))] // the compiler does not allocate for this lookup
	return e, ok
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
