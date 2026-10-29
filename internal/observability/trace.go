package observability

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/netip"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/observability/internal/store"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// GetTrace returns one call as the log keeps it, with its attempts in the order they were made and routing's record of
// how the endpoint was chosen. Its request ID is the key the ledger transactions and the supplier charges are read by,
// so a caller reads the rest of the chain from their owners under the same ID (README「追溯」). It is the internal trace
// read: unlike GetOwn it carries the cost, the endpoint and the route, so only an operator's or an administrator's
// handler may call it, with the platform scope. pg exposes no way to tell a transaction's scope, so the function cannot
// refuse an account's; the caller's choice of scope is the access control. A call that has not been written yet, or is
// not in the scope, is pg.ErrNoRows.
func GetTrace(ctx context.Context, tx pg.Tx, id uuid.UUID) (CallRecord, error) {
	q := store.New()
	r, err := q.GetCallTrace(ctx, tx, id.String())
	if err != nil {
		return CallRecord{}, fmt.Errorf("get the trace of %s: %w", id, err)
	}
	call, err := traceCall(r)
	if err != nil {
		return CallRecord{}, fmt.Errorf("get the trace of %s: %w", id, err)
	}
	rows, err := q.ListTraceAttempts(ctx, tx, store.ListTraceAttemptsParams{RequestID: id.String(), At: call.At})
	if err != nil {
		return CallRecord{}, fmt.Errorf("get the attempts of %s: %w", id, err)
	}
	if call.Attempts, err = traceAttempts(rows); err != nil {
		return CallRecord{}, fmt.Errorf("get the trace of %s: %w", id, err)
	}
	return call, nil
}

// traceCall converts a call row back to the record that was written. The columns hold values this package wrote and the
// table checked, so a value that does not convert is a bug in the writer or the schema, not caller input.
func traceCall(r store.GetCallTraceRow) (CallRecord, error) {
	c := CallRecord{
		At: r.At, ClientApp: r.ClientApp, SessionID: r.SessionID, SubagentID: r.SubagentID, Class: r.RequestClass,
		AppReferer: r.AppReferer, AppTitle: r.AppTitle, AppCategories: r.AppCategories,
		Language: r.Language, UsedTools: r.UsedTools, HasImage: r.HasImage, HasFile: r.HasFile, Model: r.Model,
		PriceVersion: r.PriceVersion, Route: []byte(r.Route), PriceNano: r.PriceNano, CostNano: r.CostNano, Free: r.FreeOfCharge,
		Latency:      time.Duration(r.LatencyMs) * time.Millisecond,
		FirstToken:   time.Duration(r.FirstTokenMs) * time.Millisecond,
		FinishReason: r.FinishReason, ErrorKind: r.ErrorKind, ConsentVersion: r.ConsentVersion,
		TrainingAllowed: r.TrainingAllowed,
	}
	for _, p := range []struct {
		dst *uuid.UUID
		s   string
	}{{&c.ID, r.ID}, {&c.Site, r.SiteID}, {&c.Account, r.AccountID}, {&c.Workspace, r.WorkspaceID}, {&c.Key, r.KeyID}} {
		id, err := uuid.Parse(p.s)
		if err != nil {
			return CallRecord{}, err
		}
		*p.dst = id
	}
	var err error
	if c.IP, err = netip.ParseAddr(r.Ip); err != nil {
		return CallRecord{}, err
	}
	if c.EndUser, err = traceEndUser(r.EndUserHash); err != nil {
		return CallRecord{}, err
	}
	if c.Usage, err = traceUsage(r.Usage); err != nil {
		return CallRecord{}, err
	}
	if c.ServiceTier, err = catalog.ParseServiceTier(r.ServiceTier); err != nil {
		return CallRecord{}, fmt.Errorf("service tier: %w", err)
	}
	c.Currency, err = platform.ParseCurrency(r.Currency)
	return c, err
}

// traceEndUser reads the keyed hash back: empty is no end user.
func traceEndUser(s string) (EndUserHash, error) {
	if s == "" {
		return EndUserHash{}, nil
	}
	var h EndUserHash
	if _, err := hex.Decode(h.sum[:], []byte(s)); err != nil {
		return EndUserHash{}, fmt.Errorf("end user hash: %w", err)
	}
	return h, nil
}

// traceUsage maps the stored billing item names back to their items.
func traceUsage(s string) (map[catalog.Item]int64, error) {
	var named map[string]int64
	if err := jsonx.Unmarshal([]byte(s), &named); err != nil {
		return nil, err
	}
	usage := make(map[catalog.Item]int64, len(named))
	for name, n := range named {
		item, err := catalog.ParseItem(name)
		if err != nil {
			return nil, fmt.Errorf("usage: %w", err)
		}
		usage[item] = n
	}
	return usage, nil
}

// traceAttempts converts the call's attempt rows, which come in attempt order, back to the records written with it.
func traceAttempts(rows []store.ListTraceAttemptsRow) ([]AttemptRecord, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	attempts := make([]AttemptRecord, len(rows))
	for i, r := range rows {
		attempts[i] = AttemptRecord{
			Seq:         platform.AttemptSeq(r.AttemptSeq), //nolint:gosec // G115: the table checks attempt_seq between 1 and 3
			SourceClass: r.SourceClass, Egress: r.Egress, Outcome: AttemptOutcome(r.Outcome), ErrorKind: r.ErrorKind,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, FirstToken: time.Duration(r.FirstTokenMs) * time.Millisecond,
		}
		for _, p := range []struct {
			dst *uuid.UUID
			s   string
		}{{&attempts[i].Endpoint, r.EndpointID}, {&attempts[i].Supplier, r.SupplierID}} {
			id, err := uuid.Parse(p.s)
			if err != nil {
				return nil, err
			}
			*p.dst = id
		}
	}
	return attempts, nil
}
