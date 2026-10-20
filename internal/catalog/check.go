package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"axiom/internal/platform/pg"
)

// ModelCheck is the catalog's view of one model of a vendor, for the first-version acceptance check
// (docs/domains/catalog.md「参考价怎么进入目录」). Problems is empty when the model is listed, has a public name and a
// price in each currency it has, the prices cover everything its spec says the upstream may report, and the spec says
// what a request to the model reads of its dialect (dialectProblems).
type ModelCheck struct {
	Slug        string      `json:"slug"`
	Status      Status      `json:"status"`
	PublicNames []string    `json:"public_names"`
	Source      PriceSource `json:"price_source"`
	Currencies  []string    `json:"currencies"`
	Problems    []string    `json:"problems,omitempty"`
}

// Check reads every draft or listed model of the vendors, read-only. It does not know which models a vendor sells now:
// that is on the vendor's page, so a model the catalog lacks is found by comparing with a dataset (Unrecognized).
func (c *Catalog) Check(ctx context.Context, authors []string) ([]ModelCheck, error) {
	var checks []ModelCheck
	err := c.db.ReadTx(ctx, c.scope, func(tx pg.Tx) error {
		rows, err := New().ListModelsOfAuthors(ctx, tx, authors)
		if err != nil {
			return err
		}
		for _, row := range rows {
			m, err := followerOf(ctx, tx, row)
			if err != nil {
				return err
			}
			checks = append(checks, checkModel(m))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catalog check: %w", err)
	}
	return checks, nil
}

func checkModel(m syncModel) ModelCheck {
	check := ModelCheck{Slug: m.row.Slug, Status: Status(m.row.Status), Source: PriceSource(m.row.PriceSource), Currencies: m.heads.priced()}
	for _, n := range m.names {
		if Visibility(n.Visibility) == VisibilityPublic {
			check.PublicNames = append(check.PublicNames, n.Name)
		}
	}
	slices.Sort(check.PublicNames)
	problem := func(format string, args ...any) {
		check.Problems = append(check.Problems, fmt.Sprintf(format, args...))
	}
	if check.Status != StatusListed {
		problem("it is %s, not listed", check.Status)
	}
	if len(check.PublicNames) == 0 {
		problem("it has no public name")
	}
	// A spec that does not parse is this model's problem alone: the check goes on to the next model, so one run lists
	// every row to fix. The snapshot, which cannot be built from it, fails to load (readSnapshot).
	spec, err := parseSpec(m.row.Spec)
	if err != nil {
		problem("its spec does not parse: %v", err)
		return check
	}
	if len(check.Currencies) == 0 {
		problem("it has no current price")
	}
	if missing := dialectProblems(m.row.Author, spec); len(missing) > 0 {
		problem("its spec does not say %s, which a request to the model needs answered", strings.Join(missing, ", "))
	}
	if !spec.declared {
		problem("its spec does not declare the items and service tiers the upstream may report")
	}
	for _, currency := range check.Currencies {
		if err := covers(currency, *m.heads[currency].schedule, spec.reportable); err != nil {
			problem("%v", err)
		}
	}
	return check
}

// dialectProblems are the dialect keys a request to the model reads that its spec leaves out, which leaves a request
// that needs the answer refused (internal/protocol/README.md「跨格式转换」). The keys asked for depend on the format
// the model is sent in, which is a vendor's: Anthropic's models are sent Messages requests converted from Chat ones,
// OpenAI's Responses requests. A vendor's author is a column of the model, not a guess from its name. The models of
// the other vendors are not asked for any until they are served by a Messages or Responses endpoint.
func dialectProblems(author string, s spec) (missing []string) {
	d := s.dialect
	need := func(key string, unset bool) {
		if unset {
			missing = append(missing, key)
		}
	}
	reasons := s.serving.Abilities.Has(AbilityReasoning)
	switch author {
	case "anthropic":
		need("thinking", reasons && d.Thinking == ThinkingUnset)
		need("default_sampling_only", d.DefaultSamplingOnly == ClaimUnset)
		need("forced_tool_choice", d.ForcedToolChoice == ClaimUnset)
		need("mid_system", d.MidSystem == ClaimUnset)
		need("untrusted_system_after_tools", d.MidSystem == ClaimYes && d.UntrustedSystemAfterTools == ClaimUnset)
	case "openai":
		need("encrypted_reasoning", reasons && d.EncryptedReasoning == ClaimUnset)
		need("cache_breakpoints", d.CacheBreakpoints == ClaimUnset)
	}
	return missing
}

// Unrecognized lists the text models of the vendors in an OpenRouter list that no name of the catalog stands for, listed
// or retired: the models the vendors sell that the catalog lacks, as far as OpenRouter lists them. It uses the
// snapshot, so the catalog must have been refreshed.
func (c *Catalog) Unrecognized(entries []Entry, authors []string) ([]string, error) {
	snap, err := c.Current()
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, e := range entries {
		author, _, _ := strings.Cut(e.Key, "/")
		if !e.Text || !slices.Contains(authors, author) {
			continue
		}
		if !slices.ContainsFunc(e.Names, func(name string) bool { r, ok := snap.Recognize(name); return ok && r.Exact }) {
			missing = append(missing, e.Key)
		}
	}
	slices.Sort(missing)
	return missing, nil
}
