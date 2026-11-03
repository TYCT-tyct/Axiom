package store

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5" //nolint:depguard // execution-mode option only; statements still use the caller's scoped transaction
)

// These handwritten statements batch rows with unnest or paged VALUES; sqlc cannot type
// the wide unnest calls or generate variable-length VALUES. Tests use the real tables.

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

// EndedRequestRow is one request's end: the database time it ended at and the tenant that owns it.
type EndedRequestRow struct {
	RequestID, SiteID, AccountID uuid.UUID
	EndedAt                      time.Time
}

const insertEndedRequests = `
INSERT INTO suppliers.ended_requests (request_id, site_id, account_id, ended_at)
SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::uuid[], $4::timestamptz[])
ON CONFLICT (request_id) DO NOTHING`

// InsertEndedRequests writes a batch's request ends in one statement. A request already ended stays at its first time,
// so a settlement repeated after a crash changes nothing.
func InsertEndedRequests(ctx context.Context, db DBTX, rows []EndedRequestRow) error {
	request, site, account, at := make([]uuid.UUID, len(rows)), make([]uuid.UUID, len(rows)), make([]uuid.UUID, len(rows)), make([]time.Time, len(rows))
	for i, r := range rows {
		request[i], site[i], account[i], at[i] = r.RequestID, r.SiteID, r.AccountID, r.EndedAt
	}
	_, err := db.Exec(ctx, insertEndedRequests, request, site, account, at)
	return err
}

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

// ProjectionPageSize bounds statement parameters and payloads for large listings.
const ProjectionPageSize = 1000

// ProjectionWrites contains only differences selected by the projector. Apply keeps dependency order;
// the caller owns the transaction, so a failed page rolls back the entire listing.
type ProjectionWrites struct {
	Groups           []InsertProjectedGroupParams
	ChangedGroups    []UpdateProjectedGroupParams
	AbsentGroups     []uuid.UUID
	Pool             []SetProjectedGroupPoolParams
	ClearedPool      []uuid.UUID
	Models           []UpsertProjectedModelParams
	AbsentModels     []SetProjectedModelAbsentParams
	Endpoints        []InsertProjectedEndpointParams
	ChangedEndpoints []UpdateProjectedEndpointParams
	Costs            []EnsureCostScheduleParams
	UnknownCosts     []UnsetProjectedEndpointCostParams
	// Eligibility is the key_eligible flag of groups the listing already had. A new group carries it in its insert.
	Eligibility []SetGroupEligibilityParams
	// AutoSelected are the groups no person has chosen that the projection puts in use, with the codename it gives them.
	AutoSelected []AutoSelectGroupParams
}

// SetGroupEligibilityParams sets whether a key of ours can serve a group.
type SetGroupEligibilityParams struct {
	ID       uuid.UUID
	Eligible bool
}

// AutoSelectGroupParams puts a never-chosen group in use and gives it the codename it keeps.
type AutoSelectGroupParams struct {
	ID       uuid.UUID
	Codename string
}

// Apply writes nonempty pages in dependency order, inside the caller's transaction.
func (w *ProjectionWrites) Apply(ctx context.Context, db DBTX) error {
	if err := w.applyNames(ctx, db); err != nil {
		return err
	}
	return w.applyEndpoints(ctx, db)
}

