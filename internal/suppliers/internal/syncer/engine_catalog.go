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
	"axiom/internal/protocol"
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
		snap, err := group(c, g, prev[g.Ref], plans, e.set.DirectFormats)
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

// group is the snapshot of one group the listing read. Unless direct is set, a model keeps only the formats a pre-P18
// node can name, and a model the gate leaves with none is not listed: the formats column needs at least one.
func group(c *Commit, g adapter.Group, before GroupSnap, plans map[string]string, direct bool) (GroupSnap, error) {
	snap := GroupSnap{Ref: g.Ref, Name: g.Name, Ratio: string(g.Ratio), Facts: string(g.Facts)}
	if g.ModelsUnread {
		return keptGroup(c, g, snap, before, plans, direct)
	}
	for _, m := range g.Models {
		reported := make([]int, len(m.Formats))
		for i, f := range m.Formats {
			reported[i] = int(f)
		}
		formats, held := storable(reported, direct)
		if held {
			continue
		}
		plan, err := plan(c, g, m, "", plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.UpstreamName, Formats: formats, Without: m.Without, Plan: plan})
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

// keptGroup is the snapshot of a group whose models were not read: they stay as they were, behind the same gate, and
// only a confirmed base follows a changed ratio.
func keptGroup(c *Commit, g adapter.Group, snap, before GroupSnap, plans map[string]string, direct bool) (GroupSnap, error) {
	for _, m := range before.Models {
		formats, held := storable(m.Formats, direct)
		if held {
			continue
		}
		plan, err := plan(c, g, adapter.Model{UpstreamName: m.Name}, m.Plan, plans)
		if err != nil {
			return snap, err
		}
		snap.Models = append(snap.Models, ModelSnap{Name: m.Name, Formats: formats, Without: m.Without, Plan: plan})
	}
	snap.KeyEligible = g.NeedsKey || hasPlan(snap.Models)
	return snap, nil
}

// preP18Formats are the formats a node released before P18 can name. It names what that node knows rather than what is
// new, so a format added later is held back too until the gate opens.
var preP18Formats = []int{int(protocol.Messages), int(protocol.Responses), int(protocol.Chat)}

// storable is the formats the state may keep: all of them once the gate is open, else only those of preP18Formats.
// held reports a model the gate leaves with none, which is then not listed until it opens.
func storable(formats []int, direct bool) (kept []int, held bool) {
	if direct {
		return formats, false
	}
	kept = slices.DeleteFunc(slices.Clone(formats), func(f int) bool { return !slices.Contains(preP18Formats, f) })
	return kept, len(kept) == 0 && len(formats) > 0
}

// plan decides the cost plan of one endpoint and returns its fingerprint, or "" when the cost is unknown: a schedule
// the supplier published, else a person's confirmed base scaled by the group's ratio, else nothing. kept is the
// fingerprint to keep when nothing new can be worked out (a model that was not read this time).
func plan(c *Commit, g adapter.Group, m adapter.Model, kept string, plans map[string]string) (string, error) {
	key := PlanKey(g.Ref, m.UpstreamName)
	var schedule catalog.Schedule
	var basis string
	switch base, confirmed := c.State.Confirmed[m.UpstreamName]; {
	case m.Cost != nil:
		schedule, basis = *m.Cost, "published"
	case confirmed:
		parsed, err := catalog.ParseSchedule([]byte(base))
		if err != nil {
			return "", fmt.Errorf("syncer: the confirmed base of %s: %w", m.UpstreamName, err)
		}
		if schedule, err = adapter.ScaleSchedule(parsed, g.Ratio); err != nil {
			return "", err
		}
		basis = "confirmed"
	default:
		if kept != "" {
			plans[key] = kept
		}
		return kept, nil
	}
	b, err := schedule.MarshalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	fp := hex.EncodeToString(sum[:8])
	plans[key] = fp
	if c.State.Plans[key] != fp {
		c.Plans = append(c.Plans, Plan{Endpoint: key, Fingerprint: fp, Schedule: string(b), Basis: basis})
	}
	return fp, nil
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
