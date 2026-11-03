package payments

import (
	"context"
	"errors"
	"time"
	"uuid"

	"axiom/internal/platform/alert"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// ErrInvestigated refuses replacing the first operator's conclusion with another note.
var ErrInvestigated = errors.New("payments: this discrepancy was already investigated")

// Discrepancy is a persisted payment fact. Resolved means investigated, without a financial resolution.
type Discrepancy struct {
	ID             uuid.UUID      `json:"id"`
	Site           uuid.UUID      `json:"site_id"`
	Account        *uuid.UUID     `json:"account_id,omitempty"`
	Order          *uuid.UUID     `json:"order_id,omitempty"`
	Merchant       uuid.UUID      `json:"merchant_id"`
	Kind           string         `json:"kind"`
	Detail         map[string]any `json:"detail"`
	State          string         `json:"state"`
	CreatedAt      time.Time      `json:"created_at"`
	InvestigatedBy *string        `json:"investigated_by,omitempty"`
	InvestigatedAt *time.Time     `json:"investigated_at,omitempty"`
	Note           *string        `json:"note,omitempty"`
}

func discrepancyRecord(r PaymentsDiscrepancy) (Discrepancy, error) {
	d := Discrepancy{ID: r.ID, Site: r.SiteID, Account: r.AccountID, Order: r.OrderID, Merchant: r.MerchantID, Kind: r.Kind, State: r.State, CreatedAt: r.CreatedAt, InvestigatedBy: r.InvestigatedBy, Note: r.Note}
	if r.InvestigatedAt.Valid {
		d.InvestigatedAt = &r.InvestigatedAt.Time
	}
	var fields map[string]jsonx.Raw
	if err := jsonx.Unmarshal(r.Detail, &fields); err != nil {
		return d, err
	}
	d.Detail = make(map[string]any, len(fields))
	for name, raw := range fields {
		// Older receipt rows stored minor units as JSON integers; never round them through float64 on the admin path.
		if name == "amount" {
			d.Detail[name] = string(raw)
			continue
		}
		var value any
		if err := jsonx.Unmarshal(raw, &value); err != nil {
			return d, err
		}
		d.Detail[name] = value
	}
	return d, nil
}

// ListDiscrepancies returns at most 100 records, newest first, within the caller's authorized scope.
func ListDiscrepancies(ctx context.Context, tx pg.Tx, before uuid.UUID, state string) ([]Discrepancy, error) {
	rows, err := queries.ListDiscrepancies(ctx, tx, ListDiscrepanciesParams{Before: before, State: state})
	if err != nil {
		return nil, err
	}
	out := make([]Discrepancy, 0, len(rows))
	for _, r := range rows {
		d, err := discrepancyRecord(r)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// GetDiscrepancy reads a visible record; invisible and absent records both return pg.ErrNoRows.
func GetDiscrepancy(ctx context.Context, tx pg.Tx, id uuid.UUID) (Discrepancy, error) {
	r, err := queries.GetDiscrepancy(ctx, tx, id)
	if err != nil {
		return Discrepancy{}, err
	}
	return discrepancyRecord(r)
}

// InvestigateDiscrepancy records a note and audit together. It never writes an order, refund or balance.
func InvestigateDiscrepancy(ctx context.Context, tx pg.Tx, id uuid.UUID, note string, actor audit.Actor) (Discrepancy, error) {
	who := actor.String()
	r, err := queries.InvestigateDiscrepancy(ctx, tx, InvestigateDiscrepancyParams{ID: id, Note: &note, Actor: &who})
	if errors.Is(err, pg.ErrNoRows) {
		if _, readErr := queries.GetDiscrepancy(ctx, tx, id); readErr != nil {
			return Discrepancy{}, readErr
		}
		return Discrepancy{}, ErrInvestigated
	}
	if err != nil {
		return Discrepancy{}, err
	}
	if err = audit.Record(ctx, tx, audit.Event{Actor: actor, Action: "discrepancy.investigate", Target: audit.Target{Module: "payments", Kind: "discrepancy", ID: id.String(), Site: r.SiteID}, Reason: note, Detail: map[string]any{}}); err != nil {
		return Discrepancy{}, err
	}
	if err = alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "payments.discrepancy", Key: id.String(), Severity: "critical", Resolved: true}); err != nil {
		return Discrepancy{}, err
	}
	return discrepancyRecord(r)
}

// insertPending records the discrepancy without its signal. A dispute records the difference first and persists the
// signal after it has suspended the account, so the transaction takes no global alert lock before its row locks.
func insertPending(ctx context.Context, tx pg.Tx, r RecordDiscrepancyParams) (bool, error) {
	n, err := queries.RecordDiscrepancy(ctx, tx, r)
	return n > 0, err
}

// persistPending publishes the signal of a discrepancy insertPending recorded.
func persistPending(ctx context.Context, tx pg.Tx, r RecordDiscrepancyParams) error {
	return alert.Persist(ctx, tx, uuid.NewV7(), alert.Signal{Kind: "payments.discrepancy", Key: r.ID.String(), Severity: "critical", Fields: map[string]string{"merchant": r.MerchantID.String(), "kind": r.Kind, "value": "1"}})
}

func recordPending(ctx context.Context, tx pg.Tx, r RecordDiscrepancyParams) (bool, error) {
	inserted, err := insertPending(ctx, tx, r)
	if err != nil || !inserted {
		return inserted, err
	}
	return true, persistPending(ctx, tx, r)
}