func (w *ProjectionWrites) applyNames(ctx context.Context, db DBTX) error {
	if err := projectionPages(ctx, db, w.Groups, `
INSERT INTO suppliers.supplier_groups (id, supplier_id, upstream_ref, name, ratio, facts, key_eligible, last_seen_at)
VALUES `, ``, []string{"uuid", "uuid", "text", "text", "numeric", "jsonb", "boolean", "timestamptz"}, func(r InsertProjectedGroupParams) []any {
		return []any{r.ID, r.SupplierID, r.UpstreamRef, r.Name, r.Ratio, string(r.Facts), r.KeyEligible, r.SeenAt}
	}); err != nil {
		return fmt.Errorf("project groups: %w", err)
	}
	if err := projectionPages(ctx, db, lastProjectionRows(w.ChangedGroups, func(r UpdateProjectedGroupParams) uuid.UUID { return r.ID }), `
UPDATE suppliers.supplier_groups AS t SET name = v.name, ratio = v.ratio, facts = v.facts, present = true, last_seen_at = v.seen_at
FROM (VALUES `, `) AS v(id, name, ratio, facts, seen_at) WHERE t.id = v.id`, []string{"uuid", "text", "numeric", "jsonb", "timestamptz"}, func(r UpdateProjectedGroupParams) []any {
		return []any{r.ID, r.Name, r.Ratio, string(r.Facts), r.SeenAt}
	}); err != nil {
		return fmt.Errorf("project changed groups: %w", err)
	}
	if err := projectionPages(ctx, db, w.AbsentGroups, `
UPDATE suppliers.supplier_groups AS t SET present = false
FROM (VALUES `, `) AS v(id) WHERE t.id = v.id AND t.present`, []string{"uuid"}, func(r uuid.UUID) []any {
		return []any{r}
	}); err != nil {
		return fmt.Errorf("project absent groups: %w", err)
	}
	if err := projectionPages(ctx, db, w.Pool, `
UPDATE suppliers.supplier_groups AS t SET pool_accounts = v.accounts, pool_failed = v.failed,
    pool_usable = v.usable, pool_headroom = v.headroom, pool_capacity = v.capacity, pool_low = v.low,
    pool_observed_at = v.observed_at
FROM (VALUES `, `) AS v(id, accounts, failed, usable, headroom, capacity, low, observed_at) WHERE t.id = v.id`, []string{"uuid", "integer", "integer", "integer", "integer", "integer", "boolean", "timestamptz"}, func(r SetProjectedGroupPoolParams) []any {
		return []any{r.ID, r.Accounts, r.Failed, r.Usable, r.Headroom, r.Capacity, r.Low, r.ObservedAt}
	}); err != nil {
		return fmt.Errorf("project pool capacity: %w", err)
	}
	if err := projectionPages(ctx, db, w.ClearedPool, `
UPDATE suppliers.supplier_groups AS t SET pool_accounts = NULL, pool_failed = NULL, pool_usable = NULL,
    pool_headroom = NULL, pool_capacity = NULL, pool_low = NULL, pool_observed_at = NULL
FROM (VALUES `, `) AS v(id) WHERE t.id = v.id`, []string{"uuid"}, func(r uuid.UUID) []any {
		return []any{r}
	}); err != nil {
		return fmt.Errorf("clear pool capacity: %w", err)
	}
	if err := projectionPages(ctx, db, w.AutoSelected, `
UPDATE suppliers.supplier_groups AS t SET selected = true, codename = v.codename
FROM (VALUES `, `) AS v(id, codename) WHERE t.id = v.id AND NOT t.selected AND t.codename IS NULL`, []string{"uuid", "text"}, func(r AutoSelectGroupParams) []any {
		return []any{r.ID, r.Codename}
	}); err != nil {
		return fmt.Errorf("select groups: %w", err)
	}
	if err := projectionPages(ctx, db, w.Eligibility, `
UPDATE suppliers.supplier_groups AS t SET key_eligible = v.eligible
FROM (VALUES `, `) AS v(id, eligible) WHERE t.id = v.id AND t.key_eligible IS DISTINCT FROM v.eligible`, []string{"uuid", "boolean"}, func(r SetGroupEligibilityParams) []any {
		return []any{r.ID, r.Eligible}
	}); err != nil {
		return fmt.Errorf("project key eligibility: %w", err)
	}
	if err := projectionPages(ctx, db, lastProjectionRows(w.Models, func(r UpsertProjectedModelParams) projectedNameKey {
		return projectedNameKey{r.SupplierGroupID, r.UpstreamName}
	}), `
INSERT INTO suppliers.supplier_models (supplier_id, supplier_group_id, upstream_name, model_id, formats)
VALUES `, `
ON CONFLICT (supplier_group_id, upstream_name) DO UPDATE
SET model_id = excluded.model_id, formats = excluded.formats, present = true`, []string{"uuid", "uuid", "text", "uuid", "text[]"}, func(r UpsertProjectedModelParams) []any {
		return []any{r.SupplierID, r.SupplierGroupID, r.UpstreamName, r.ModelID, r.Formats}
	}); err != nil {
		return fmt.Errorf("project models: %w", err)
	}
	if err := projectionPages(ctx, db, w.AbsentModels, `
UPDATE suppliers.supplier_models AS t SET present = false
FROM (VALUES `, `) AS v(group_id, name) WHERE t.supplier_group_id = v.group_id AND t.upstream_name = v.name AND t.present`, []string{"uuid", "text"}, func(r SetProjectedModelAbsentParams) []any {
		return []any{r.SupplierGroupID, r.UpstreamName}
	}); err != nil {
		return fmt.Errorf("project absent models: %w", err)
	}
	return nil
}

