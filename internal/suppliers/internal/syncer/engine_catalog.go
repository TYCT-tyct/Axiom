package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"axiom/internal/catalog"
	"axiom/internal/suppliers/internal/adapter"
)

func (e *Engine) catalog(ctx context.Context, c *Commit) error {
	page, err := e.mgr.Offer(ctx, c.State.OfferAt)
	if err != nil {
		return err
	}
	c.State.OfferAt = page.Next
	if !page.Done {
		return adapter.Blocked{Until: page.Until}
	}
	prev := make(map[string]GroupSnap, len(c.State.Groups))
	for _, g := range c.State.Groups {
		prev[g.Ref] = g
	}
	plans := map[string]string{}
	var groups []GroupSnap
	for _, g := range page.Items {
		snap, err := group(c, g, prev[g.Ref], plans)
		if err != nil {
			return err
		}
		groups = append(groups, snap)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Ref < groups[j].Ref })
	diffGroups(c, prev, groups)
	diffPlans(c, plans)
	c.State.Groups, c.State.Plans = groups, plans
	c.OfferRead = true
	if err := e.pool(ctx, c); err != nil && !errors.Is(err, adapter.ErrNotPool) {
		return err
	}
	return nil
}

// group is the snapshot of one group the listing read.
func group(c *Commit, g adapter.Group, before GroupSnap, plans map[string]string) (GroupSnap, error) {
	snap := GroupSnap{Ref: g.Ref, Name: g.Name, Ratio: string(g.Ratio), Facts: string(g.Facts)}
	if g.ModelsUnread {
		return keptGroup(c, g, snap, before, plans)
	}
	for _, m := range g.Models {
		reported := make([]int, len(m.Formats))
		for i, f := range m.Formats {
			reported[i] = int(f)
		}
		plan, confirmed, err := plan(c, g, m, ModelSnap{}, plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.UpstreamName, Formats: reported, Without: m.Without, Plan: plan, Confirmed: confirmed})
	}
	sort.Slice(snap.Models, func(i, j int) bool { return snap.Models[i].Name < snap.Models[j].Name })
	snap.KeyEligible = g.NeedsKey || hasPlan(snap.Models)
	return snap, nil
}

// hasPlan reports whether a model of the group can be sold with a known cost. A model without a call format is not sold,
// so its plan does not make the group one to make a key for.
func hasPlan(models []ModelSnap) bool {
	for _, m := range models {
		if m.Plan != "" && len(m.Formats) > 0 {
			return true
		}
	}
	return false
}

// keptGroup is the snapshot of a group whose models were not read: they stay as they were, a confirmed base follows a
// changed ratio, and a plan made of a base that has been cleared since is gone.
func keptGroup(c *Commit, g adapter.Group, snap, before GroupSnap, plans map[string]string) (GroupSnap, error) {
	for _, m := range before.Models {
		plan, confirmed, err := plan(c, g, adapter.Model{UpstreamName: m.Name}, m, plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.Name, Formats: m.Formats, Without: m.Without, Plan: plan, Confirmed: confirmed})
	}
	snap.KeyEligible = g.NeedsKey || hasPlan(snap.Models)
	return snap, nil
}

// plan decides the cost plan of one endpoint and returns its fingerprint, or "" when the cost is unknown, and whether the
// plan is made of a confirmed base. A schedule the supplier published comes first. A model the supplier prices in a way
// no schedule expresses (per call, say) has no plan: a confirmed base never stands in for a price the supplier states.
// Only while it states none does a person's confirmed base, scaled by the group's ratio, price it; a base the ratio
// takes past a unit price leaves this one endpoint unknown. Otherwise the model keeps the plan it had (kept: one not
// read this time), unless that plan was made of a base that has been cleared.
func plan(c *Commit, g adapter.Group, m adapter.Model, kept ModelSnap, plans map[string]string) (string, bool, error) {
	key := PlanKey(g.Ref, m.UpstreamName)
	switch base, confirmed := c.State.Base(BaseKey{GroupRef: g.Ref, Model: m.UpstreamName}); {
	case m.Cost != nil:
		fp, err := put(c, key, *m.Cost, m.Derivation, "published", plans)
		return fp, false, err
	case m.Published:
		return "", false, nil
	case confirmed:
		parsed, err := catalog.ParseSchedule([]byte(base.Base))
		if err != nil {
			return "", false, fmt.Errorf("syncer: the confirmed base of %s: %w", key, err)
		}
		schedule, ok := scaled(parsed, g.Ratio)
		if !ok {
			return "", false, nil
		}
		fp, err := put(c, key, schedule, adapter.ScaledDerivation(parsed, g.Ratio), "confirmed", plans)
		return fp, true, err
	case kept.Plan != "" && !kept.Confirmed:
		plans[key] = kept.Plan
		return kept.Plan, false, nil
	default:
		return "", false, nil
	}
}

