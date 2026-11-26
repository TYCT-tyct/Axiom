package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// ImportReport is what an import of a dataset did: drafts added, entries the catalog already knew, and entries it could
// not use as they are, with the first of their keys for a person to add by hand.
type ImportReport struct {
	Source    PriceSource
	Added     int
	Known     int
	Held      int
	HeldNames []string
}

// maxHeldNames is how many held entries a report names, the contract's bound.
const maxHeldNames = 200

// standardID is what a model's standard ID may be: the slug's form without a version (the column's CHECK).
var standardID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}/[a-z0-9][a-z0-9._-]{0,99}(:free)?$`)

// Import adds a draft model, in tx, for each entry of the fetched dataset that no name of the catalog stands for
// (RecognizeIn, drafts included): its standard ID and the entry's names, a family guessed from its name, the dataset as
// its price source and the entry's price as its first reference price. Each draft is made by the catalog's own
// operations (CreateModel, AddName, PublishPrice), each recorded and audited as actor with reason, so a draft is what a
// person would have made by hand and is reviewed before it is listed.
//
// An entry is held, and nothing of it is written, for what the dataset sync holds an entry for (an unusable price, a
// threshold whose basis only a current price can give, a price that is not sane or that the node's file-parsing switch
// refuses), and for what a draft needs that the entry lacks: an author the catalog has, a standard ID of the slug's
// form, names the catalog can hold. An entry whose writes are refused is undone to a savepoint and held, so one entry
// never fails the import. Names are matched normalized, so a second run finds the drafts of the first and adds only what
// is still missing.
func (c *Catalog) Import(ctx context.Context, tx pg.Tx, actor audit.Actor, reason string, f Fetched) (ImportReport, error) {
	entries, err := parseDataset(f.Source, f.Body)
	if err != nil {
		return ImportReport{}, err
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	authors, err := New().ListAuthors(ctx, tx)
	if err != nil {
		return ImportReport{}, fmt.Errorf("import: read the authors: %w", err)
	}
	im := importer{c: c, tx: tx, actor: actor, reason: reason, f: f, authors: map[string]bool{}, taken: map[string]bool{}}
	for _, a := range authors {
		im.authors[a.Slug] = true
	}
	var names []string
	for i := range entries {
		names = append(names, im.namesOf(&entries[i])...)
	}
	known, err := RecognizeIn(ctx, tx, names)
	if err != nil {
		return ImportReport{}, err
	}
	for name := range known {
		im.taken[Normalize(name)] = true
	}
	sum := sha256.Sum256(f.Body)
	im.bodySum = hex.EncodeToString(sum[:])
	report := ImportReport{Source: f.Source}
	for i := range entries {
		e := &entries[i]
		added, err := im.entry(ctx, e)
		switch {
		case errors.Is(err, errKnown):
			report.Known++
		case errors.As(err, new(heldEntry)):
			report.Held++
			if len(report.HeldNames) < maxHeldNames {
				report.HeldNames = append(report.HeldNames, e.Key)
			}
		case err != nil:
			return ImportReport{}, fmt.Errorf("import %s: %w", e.Key, err)
		case added:
			report.Added++
		}
	}
	return report, nil
}

// importer is one import's state: what the catalog has, and the norms the import has taken so far, so two entries of
// one dataset that name the same model add it once.
type importer struct {
	c       *Catalog
	tx      pg.Tx
	actor   audit.Actor
	reason  string
	f       Fetched
	bodySum string
	authors map[string]bool
	taken   map[string]bool
}

// errKnown is an entry one of whose names the catalog already has.
var errKnown = errors.New("the catalog knows the entry")

// heldEntry is why an entry is not added.
type heldEntry struct{ why error }

func (h heldEntry) Error() string { return h.why.Error() }

// standardOf is the standard ID an entry's model would have: OpenRouter's id is one; LiteLLM's name is the vendor's,
// under its author.
func (im *importer) standardOf(e *Entry) string {
	if im.f.Source == SourceOpenRouter {
		return e.Key
	}
	return e.Author + "/" + strings.ToLower(e.Names[0])
}

// namesOf are the names an entry's model would have, its standard ID first, each once.
func (im *importer) namesOf(e *Entry) []string {
	names := []string{im.standardOf(e)}
	for _, n := range e.Names {
		if !slices.ContainsFunc(names, func(had string) bool { return Normalize(had) == Normalize(n) }) {
			names = append(names, n)
		}
	}
	return names
}

// entry adds the draft of one entry, or says why not.
func (im *importer) entry(ctx context.Context, e *Entry) (bool, error) {
	names := im.namesOf(e)
	if slices.ContainsFunc(names, func(n string) bool { return im.taken[Normalize(n)] }) {
		return false, errKnown
	}
	next, err := im.usable(e, names[0])
	if err != nil {
		return false, heldEntry{err}
	}
	if _, err := im.tx.Exec(ctx, "SAVEPOINT import_entry"); err != nil {
		return false, err
	}
	err = im.draft(ctx, e, names, next)
	if refused(err) {
		if _, err := im.tx.Exec(ctx, "ROLLBACK TO SAVEPOINT import_entry"); err != nil {
			return false, err
		}
		return false, heldEntry{err}
	}
	if err != nil {
		return false, err
	}
	for _, n := range names {
		im.taken[Normalize(n)] = true
	}
	_, err = im.tx.Exec(ctx, "RELEASE SAVEPOINT import_entry")
	return err == nil, err
}

// usable is the entry's price as a draft's first one, or why the entry cannot be imported as it is. A draft has no
// current price, so an entry with context tiers is held, as the sync holds it, until a person publishes the basis.
func (im *importer) usable(e *Entry, standard string) (newPrice, error) {
	if !im.authors[e.Author] {
		return newPrice{}, fmt.Errorf("%w: %q", ErrAuthorMissing, e.Author)
	}
	if !standardID.MatchString(standard) {
		return newPrice{}, fmt.Errorf("%q is not a standard ID", standard)
	}
	s, err := e.schedule(nil)
	if err != nil {
		return newPrice{}, err
	}
	if err := errors.Join(im.c.safe(nil, s), im.c.AllowSchedule(s)); err != nil {
		return newPrice{}, err
	}
	return parsePrice(mustJSON(s))
}

// draft makes the model, gives it its names and publishes its price, each an operation of its own in the import's
// transaction. The standard ID and a vendor's own name in LiteLLM are public; OpenRouter's other names, its dated
// canonical slugs, are internal (docs/domains/catalog.md「模型与名字」), and so is a -latest name, which is never public.
func (im *importer) draft(ctx context.Context, e *Entry, names []string, price newPrice) error {
	basis := VersionFloating
	if slices.ContainsFunc(names, func(n string) bool { return hasDate(Normalize(n)) }) {
		basis = VersionDated
	}
	created, err := im.c.CreateModel(ctx, CreateModelRequest{
		Op: im.op(), Slug: names[0], VersionBasis: basis, PriceSource: im.f.Source, Spec: jsonx.Raw("{}"), Family: familyOf(names[0]),
	})
	if err != nil {
		return err
	}
	id, err := uuid.Parse(created.Subject)
	if err != nil {
		return err
	}
	for i, n := range names {
		visibility := VisibilityPublic
		if i > 0 && (im.f.Source == SourceOpenRouter || strings.HasSuffix(Normalize(n), "-latest")) {
			visibility = VisibilityInternal
		}
		if _, err := im.c.AddName(ctx, AddNameRequest{Op: im.op(), Model: ModelID(id), Name: n, Visibility: visibility}); err != nil {
			return err
		}
	}
	_, err = im.c.PublishPrice(ctx, PublishPriceRequest{
		Op: im.op(), Model: ModelID(id), Source: im.f.Source, Schedule: price.canonical, Evidence: datasetEvidence(im.f, im.bodySum, e),
	})
	return err
}

// op is a new operation of the import: the import's own idempotency claim answers a retry, so each write takes a key of
// its own, as a sync's publish does.
func (im *importer) op() Op {
	return Op{Actor: im.actor, Key: uuid.NewV7(), Reason: im.reason, Tx: &im.tx}
}

// refused reports whether err is the catalog refusing an entry's writes, rather than the database failing: then the
// entry is held and the import goes on.
func refused(err error) bool {
	if _, duplicate := pg.Duplicate(err); duplicate {
		return true
	}
	for _, refusal := range []error{ErrInvalid, ErrNameTaken, ErrNameLength, ErrAuthorMissing, ErrIncomplete, ErrPriceLost, ErrFileParse} {
		if errors.Is(err, refusal) {
			return true
		}
	}
	return false
}
