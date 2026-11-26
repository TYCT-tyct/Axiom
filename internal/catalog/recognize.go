package catalog

import (
	"context"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// Known is a model a name stands for, as the catalog's internal recognition finds it in the tables rather than in a
// snapshot, so a draft is found too.
type Known struct {
	Model  ModelID
	Slug   string
	Status Status
	Family string
	// Exact is set when the name itself is evidence of the version (Recognition.Exact).
	Exact bool
}

// RecognizeIn is Snapshot.Recognize over every model in tx, drafts included: the same normalized match on public and
// internal names, aliases left out, and the same rule for whether the name is evidence of the version. A snapshot holds
// no draft, and what a supplier's names are sorted into (a name of a model a person has not listed yet) needs them. The
// result is by the name asked for; a name the catalog does not know is not in it.
func RecognizeIn(ctx context.Context, tx pg.Tx, names []string) (map[string]Known, error) {
	byNorm := make(map[string][]string, len(names))
	for _, name := range names {
		if len(name) <= maxNameLen {
			norm := Normalize(name)
			byNorm[norm] = append(byNorm[norm], name)
		}
	}
	norms := make([]string, 0, len(byNorm))
	for norm := range byNorm {
		norms = append(norms, norm)
	}
	rows, err := New().RecognizeNorms(ctx, tx, norms)
	if err != nil {
		return nil, fmt.Errorf("recognize names: %w", err)
	}
	known := make(map[string]Known, len(rows))
	for _, r := range rows {
		k := Known{
			Model: ModelID(r.ID), Slug: r.Slug, Status: Status(r.Status), Family: deref(r.Family),
			Exact: versionEvidence(VersionBasis(r.VersionBasis), r.Norm),
		}
		for _, name := range byNorm[r.Norm] {
			known[name] = k
		}
	}
	return known, nil
}

// ModelsIn are the models of ids in tx, drafts included, as RecognizeIn gives them: Exact is not set, since no name was
// asked for. An ID that is no model's is not in the result.
func ModelsIn(ctx context.Context, tx pg.Tx, ids []ModelID) (map[ModelID]Known, error) {
	raw := make([]uuid.UUID, len(ids))
	for i, id := range ids {
		raw[i] = uuid.UUID(id)
	}
	rows, err := New().ModelsByID(ctx, tx, raw)
	if err != nil {
		return nil, fmt.Errorf("read models: %w", err)
	}
	models := make(map[ModelID]Known, len(rows))
	for _, r := range rows {
		models[ModelID(r.ID)] = Known{Model: ModelID(r.ID), Slug: r.Slug, Status: Status(r.Status), Family: deref(r.Family)}
	}
	return models, nil
}

// versionEvidence reports whether a name, normalized, says which version of its model it is: every name of a floating
// model does, since the model is the name; a fixed version's name does only with a date, which a vendor never moves.
func versionEvidence(basis VersionBasis, norm string) bool {
	return basis == VersionFloating || hasDate(norm)
}

// Undated is the name, normalized, without the date it ends in, and whether it had one (hasDate): the name of the model
// a dated snapshot is a version of, as far as the name tells.
func Undated(name string) (string, bool) {
	norm := Normalize(name)
	if !hasDate(norm) {
		return norm, false
	}
	n := len(norm)
	switch {
	case n >= 9 && norm[n-9] == '-' && digits(norm[n-8:]):
		return norm[:n-9], true
	case n >= 5 && norm[n-5] == '-' && digits(norm[n-4:]):
		return norm[:n-5], true
	}
	return norm[:n-6], true // -MM-DD
}
