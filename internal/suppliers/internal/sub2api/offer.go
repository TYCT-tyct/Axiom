package sub2api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// The two paged listings of the offer and the keys (internal/suppliers/README.md「适配器」). Their position is kept by
// the module in the supplier's sync state, so it carries what the pass read and never a key's text: that state holds no
// credentials.

type groupDTO struct {
	ID           number `json:"id"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Status       string `json:"status"`
	Subscription string `json:"subscription_type"`
	Peak         bool   `json:"peak_rate_enabled"`
	Multiplier   number `json:"rate_multiplier"`
}

// facts is the part of a group that decides what can be sold from it; Formats reads it back.
type facts struct {
	Platform     string `json:"platform"`
	Status       string `json:"status"`
	Subscription string `json:"subscription_type"`
	Peak         bool   `json:"peak_rate_enabled"`
}

// offerProgress is the position in the offer listing: the groups and multipliers read when the pass began, the key page
// it has reached, the first active key of each group by its id, and the groups built so far. Not a key's text. A
// position whose groups are no longer the supplier's begins again, or a group removed since would be read for ever, its
// key's list refused.
type offerProgress struct {
	V         int               `json:"v"`
	Groups    []groupDTO        `json:"groups"`
	Overrides map[string]number `json:"overrides"`
	Page      int               `json:"page"` // the key page to read next, 0 once every key is read
	Keys      map[number]number `json:"keys"` // group id -> the id of a key in it
	Next      int               `json:"next"` // the group to read next
	Built     []builtGroup      `json:"built"`
}

// builtGroup is a group of the offer as the pass read it; the formats of its models follow from its facts.
type builtGroup struct {
	Ref, Name string
	Ratio     adapter.Amount
	Facts     adapter.GroupFacts
	Models    []string
	// ModelsUnread is set when no key of the group could list its models: they stay as they were, not empty.
	ModelsUnread bool
}

// decodeOffer reads a position; a position this version does not read begins the listing again.
func decodeOffer(raw adapter.Listing) offerProgress {
	if len(raw) == 0 {
		return offerProgress{}
	}
	var p offerProgress
	if jsonx.UnmarshalKnown(raw, &p) != nil || p.V != 1 || p.Page < 0 || p.Next < 0 || p.Next > len(p.Groups) {
		return offerProgress{}
	}
	return p
}

// Offer reads the groups, their multipliers and, from one of our keys in each group, the models it lists. Prices are
// not read: sub2api shows none to an ordinary user in a default installation, so the cost of a model stays unknown
// until a person confirms a base price (adapter.ScaleSchedule drafts the figure). It is read from the position in from,
// as far as the panel bucket allows; what it read is in the position it answers with.
func (m *manager) Offer(ctx context.Context, from adapter.Listing) (adapter.Page[adapter.Group], error) {
	p, err := m.offerStart(ctx, from)
	if err != nil {
		return adapter.Page[adapter.Group]{}, err
	}
	for {
		page, more, err := m.offerPage(ctx, &p)
		if err != nil || !more {
			return page, err
		}
	}
}

// offerStart is the position a read begins from: the one given, unless the supplier's groups are no longer the ones it
// was read over, and a new pass's where none was given.
func (m *manager) offerStart(ctx context.Context, from adapter.Listing) (offerProgress, error) {
	p := decodeOffer(from)
	if p.V == 0 {
		return m.startOffer(ctx, nil)
	}
	groups, err := m.groupList(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	if !sameGroups(groups, p.Groups) {
		return m.startOffer(ctx, groups)
	}
	return p, nil
}

// offerPage does one step of the positioning: it lists the next page of keys, or reads the models of the next group.
// more is true when the step was done and the pass goes on; otherwise the page answers, either with the whole offer
// (Done) or with the position it reached when the supplier's limits stopped it.
func (m *manager) offerPage(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	if p.Page != 0 {
		return m.keyPageStep(ctx, p)
	}
	if p.Next < len(p.Groups) {
		return m.groupStep(ctx, p)
	}
	return adapter.Page[adapter.Group]{Items: p.offer(), Done: true}, false, nil
}

// keyPageStep reads the next page of keys and notes the first active key of each group it names.
func (m *manager) keyPageStep(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	items, pages, _, err := m.keyPage(ctx, p.Page)
	if err != nil {
		return stopped(err, *p)
	}
	for _, k := range items {
		if _, seen := p.Keys[k.GroupID]; !seen && k.Status == "active" && k.Key != "" {
			p.Keys[k.GroupID] = k.ID
		}
	}
	if p.Page >= pages || len(items) == 0 {
		p.Page, p.Next = 0, 0
		return page, true, nil
	}
	p.Page++
	return page, true, nil
}

// groupStep reads the group the position points at: its own multipliers and models, through the key the pass found for
// it, or unread when it has none.
func (m *manager) groupStep(ctx context.Context, p *offerProgress) (page adapter.Page[adapter.Group], more bool, err error) {
	key, found, err := m.groupKey(ctx, p.Keys[p.Groups[p.Next].ID])
	if err != nil {
		return stopped(err, *p)
	}
	var k *keyDTO
	if found {
		k = &key
	}
	b, err := m.group(ctx, p.Groups[p.Next], p.Overrides, k)
	if err != nil {
		return stopped(err, *p)
	}
	p.Built = append(p.Built, b)
	p.Next++
	return page, true, nil
}

// stopped answers a step whose request the supplier's limits refused with the position the pass reached and when to go
// on; any other error is answered as it is, and the pass ends either way.
func stopped(err error, p offerProgress) (adapter.Page[adapter.Group], bool, error) {
	if blocked, ok := errors.AsType[adapter.Blocked](err); ok {
		return adapter.Page[adapter.Group]{Next: listing(p), Until: blocked.Until}, false, nil
	}
	return adapter.Page[adapter.Group]{}, false, err
}

// startOffer reads what the whole pass works from: the groups (read here when the caller has none) and the multipliers
// that replace theirs.
func (m *manager) startOffer(ctx context.Context, groups []groupDTO) (offerProgress, error) {
	var err error
	if groups == nil {
		if groups, err = m.groupList(ctx); err != nil {
			return offerProgress{}, err
		}
	}
	overrides, err := m.userMultipliers(ctx)
	if err != nil {
		return offerProgress{}, err
	}
	return offerProgress{V: 1, Groups: groups, Overrides: overrides, Page: 1, Keys: map[number]number{}}, nil
}

// offer is the offer of the pass: the groups it built, with the formats their facts allow.
func (p offerProgress) offer() []adapter.Group {
	out := make([]adapter.Group, 0, len(p.Built))
	for _, b := range p.Built {
		g := adapter.Group{Ref: b.Ref, Name: b.Name, Ratio: b.Ratio, Facts: b.Facts, ModelsUnread: b.ModelsUnread}
		if !b.ModelsUnread {
			formats := quirks{}.Formats(b.Facts)
			for _, n := range b.Models {
				g.Models = append(g.Models, adapter.Model{UpstreamName: n, Formats: append([]protocol.Format(nil), formats...)})
			}
		}
		out = append(out, g)
	}
	return out
}

// sameGroups reports whether two readings name the same groups: a pass whose groups changed under its position begins
// again.
func sameGroups(a, b []groupDTO) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func (m *manager) groupList(ctx context.Context) ([]groupDTO, error) {
	resp, err := m.get(ctx, "/api/v1/groups/available", bucketPanel)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, statusError("groups", resp)
	}
	groups, err := open[[]groupDTO](resp)
	if err != nil {
		return nil, err
	}
	if groups == nil {
		return nil, fmt.Errorf("%w: no list of groups", adapter.ErrUnrecognised)
	}
	for _, g := range groups {
		if !g.ID.present() || !g.Multiplier.present() || g.Name == "" {
			return nil, fmt.Errorf("%w: a group without its id, name or multiplier", adapter.ErrUnrecognised)
		}
	}
	return groups, nil
}

// groupKey reads the key a group is listed with: an id the pass took from the key list, whose text the read of the
// group's models and its multiplier needs. found is false when the group has no key, or when it went away since the list
// named it (the list is read before the groups are): the group stays unread for this pass rather than failing the task
// for ever.
func (m *manager) groupKey(ctx context.Context, id number) (key keyDTO, found bool, err error) {
	if !id.present() {
		return key, false, nil
	}
	ref, err := pathID(string(id))
	if err != nil {
		return key, false, err
	}
	resp, err := m.get(ctx, "/api/v1/keys/"+ref, bucketPanel)
	if err != nil {
		return key, false, err
	}
	switch resp.Status {
	case http.StatusNotFound:
		return key, false, nil
	case http.StatusOK:
	default:
		return key, false, statusError("key", resp)
	}
	if key, err = open[keyDTO](resp); err != nil {
		return key, false, err
	}
	if key.Key == "" || string(key.ID) != ref {
		return key, false, fmt.Errorf("%w: a key answer for another key or without text", adapter.ErrUnrecognised)
	}
	return key, true, nil
}

// userMultipliers are the per-user multipliers that replace a group's own, by group id.
func (m *manager) userMultipliers(ctx context.Context) (map[string]number, error) {
	resp, err := m.get(ctx, "/api/v1/groups/rates", bucketPanel)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, statusError("group multipliers", resp)
	}
	rates, err := open[map[string]number](resp)
	if err != nil {
		return nil, err
	}
	if rates == nil {
		return nil, fmt.Errorf("%w: no map of multipliers", adapter.ErrUnrecognised)
	}
	return rates, nil
}

// group reads one group of the offer: its facts and its multiplier, and, when a key of it is given, the multiplier that
// key is billed at and the models it lists.
func (m *manager) group(ctx context.Context, g groupDTO, overrides map[string]number, key *keyDTO) (builtGroup, error) {
	b := builtGroup{Ref: string(g.ID), Name: g.Name, ModelsUnread: true}
	multiplier := g.Multiplier
	if o, ok := overrides[string(g.ID)]; ok {
		multiplier = o
	}
	f, err := jsonx.MarshalCanonical(facts{Platform: g.Platform, Status: g.Status, Subscription: g.Subscription, Peak: g.Peak})
	if err != nil {
		return builtGroup{}, err
	}
	b.Facts = f
	if key != nil {
		// The key's own view is the supplier's final word on the multiplier (it knows the peak hours) and on the models.
		if resolved, ok, err := m.resolvedMultiplier(ctx, key); err != nil {
			return builtGroup{}, err
		} else if ok {
			multiplier = resolved
		}
		names, ok, err := m.modelNames(ctx, key)
		if err != nil {
			return builtGroup{}, err
		}
		if ok {
			b.ModelsUnread, b.Models = false, names
		}
	}
	ratio, err := multiplier.amount()
	if err != nil {
		return builtGroup{}, err
	}
	b.Ratio = ratio
	return b, nil
}