func (w *ProjectionWrites) applyEndpoints(ctx context.Context, db DBTX) error {
	if err := projectionPages(ctx, db, w.Endpoints, `
INSERT INTO suppliers.endpoints (id, supplier_id, supplier_group_id, model_id, upstream_name, formats)
VALUES `, ``, []string{"uuid", "uuid", "uuid", "uuid", "text", "text[]"}, func(r InsertProjectedEndpointParams) []any {
		return []any{r.ID, r.SupplierID, r.SupplierGroupID, r.ModelID, r.UpstreamName, r.Formats}
	}); err != nil {
		return fmt.Errorf("project endpoints: %w", err)
	}
	if err := projectionPages(ctx, db, w.ChangedEndpoints, `
UPDATE suppliers.endpoints AS t SET upstream_name = v.name, formats = v.formats
FROM (VALUES `, `) AS v(id, name, formats) WHERE t.id = v.id`, []string{"uuid", "text", "text[]"}, func(r UpdateProjectedEndpointParams) []any {
		return []any{r.ID, r.UpstreamName, r.Formats}
	}); err != nil {
		return fmt.Errorf("project changed endpoints: %w", err)
	}
	if err := projectionPages(ctx, db, w.Costs, `
WITH wanted(supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint) AS (VALUES `, `), made AS (
 INSERT INTO suppliers.cost_schedules (supplier_id, endpoint_id, native_unit, schedule, basis, fingerprint)
 SELECT * FROM wanted ON CONFLICT (endpoint_id, fingerprint) DO NOTHING
 RETURNING id, endpoint_id
), schedules AS (
 SELECT id, endpoint_id FROM made
 UNION ALL
 SELECT c.id, c.endpoint_id FROM suppliers.cost_schedules c
 JOIN wanted w ON w.endpoint_id = c.endpoint_id AND w.fingerprint = c.fingerprint
)
UPDATE suppliers.endpoints e SET cost_state = 'known', cost_schedule_id = s.id, cost_state_reason = NULL
FROM schedules s WHERE e.id = s.endpoint_id`, []string{"uuid", "uuid", "text", "jsonb", "text", "text"}, func(r EnsureCostScheduleParams) []any {
		return []any{r.SupplierID, r.EndpointID, r.NativeUnit, string(r.Schedule), r.Basis, r.Fingerprint}
	}); err != nil {
		return fmt.Errorf("project costs: %w", err)
	}
	if err := projectionPages(ctx, db, w.UnknownCosts, `
UPDATE suppliers.endpoints AS t SET cost_state = 'unknown', cost_state_reason = v.reason
FROM (VALUES `, `) AS v(id, reason) WHERE t.id = v.id AND t.cost_state = 'known'`, []string{"uuid", "text"}, func(r UnsetProjectedEndpointCostParams) []any {
		return []any{r.ID, r.Reason}
	}); err != nil {
		return fmt.Errorf("project unknown costs: %w", err)
	}
	return nil
}

// Explicit casts keep nullable values and all-null columns typed. VALUES also supports ragged
// format arrays without padding them, unlike a multidimensional PostgreSQL array.
// JSON is passed as text: uncached execution otherwise infers []byte as bytea.
func projectionPages[T any](ctx context.Context, db DBTX, rows []T, prefix, suffix string, types []string, values func(T) []any) error {
	for start := 0; start < len(rows); start += ProjectionPageSize {
		page := rows[start:min(start+ProjectionPageSize, len(rows))]
		var sql strings.Builder
		sql.WriteString(prefix)
		args := make([]any, 0, len(page)*len(types))
		for i, r := range page {
			if i > 0 {
				sql.WriteByte(',')
			}
			sql.WriteByte('(')
			for j, typ := range types {
				if j > 0 {
					sql.WriteByte(',')
				}
				fmt.Fprintf(&sql, "$%d::%s", len(args)+j+1, typ)
			}
			sql.WriteByte(')')
			args = append(args, values(r)...)
		}
		sql.WriteString(suffix)
		// Tail pages have varying SQL text; caching them would evict hot-path statements.
		args = append([]any{pgx.QueryExecModeExec}, args...)
		if _, err := db.Exec(ctx, sql.String(), args...); err != nil {
			return fmt.Errorf("rows %d-%d: %w", start+1, start+len(page), err)
		}
	}
	return nil
}

type projectedNameKey struct {
	group uuid.UUID
	name  string
}

// One INSERT ON CONFLICT cannot affect a row twice, and UPDATE FROM chooses an
// unspecified match for repeated IDs. Preserve the row-by-row path's last write.
func lastProjectionRows[T any, K comparable](rows []T, key func(T) K) []T {
	indices := make(map[K]int, len(rows))
	out := make([]T, 0, len(rows))
	for _, r := range rows {
		k := key(r)
		if i, ok := indices[k]; ok {
			out[i] = r
		} else {
			indices[k] = len(out)
			out = append(out, r)
		}
	}
	return out
}
