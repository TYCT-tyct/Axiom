package suppliers

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/store"
)

// ChargeEvent is one revision of a supplier's charge, attributed to an attempt of ours: what billing needs to correct
// an estimated charge from the supplier's own usage. The cost amounts and verdict are for cost reconciliation; the
// counts here are the only thing the user's charge is corrected with.
type ChargeEvent struct {
	ID         uuid.UUID
	ChargeID   uuid.UUID
	Revision   int32
	At         time.Time
	Request    uuid.UUID
	AttemptSeq platform.AttemptSeq
	Test       bool
	CostFlag   string // the original attempt's completeness; informational, not the later record's
	Usage      catalog.Usage
	Reported   catalog.ItemSet
}

// chargeEventPayload is the part of the stored event payload billing reads. The rest describes cost, which does not
// change the user's sale price.
type chargeEventPayload struct {
	RequestID  string `json:"request_id"`
	AttemptSeq int16  `json:"attempt_seq"`
	CostFlag   string `json:"cost_flag"`
	Test       bool   `json:"test"`
}

// chargeUsageDoc is the stored form of a charge's usage (reconcile.usageDoc): the counts, the items the supplier
// stated, the usage with no item, and the tier.
type chargeUsageDoc struct {
	Items    map[string]int64 `json:"items"`
	Reported []string         `json:"reported"`
	Unknown  []struct {
		Field string `json:"field"`
		Value string `json:"value"`
	} `json:"unknown"`
	Tier          string `json:"tier"`
	PhysicalInput *int64 `json:"physical_input"`
}

// ChargeEvents reads the charge events after (afterAt, afterID), oldest first, at most max. Only events attributed to
// an attempt are returned: an unattributed or lost-request charge has no user settlement to correct.
func ChargeEvents(ctx context.Context, tx pg.Tx, afterAt time.Time, afterID uuid.UUID, max int32) ([]ChargeEvent, error) {
	q := store.New()
	if err := q.LockChargeEvents(ctx, tx); err != nil {
		return nil, fmt.Errorf("lock charge events: %w", err)
	}
	rows, err := q.ChargeEvents(ctx, tx, store.ChargeEventsParams{AfterAt: afterAt, AfterID: afterID, Max: max})
	if err != nil {
		return nil, fmt.Errorf("read charge events: %w", err)
	}
	events := make([]ChargeEvent, 0, len(rows))
	for _, r := range rows {
		e, err := chargeEvent(r.ID, r.ChargeRecordID, r.Revision, r.CreatedAt, r.Payload, r.Usage, r.AttemptRequestID, r.AttemptSeq)
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// ChargeEventByID reads one event, for a correction that was not ready when it was first read.
func ChargeEventByID(ctx context.Context, tx pg.Tx, id uuid.UUID) (ChargeEvent, error) {
	r, err := store.New().GetChargeEvent(ctx, tx, id)
	if err != nil {
		return ChargeEvent{}, fmt.Errorf("read charge event %s: %w", id, err)
	}
	return chargeEvent(r.ID, r.ChargeRecordID, r.Revision, r.CreatedAt, r.Payload, r.Usage, r.AttemptRequestID, r.AttemptSeq)
}

func chargeEvent(id, charge uuid.UUID, revision int32, at time.Time, payload, usage []byte, request *uuid.UUID, seq int16) (ChargeEvent, error) {
	if request == nil {
		return ChargeEvent{}, fmt.Errorf("charge event %s: no attempt", id)
	}
	var p chargeEventPayload
	if err := jsonx.Unmarshal(payload, &p); err != nil {
		return ChargeEvent{}, fmt.Errorf("charge event %s: payload: %w", id, err)
	}
	u, reported, err := parseChargeUsage(usage)
	if err != nil {
		return ChargeEvent{}, fmt.Errorf("charge event %s: %w", id, err)
	}
	attempt := seq // the payload may be older than a reattribution; the charge record is the authority
	if attempt == 0 {
		attempt = p.AttemptSeq
	}
	return ChargeEvent{
		ID: id, ChargeID: charge, Revision: revision, At: at, Request: *request,
		// The charge record's attempt_seq references attempt_costs' 1 to 3.
		AttemptSeq: platform.AttemptSeq(attempt), //nolint:gosec // G115: bounded by that reference
		Test:       p.Test, CostFlag: p.CostFlag, Usage: u, Reported: reported,
	}, nil
}

// parseChargeUsage reverses reconcile.usageDoc. An item in Reported may be stated as zero; the rest were counted but
// not stated, and may not correct an estimate on their own.
func parseChargeUsage(body []byte) (catalog.Usage, catalog.ItemSet, error) {
	var doc chargeUsageDoc
	if err := jsonx.Unmarshal(body, &doc); err != nil {
		return catalog.Usage{}, 0, fmt.Errorf("usage: %w", err)
	}
	counts := make(map[catalog.Item]int64, len(doc.Items))
	for name, n := range doc.Items {
		it, err := catalog.ParseItem(name)
		if err != nil {
			return catalog.Usage{}, 0, fmt.Errorf("usage item %q: %w", name, err)
		}
		counts[it] = n
	}
	var reported catalog.ItemSet
	for _, name := range doc.Reported {
		it, err := catalog.ParseItem(name)
		if err != nil {
			return catalog.Usage{}, 0, fmt.Errorf("reported item %q: %w", name, err)
		}
		reported.Add(it)
	}
	tier, err := catalog.ParseServiceTier(doc.Tier)
	if err != nil {
		return catalog.Usage{}, 0, fmt.Errorf("usage tier %q: %w", doc.Tier, err)
	}
	unknown := make([]catalog.UnknownUsage, len(doc.Unknown))
	for i, k := range doc.Unknown {
		unknown[i] = catalog.UnknownUsage{Field: k.Field, Value: k.Value}
	}
	u, err := catalog.NewUsage(counts, tier, unknown)
	if err != nil {
		return catalog.Usage{}, 0, err
	}
	if doc.PhysicalInput != nil {
		u, err = u.WithPhysicalInput(*doc.PhysicalInput)
		if err != nil {
			return catalog.Usage{}, 0, err
		}
	}
	return u, reported, nil
}
