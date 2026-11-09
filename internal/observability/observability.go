// Package observability keeps the call log: one metadata row per request and one per attempt, written after the request
// ends, read back by the account the call belongs to (README.md). It holds no conversation content and no credential.
package observability

import (
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Module is the module's migrations, for cmd/axiom to give pg.Migrate and pg.DB.Check.
func Module() pg.Module {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return pg.Module{Name: "observability", Migrations: files, JobRole: retentionJobRole}
}

// EndUserHash is the keyed hash of an end user's identifier. Its array is private, so no code outside this package can
// build a non-zero one except through HashEndUser, and a raw identifier cannot be stored in it; the zero value means the
// request named no end user.
type EndUserHash struct{ sum [sha256.Size]byte }

// HashEndUser is HMAC-SHA256 of the account and id under key: without the key, an identifier cannot be recovered by
// trying guesses, and the same identifier in two accounts hashes apart. The key stays out of the database; rotating it
// makes earlier hashes unmatchable. The gateway derives it from the node's identity secret.
func HashEndUser(key []byte, account uuid.UUID, id string) EndUserHash {
	mac := hmac.New(sha256.New, key)
	mac.Write(account[:]) // hash.Hash.Write never fails
	mac.Write([]byte(id))
	return EndUserHash{sum: [sha256.Size]byte(mac.Sum(nil))}
}

// AttemptOutcome is how an attempt ended. Committed means content reached the client; Failed means it did not; Unknown
// means the supplier may have run the request.
type AttemptOutcome string

// The outcomes an attempt record keeps.
const (
	AttemptCommitted AttemptOutcome = "committed"
	AttemptFailed    AttemptOutcome = "failed"
	AttemptUnknown   AttemptOutcome = "unknown"
)

// AttemptRecord is one try at an endpoint, known only while it happened. The call it belongs to gives the request ID,
// the request's start (the partition key) and the tenant. The supplier's cost and reconciliation key stay with
// suppliers, which keeps them by (request ID, sequence); this record is what makes the attempt findable there.
type AttemptRecord struct {
	Seq         platform.AttemptSeq // 1 to platform.MaxAttemptSeq, in the order the attempts were made
	Endpoint    uuid.UUID
	Supplier    uuid.UUID
	SourceClass string // a stable source class slug; the seeds are official, cloud or relay
	Egress      string // the outbound route, by name
	Outcome     AttemptOutcome
	ErrorKind   string
	StartedAt   time.Time
	FinishedAt  time.Time
	FirstToken  time.Duration // 0 when the attempt produced no output
}

// CallRecord is everything the log keeps of one request, known only while it happened, and its attempts. Amounts are
// in billing's smallest unit. It names no key, credential or conversation text; the ledger transactions and supplier
// charges are kept by their owners under the same request ID, which is the only link between them (README「追溯」). The
// call's endpoint, source class, egress and attempt count are the last attempt's, so the row cannot disagree with its
// attempts.
type CallRecord struct {
	ID                               uuid.UUID // the request ID
	At                               time.Time // when the request started
	Site, Account, Workspace, Key    uuid.UUID
	EndUser                          EndUserHash
	IP                               netip.Addr
	ClientApp, SessionID, SubagentID string
	AppReferer, AppTitle             string // the client's own name for its application, from the attribution headers
	AppCategories                    string // comma-separated marketplace categories, at most two
	Class                            string // request class, such as chat or embeddings
	Language                         string
	UsedTools, HasImage, HasFile     bool
	Model                            string // the public name the client asked for
	Attempts                         []AttemptRecord
	Route                            []byte // a JSON object, routing's record of how the endpoint was chosen
	Amounts
	CostNano                int64
	Currency                platform.Currency
	Latency, FirstToken     time.Duration // FirstToken is 0 when no output was produced
	FinishReason, ErrorKind string
	ConsentVersion          string
	TrainingAllowed         bool
	Streamed                bool
	CacheHit                bool // answered from the response cache, with no upstream attempt
	// BillingPending is a record written before the call's settlement was: its Amounts are not known yet. Slot.Commit
	// parks it and stays open for the settled record of the same call, which replaces it; it reaches the call log only
	// when none will, and the writer then takes its Amounts from the ledger if the settlement did commit (README「写入」).
	BillingPending bool
}

// Amounts is what the call's settlement charged, as the ledger stores it: the usage it billed, the tier and price
// version it priced at, the charge, whether the waiver rules made it free, and the upstream's prompt total. A record
// written before its settlement has none of them; Config.Ledger reads them back for such a record.
type Amounts struct {
	PriceVersion  string
	Usage         map[catalog.Item]int64
	ServiceTier   catalog.ServiceTier
	PriceNano     int64
	Free          bool
	PhysicalInput *int64 // nil when the upstream reported none
}

// row is a CallRecord as the insert statement reads it: its members are the table's column names.
type row struct {
	PriceVersion    string           `json:"price_version"`
	ID              string           `json:"id"`
	At              time.Time        `json:"at"`
	SiteID          string           `json:"site_id"`
	AccountID       string           `json:"account_id"`
	WorkspaceID     string           `json:"workspace_id"`
	KeyID           string           `json:"key_id"`
	EndUserHash     string           `json:"end_user_hash"`
	IP              string           `json:"ip"`
	ClientApp       string           `json:"client_app"`
	SessionID       string           `json:"session_id"`
	SubagentID      string           `json:"subagent_id"`
	AppReferer      string           `json:"app_referer"`
	AppTitle        string           `json:"app_title"`
	AppCategories   string           `json:"app_categories"`
	RequestClass    string           `json:"request_class"`
	Language        string           `json:"language"`
	UsedTools       bool             `json:"used_tools"`
	HasImage        bool             `json:"has_image"`
	HasFile         bool             `json:"has_file"`
	Model           string           `json:"model"`
	EndpointID      string           `json:"endpoint_id"`
	SourceClass     string           `json:"source_class"`
	Egress          string           `json:"egress"`
	AttemptCount    int              `json:"attempt_count"`
	Route           string           `json:"route"`
	Usage           map[string]int64 `json:"usage"`
	ServiceTier     string           `json:"service_tier"`
	PriceNano       int64            `json:"price_nano"`
	CostNano        int64            `json:"cost_nano"`
	Currency        string           `json:"currency"`
	FreeOfCharge    bool             `json:"free_of_charge"`
	LatencyMs       int64            `json:"latency_ms"`
	FirstTokenMs    int64            `json:"first_token_ms"`
	FinishReason    string           `json:"finish_reason"`
	ErrorKind       string           `json:"error_kind"`
	ConsentVersion  string           `json:"consent_version"`
	TrainingAllowed bool             `json:"training_allowed"`
	Streamed        bool             `json:"streamed"`
	CacheHit        bool             `json:"cache_hit"`
	PhysicalInput   int64            `json:"physical_input"`
	BillingPending  bool             `json:"billing_pending,omitempty"` // omitted when settled: a settled record's bytes are as they were
}

// attemptRow is an AttemptRecord as the insert statement reads it: the call's ID, start and tenant, then its own
// fields. Its members are the table's column names.
type attemptRow struct {
	RequestID    string    `json:"request_id"`
	At           time.Time `json:"at"`
	Seq          int16     `json:"attempt_seq"`
	SiteID       string    `json:"site_id"`
	AccountID    string    `json:"account_id"`
	EndpointID   string    `json:"endpoint_id"`
	SupplierID   string    `json:"supplier_id"`
	SourceClass  string    `json:"source_class"`
	Egress       string    `json:"egress"`
	Outcome      string    `json:"outcome"`
	ErrorKind    string    `json:"error_kind"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
	FirstTokenMs int64     `json:"first_token_ms"`
}

// newRows checks what the database cannot say well and converts r and its attempts. The call's endpoint, source
// class, egress and attempt count come from the last attempt, so the two statements below always agree.
func newRows(r CallRecord) (row, []attemptRow, error) {
	if len(r.Attempts) > platform.MaxAttemptSeq {
		return row{}, nil, fmt.Errorf("attempts: %d, at most %d", len(r.Attempts), platform.MaxAttemptSeq)
	}
	attempts := make([]attemptRow, len(r.Attempts))
	want := platform.AttemptSeq(1)
	for i, a := range r.Attempts {
		if a.Seq != want {
			return row{}, nil, fmt.Errorf("attempt %d: sequence %d, want %d", i+1, a.Seq, want)
		}
		want++
		var err error
		if attempts[i], err = newAttemptRow(r, a); err != nil {
			return row{}, nil, err
		}
	}
	call, err := newCallRow(r)
	return call, attempts, err
}

// maxRouteText is the route column's limit on its jsonb text (migrations/00001_call_log.sql). A record over it is
// refused here, before it reaches the spill: once there, PostgreSQL would refuse its batch on every replay and hold
// every later record behind it. routing.Record.JSON is bounded well below it, so only a bug reaches the refusal.
const maxRouteText = 8192

// jsonbTextLen bounds the length of canonical JSON once PostgreSQL prints it as jsonb text, which adds a space after
// every ':' and ',' between tokens, escapes strings as canonical JSON does and prints small integers unchanged (the
// route holds no others). Counting every ':' and ',' byte, also those inside strings, can only overcount.
func jsonbTextLen(compact string) int {
	return len(compact) + strings.Count(compact, ":") + strings.Count(compact, ",")
}

// newCallRow converts the call's own fields. It derives what each attempt already says.
func newCallRow(r CallRecord) (row, error) {
	route := string(r.Route)
	if route == "" {
		route = "{}"
	} else if err := jsonx.Validate(r.Route); err != nil {
		return row{}, fmt.Errorf("route: %w", err)
	}
	if n := jsonbTextLen(route); n > maxRouteText {
		return row{}, fmt.Errorf("route: %d bytes as jsonb text, at most %d", n, maxRouteText)
	}
	if err := checkText(map[string]string{
		"client app": r.ClientApp, "session": r.SessionID, "subagent": r.SubagentID,
		"app referer": r.AppReferer, "app title": r.AppTitle, "app categories": r.AppCategories,
		"class": r.Class, "language": r.Language, "model": r.Model,
		"finish reason": r.FinishReason, "error kind": r.ErrorKind, "consent version": r.ConsentVersion,
	}); err != nil {
		return row{}, err
	}
	if !r.IP.IsValid() {
		return row{}, errors.New("ip: not set")
	}
	endUser := ""
	if r.EndUser != (EndUserHash{}) {
		endUser = hex.EncodeToString(r.EndUser.sum[:])
	}
	endpoint, sourceClass, egress := uuid.Nil().String(), "", ""
	if n := len(r.Attempts); n > 0 {
		last := r.Attempts[n-1]
		endpoint, sourceClass, egress = last.Endpoint.String(), last.SourceClass, last.Egress
	}
	call := row{
		ID: r.ID.String(), At: r.At, SiteID: r.Site.String(), AccountID: r.Account.String(),
		WorkspaceID: r.Workspace.String(), KeyID: r.Key.String(), EndUserHash: endUser, IP: r.IP.String(),
		ClientApp: r.ClientApp, SessionID: r.SessionID, SubagentID: r.SubagentID, RequestClass: r.Class,
		AppReferer: r.AppReferer, AppTitle: r.AppTitle, AppCategories: r.AppCategories,
		Language: r.Language, UsedTools: r.UsedTools, HasImage: r.HasImage, HasFile: r.HasFile, Model: r.Model,
		EndpointID: endpoint, SourceClass: sourceClass, Egress: egress, AttemptCount: len(r.Attempts), Route: route,
		CostNano: r.CostNano, Currency: r.Currency.String(), LatencyMs: r.Latency.Milliseconds(),
		FirstTokenMs: r.FirstToken.Milliseconds(), FinishReason: r.FinishReason, ErrorKind: r.ErrorKind,
		ConsentVersion: r.ConsentVersion, TrainingAllowed: r.TrainingAllowed, Streamed: r.Streamed,
		CacheHit: r.CacheHit,
	}
	err := call.settle(r.Amounts)
	call.BillingPending = r.BillingPending
	return call, err
}

// settle puts the settlement's amounts in the row and marks it settled. A record's own amounts go through it, and so do
// the ledger's for a record written pending, so both are stored the same way.
func (w *row) settle(a Amounts) error {
	usage := make(map[string]int64, len(a.Usage))
	for item, n := range a.Usage {
		if item >= catalog.NumItems {
			return fmt.Errorf("usage: unknown billing item %d", uint8(item))
		}
		usage[item.String()] = n
	}
	if a.ServiceTier > catalog.ServiceTierFast {
		return fmt.Errorf("service tier: unknown value %d", uint8(a.ServiceTier))
	}
	w.PriceVersion, w.Usage, w.ServiceTier, w.PriceNano = a.PriceVersion, usage, a.ServiceTier.String(), a.PriceNano
	w.FreeOfCharge, w.PhysicalInput, w.BillingPending = a.Free, physicalInput(a.PhysicalInput), false
	return nil
}

// physicalInput is the stored prompt total, -1 when the upstream reported none.
func physicalInput(n *int64) int64 {
	if n == nil {
		return -1
	}
	return *n
}

// newAttemptRow converts one attempt, taking the request ID, start and tenant from the call it belongs to.
func newAttemptRow(call CallRecord, a AttemptRecord) (attemptRow, error) {
	switch a.Outcome {
	case AttemptCommitted, AttemptFailed, AttemptUnknown:
	default:
		return attemptRow{}, fmt.Errorf("attempt %d: outcome %q, want committed, failed or unknown", a.Seq, a.Outcome)
	}
	if a.Endpoint == uuid.Nil() || a.Supplier == uuid.Nil() {
		return attemptRow{}, fmt.Errorf("attempt %d: endpoint and supplier are required", a.Seq)
	}
	if a.StartedAt.IsZero() || a.FinishedAt.IsZero() || a.FinishedAt.Before(a.StartedAt) {
		return attemptRow{}, fmt.Errorf("attempt %d: start and finish times are required, finish not before start", a.Seq)
	}
	if a.FirstToken < 0 {
		return attemptRow{}, fmt.Errorf("attempt %d: first token %s is negative", a.Seq, a.FirstToken)
	}
	if err := checkText(map[string]string{
		"source class": a.SourceClass, "egress": a.Egress, "error kind": a.ErrorKind,
	}); err != nil {
		return attemptRow{}, fmt.Errorf("attempt %d: %w", a.Seq, err)
	}
	return attemptRow{
		RequestID: call.ID.String(), At: call.At, Seq: int16(a.Seq), SiteID: call.Site.String(),
		AccountID: call.Account.String(), EndpointID: a.Endpoint.String(), SupplierID: a.Supplier.String(),
		SourceClass: a.SourceClass, Egress: a.Egress, Outcome: string(a.Outcome), ErrorKind: a.ErrorKind,
		StartedAt: a.StartedAt, FinishedAt: a.FinishedAt, FirstTokenMs: a.FirstToken.Milliseconds(),
	}, nil
}

// checkText refuses text the database cannot store, before it reaches a batch: bad UTF-8 and a NUL have no place in
// any of the log's text columns.
func checkText(fields map[string]string) error {
	for name, text := range fields {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return fmt.Errorf("%s: not valid text (bad UTF-8 or a NUL)", name)
		}
	}
	return nil
}