// scaled is a confirmed base at the group's ratio; not ok when a price comes out beyond what a unit price may be, which
// leaves that one endpoint's cost unknown rather than the supplier's listing unread.
func scaled(base catalog.Schedule, ratio adapter.Amount) (catalog.Schedule, bool) {
	s, err := adapter.ScaleSchedule(base, ratio)
	return s, err == nil
}

// put makes a plan of the schedule the endpoint's, a new version when it is not the one in force, and returns its
// fingerprint.
func put(c *Commit, key string, schedule catalog.Schedule, derivation adapter.Derivation, basis string, plans map[string]string) (string, error) {
	b, err := schedule.MarshalJSON()
	if err != nil {
		return "", err
	}
	if derivation.Check(schedule) != nil {
		derivation = nil // an explanation that does not multiply out to the price is none; the price stands
	}
	d, err := derivation.Text()
	if err != nil {
		return "", err
	}
	fp := fingerprint(b, d)
	plans[key] = fp
	if c.State.Plans[key] != fp {
		c.Plans = append(c.Plans, Plan{Endpoint: key, Fingerprint: fp, Schedule: string(b), Basis: basis, Derivation: d})
	}
	return fp, nil
}

// fingerprint names a version of a plan: its schedule, and its derivation when it has one, so a plan without factors
// keeps the fingerprint it had before plans carried them, and one whose factors change is a new version of the plan
// even at the same prices.
func fingerprint(schedule []byte, derivation string) string {
	h := sha256.New()
	h.Write(schedule)
	if derivation != "" {
		h.Write([]byte{0})
		h.Write([]byte(derivation))
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func diffGroups(c *Commit, prev map[string]GroupSnap, now []GroupSnap) {
	seen := map[string]bool{}
	for _, g := range now {
		seen[g.Ref] = true
		before, ok := prev[g.Ref]
		if !ok {
			c.Events = append(c.Events, Event{Kind: GroupAdded, Subject: g.Ref, New: g.Name})
		} else {
			if before.Ratio != g.Ratio {
				c.Events = append(c.Events, Event{Kind: RatioChanged, Subject: g.Ref, Old: before.Ratio, New: g.Ratio})
			}
			if before.Facts != g.Facts {
				c.Events = append(c.Events, Event{Kind: GroupChanged, Subject: g.Ref})
			}
		}
		diffModels(c, g, before)
	}
	for _, ref := range slices.Sorted(maps.Keys(prev)) {
		if !seen[ref] {
			c.Events = append(c.Events, Event{Kind: GroupRemoved, Subject: ref})
		}
	}
}

// diffModels sends ModelAdded and ModelRemoved for the models of g against those of before, and ModelChanged for one
// whose formats or capability limits differ.
func diffModels(c *Commit, g, before GroupSnap) {
	had := map[string]ModelSnap{}
	for _, m := range before.Models {
		had[m.Name] = m
	}
	for _, m := range g.Models {
		old, ok := had[m.Name]
		switch {
		case !ok:
			c.Events = append(c.Events, Event{Kind: ModelAdded, Subject: g.Ref + "/" + m.Name})
		case !slices.Equal(old.Formats, m.Formats) || old.Without != m.Without:
			c.Events = append(c.Events, Event{Kind: ModelChanged, Subject: g.Ref + "/" + m.Name})
		}
		delete(had, m.Name)
	}
	for _, name := range slices.Sorted(maps.Keys(had)) {
		c.Events = append(c.Events, Event{Kind: ModelRemoved, Subject: g.Ref + "/" + name})
	}
}

// diffPlans sends PlanChanged for each endpoint whose plan differs from the one before, including one that has lost
// its plan and so its place in routing.
func diffPlans(c *Commit, now map[string]string) {
	for _, key := range slices.Sorted(maps.Keys(now)) {
		if old := c.State.Plans[key]; old != now[key] {
			c.Events = append(c.Events, Event{Kind: PlanChanged, Subject: key, Old: old, New: now[key]})
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.State.Plans)) {
		if _, ok := now[key]; !ok {
			c.Events = append(c.Events, Event{Kind: PlanChanged, Subject: key, Old: c.State.Plans[key]})
		}
	}
}
