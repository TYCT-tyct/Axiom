package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
)

// DialectFile is the dialect keys of the models the vendors' documents describe, by the model's slug
// (deploy/catalog/dialects.json). It names every model it covers: no pattern stands for a family, because a model that
// matches by its name and not by its documents is the guess the spec exists to prevent.
type DialectFile struct {
	Models map[string]DialectKeys `json:"models"`
}

// ParseDialectFile reads the file, refusing a member it does not know, a thinking mode the catalog does not know, and a
// model that has no key.
func ParseDialectFile(data []byte) (DialectFile, error) {
	var f DialectFile
	if err := jsonx.UnmarshalKnown(data, &f); err != nil {
		return DialectFile{}, fmt.Errorf("dialect file: %w", err)
	}
	for slug, keys := range f.Models {
		if err := keys.check(); err != nil {
			return DialectFile{}, fmt.Errorf("dialect file, %s: %w", slug, err)
		}
	}
	return f, nil
}

// check is why the keys are not a dialect to write: one the catalog does not know, or none at all.
func (k DialectKeys) check() error {
	if _, err := parseDialect(k); err != nil {
		return err
	}
	members, err := k.members()
	if err == nil && len(members) == 0 {
		err = errors.New("it sets no key")
	}
	return err
}

// members are the keys set, as the spec spells them.
func (k DialectKeys) members() (map[string]jsonx.Raw, error) {
	b, err := jsonx.MarshalCanonical(k)
	if err != nil {
		return nil, err
	}
	var m map[string]jsonx.Raw
	return m, jsonx.Unmarshal(b, &m)
}

// What ApplyDialects did, or would do, to a model.
const (
	DialectSet       = "set"       // keys were written, or would be
	DialectUnchanged = "unchanged" // the spec has every key as the file has it
	DialectAbsent    = "absent"    // the catalog has no model of that slug
	DialectRetired   = "retired"   // a retired model is not changed
	DialectFailed    = "failed"    // the catalog refused the new spec, or it could not be read
)

// DialectResult is what ApplyDialects did to one model of the file. Changes are "key: before -> after" for each key it
// writes, null for a key the spec lacked.
type DialectResult struct {
	Slug    string   `json:"slug"`
	Outcome string   `json:"outcome"`
	Changes []string `json:"changes,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// ApplyDialects writes the file's keys into the specs of the models of the same slugs, leaving every other key of a spec
// as it is, each through UpdateSpec: audited, checked against the rev it read, refused where the catalog refuses the new
// spec. A model the file lists and the catalog lacks is reported, not created. Without apply it writes nothing and
// reports what it would. It is idempotent: a spec that already holds every key is left alone, so a second run changes
// nothing. A model that fails does not stop the others.
func (c *Catalog) ApplyDialects(ctx context.Context, actor audit.Actor, f DialectFile, reason string, apply bool) []DialectResult {
	results := make([]DialectResult, 0, len(f.Models))
	for _, slug := range slices.Sorted(maps.Keys(f.Models)) {
		r := DialectResult{Slug: slug}
		r.Outcome, r.Changes, r.Error = c.applyDialect(ctx, actor, slug, f.Models[slug], reason, apply)
		results = append(results, r)
	}
	return results
}

func (c *Catalog) applyDialect(ctx context.Context, actor audit.Actor, slug string, keys DialectKeys, reason string, apply bool) (outcome string, changes []string, failure string) {
	failed := func(err error) (string, []string, string) { return DialectFailed, nil, err.Error() }
	model, err := c.modelBySlug(ctx, slug)
	switch {
	case errors.Is(err, ErrModelNotFound):
		return DialectAbsent, nil, ""
	case err != nil:
		return failed(err)
	case model.Status == StatusRetired:
		return DialectRetired, nil, ""
	}
	spec, changes, err := mergeDialect(model.Spec, keys)
	switch {
	case err != nil:
		return failed(err)
	case len(changes) == 0:
		return DialectUnchanged, nil, ""
	case !apply:
		return DialectSet, changes, ""
	}
	_, err = c.UpdateSpec(ctx, UpdateSpecRequest{
		Actor: actor, Key: uuid.NewV7(), Reason: reason, Model: model.ID, Rev: model.Rev, Spec: spec,
	})
	if err != nil {
		return failed(err)
	}
	return DialectSet, changes, ""
}

// mergeDialect is the spec with the keys' members set, and the changes that made: "key: before -> after" for each member
// the spec lacked or had with another value. Every other member is carried as it is.
func mergeDialect(spec jsonx.Raw, keys DialectKeys) (merged jsonx.Raw, changes []string, err error) {
	have := map[string]jsonx.Raw{}
	if err := jsonx.Unmarshal(spec, &have); err != nil {
		return nil, nil, fmt.Errorf("its spec is no JSON object: %w", err)
	}
	want, err := keys.members()
	if err != nil {
		return nil, nil, err
	}
	for _, key := range slices.Sorted(maps.Keys(want)) {
		if old, ok := have[key]; !ok || !bytes.Equal(old, want[key]) {
			before := "null"
			if ok {
				before = string(old)
			}
			changes = append(changes, fmt.Sprintf("%s: %s -> %s", key, before, want[key]))
			have[key] = want[key]
		}
	}
	merged, err = jsonx.MarshalCanonical(have)
	return merged, changes, err
}

// modelBySlug reads the model whose slug is exactly slug; ErrModelNotFound when there is none.
func (c *Catalog) modelBySlug(ctx context.Context, slug string) (ModelView, error) {
	// The listing matches by prefix, so the models of which slug is the prefix come with it. A page of 200 holds every one.
	models, _, err := c.Models(ctx, ModelQuery{SlugPrefix: slug, PageSize: 200})
	if err != nil {
		return ModelView{}, err
	}
	for _, m := range models {
		if m.Slug == slug {
			return c.Model(ctx, m.ID)
		}
	}
	return ModelView{}, ErrModelNotFound
}
