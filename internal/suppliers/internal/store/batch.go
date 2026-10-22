package store

import (
	"context"
	"time"
	"uuid"
)

// The statements below write many rows in one round trip with unnest. sqlc cannot type a call of unnest over eleven
// arrays, so they are written by hand; each is tested against the real tables.

// AttemptCostRow is the cost and reconciliation key of one finished attempt.
type AttemptCostRow struct {
	UnitValueID, ExchangeRateID                      *uuid.UUID
	ConversionFrom, ConversionTo, ConversionState    *string
	RequestID                                        uuid.UUID
	AttemptSeq                                       int16
	SiteID, AccountID                                *uuid.UUID
	SupplierID, CredentialID, EndpointID, ScheduleID uuid.UUID
	ChargeRef                                        *string
	Usage                                            []byte
	PricedAt, FinishedAt                             time.Time
	CostFlag                                         string
	EstimatedNative                                  string
	Test                                             bool
}

const insertAttemptCosts = `
INSERT INTO suppliers.attempt_costs (request_id, attempt_seq, site_id, account_id, supplier_id, credential_id, endpoint_id,
    cost_schedule_id, charge_ref, usage, priced_at, finished_at, cost_flag, estimated_native,
    unit_value_id, exchange_rate_id, conversion_from, conversion_to, conversion_state, test)
SELECT * FROM unnest($1::uuid[], $2::smallint[], $3::uuid[], $4::uuid[], $5::uuid[], $6::uuid[], $7::uuid[], $8::uuid[],
    $9::text[], $10::jsonb[], $11::timestamptz[], $12::timestamptz[], $13::text[], $14::numeric[],
    $15::uuid[], $16::uuid[], $17::text[], $18::text[], $19::text[], $20::boolean[])
ON CONFLICT (request_id, attempt_seq) DO NOTHING`

// InsertAttemptCosts writes the attempts of a request in one statement. A row already there stays as it was, so a
// settlement repeated after a crash changes nothing.
func InsertAttemptCosts(ctx context.Context, db DBTX, rows []AttemptCostRow) error {
	var a struct {
		unit, exchange                                []*uuid.UUID
		from, to, conversion                          []*string
		req, supplier, credential, endpoint, schedule []uuid.UUID
		seq                                           []int16
		site, account                                 []*uuid.UUID
		ref                                           []*string
		usage                                         [][]byte
		priced, finished                              []time.Time
		flag, estimated                               []string
		test                                          []bool
	}
	for _, r := range rows {
		a.unit, a.exchange = append(a.unit, r.UnitValueID), append(a.exchange, r.ExchangeRateID)
		a.from, a.to, a.conversion = append(a.from, r.ConversionFrom), append(a.to, r.ConversionTo), append(a.conversion, r.ConversionState)
		a.req, a.seq = append(a.req, r.RequestID), append(a.seq, r.AttemptSeq)
		a.site, a.account = append(a.site, r.SiteID), append(a.account, r.AccountID)
		a.supplier, a.credential = append(a.supplier, r.SupplierID), append(a.credential, r.CredentialID)
		a.endpoint, a.schedule = append(a.endpoint, r.EndpointID), append(a.schedule, r.ScheduleID)
		a.ref, a.usage = append(a.ref, r.ChargeRef), append(a.usage, r.Usage)
		a.priced, a.finished = append(a.priced, r.PricedAt), append(a.finished, r.FinishedAt)
		a.flag, a.estimated = append(a.flag, r.CostFlag), append(a.estimated, r.EstimatedNative)
		a.test = append(a.test, r.Test)
	}
	_, err := db.Exec(ctx, insertAttemptCosts, a.req, a.seq, a.site, a.account, a.supplier, a.credential, a.endpoint,
		a.schedule, a.ref, a.usage, a.priced, a.finished, a.flag, a.estimated, a.unit, a.exchange, a.from, a.to, a.conversion, a.test)
	return err
}

// ChargeRow is one charge record as the scan read it.
type ChargeRow struct {
	KeyRef, ChargeRef string
	OccurredAt        time.Time
	AmountNative      string
	Usage             []byte
	Funding           string
	UpstreamModel     string
}

const insertChargeRecords = `
INSERT INTO suppliers.charge_records (supplier_id, credential_id, charge_ref, occurred_at, amount_native, usage, funding,
    upstream_model)
SELECT $1::uuid, c.id, r.charge_ref, r.occurred_at, r.amount_native, r.usage, r.funding, r.upstream_model
FROM unnest($2::text[], $3::text[], $4::timestamptz[], $5::numeric[], $6::jsonb[], $7::text[], $8::text[])
    AS r(key_ref, charge_ref, occurred_at, amount_native, usage, funding, upstream_model)
LEFT JOIN suppliers.credentials c ON c.supplier_id = $1::uuid AND c.upstream_key_ref = r.key_ref
ON CONFLICT (supplier_id, charge_ref) DO NOTHING`

// InsertChargeRecords stores charge records of a supplier; a record read twice is kept once, as it was first read.
func InsertChargeRecords(ctx context.Context, db DBTX, supplier uuid.UUID, rows []ChargeRow) error {
	var a struct {
		key, ref, funding, model, amount []string
		at                               []time.Time
		usage                            [][]byte
	}
	for _, r := range rows {
		a.key, a.ref = append(a.key, r.KeyRef), append(a.ref, r.ChargeRef)
		a.at, a.amount = append(a.at, r.OccurredAt), append(a.amount, r.AmountNative)
		a.usage, a.funding, a.model = append(a.usage, r.Usage), append(a.funding, r.Funding), append(a.model, r.UpstreamModel)
	}
	_, err := db.Exec(ctx, insertChargeRecords, supplier, a.key, a.ref, a.at, a.amount, a.usage, a.funding, a.model)
	return err
}
