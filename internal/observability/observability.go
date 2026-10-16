// Package observability keeps the call log: one metadata row per request, written after the request ends, read back
// by the account it belongs to (README.md). It holds no conversation content and no credential.
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
	return pg.Module{Name: "observability", Migrations: files}
}

// EndUserHash is the keyed hash of an end user's identifier. Its array is private, so no code outside this package can
// build a non-zero one except through HashEndUser, and a raw identifier cannot be stored in it; the zero value means the
// request named no end user.
type EndUserHash struct{ sum [sha256.Size]byte }

// HashEndUser is HMAC-SHA256 of id under key: without the key, an identifier cannot be recovered by trying guesses.
// The key belongs to the site, and rotating it makes earlier hashes unmatchable, which is the point of keeping it
// out of the database.
func HashEndUser(key []byte, id string) EndUserHash {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id)) // hash.Hash.Write never fails
	return EndUserHash{sum: [sha256.Size]byte(mac.Sum(nil))}
}

// CallRecord is everything the log keeps of one request, known only while it happened. Amounts are in billing's
// smallest unit. It names no key, credential or conversation text; the attempts, the ledger entries and the
// supplier charges are kept by their owners under the same ID, which this record names where it can.
type CallRecord struct {
	ID                               uuid.UUID // the request ID
	At                               time.Time // when the request started
	Site, Account, Workspace, Key    uuid.UUID
	EndUser                          EndUserHash
	IP                               netip.Addr
	ClientApp, SessionID, SubagentID string
	Class                            string // request class, such as chat or embeddings
	Language                         string
	UsedTools, HasImage, HasFile     bool
	Model                            string    // the public name the client asked for
	Endpoint                         uuid.UUID // the endpoint of the final attempt; the nil UUID if none was made
	SourceClass                      string
	Attempts                         int
	Route                            []byte // a JSON object, routing's record of how the endpoint was chosen
	Ledger                           []uuid.UUID
	Usage                            map[catalog.Item]int64
	ServiceTier                      catalog.ServiceTier
	PriceNano, CostNano              int64
	Currency                         platform.Currency
	Free                             bool
	Latency, FirstToken              time.Duration // FirstToken is 0 when no output was produced
	FinishReason, ErrorKind          string
	ConsentVersion                   string
	TrainingAllowed                  bool
}

// row is a CallRecord as the insert statement reads it: its members are the table's column names.
type row struct {
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
	RequestClass    string           `json:"request_class"`
	Language        string           `json:"language"`
	UsedTools       bool             `json:"used_tools"`
	HasImage        bool             `json:"has_image"`
	HasFile         bool             `json:"has_file"`
	Model           string           `json:"model"`
	EndpointID      string           `json:"endpoint_id"`
	SourceClass     string           `json:"source_class"`
	AttemptCount    int              `json:"attempt_count"`
	Route           string           `json:"route"`
	LedgerEntryIDs  []string         `json:"ledger_entry_ids"`
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
}

// newRow checks what the database cannot say well and converts r. Everything else is the table's to refuse.
func newRow(r CallRecord) (row, error) {
	route := string(r.Route)
	if route == "" {
		route = "{}"
	} else if err := jsonx.Validate(r.Route); err != nil {
		return row{}, fmt.Errorf("route: %w", err)
	}
	usage := make(map[string]int64, len(r.Usage))
	for item, n := range r.Usage {
		if item >= catalog.NumItems {
			return row{}, fmt.Errorf("usage: unknown billing item %d", uint8(item))
		}
		usage[item.String()] = n
	}
	for name, text := range map[string]string{
		"client app": r.ClientApp, "session": r.SessionID, "subagent": r.SubagentID,
		"class": r.Class, "language": r.Language, "model": r.Model, "source class": r.SourceClass,
		"finish reason": r.FinishReason, "error kind": r.ErrorKind, "consent version": r.ConsentVersion,
	} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return row{}, fmt.Errorf("%s: not valid text (bad UTF-8 or a NUL)", name)
		}
	}
	if r.ServiceTier > catalog.ServiceTierFast {
		return row{}, fmt.Errorf("service tier: unknown value %d", uint8(r.ServiceTier))
	}
	if !r.IP.IsValid() {
		return row{}, errors.New("ip: not set")
	}
	ledger := make([]string, len(r.Ledger))
	for i, id := range r.Ledger {
		ledger[i] = id.String()
	}
	endUser := ""
	if r.EndUser != (EndUserHash{}) {
		endUser = hex.EncodeToString(r.EndUser.sum[:])
	}
	return row{
		ID: r.ID.String(), At: r.At, SiteID: r.Site.String(), AccountID: r.Account.String(),
		WorkspaceID: r.Workspace.String(), KeyID: r.Key.String(), EndUserHash: endUser, IP: r.IP.String(),
		ClientApp: r.ClientApp, SessionID: r.SessionID, SubagentID: r.SubagentID, RequestClass: r.Class,
		Language: r.Language, UsedTools: r.UsedTools, HasImage: r.HasImage, HasFile: r.HasFile, Model: r.Model,
		EndpointID: r.Endpoint.String(), SourceClass: r.SourceClass, AttemptCount: r.Attempts, Route: route,
		LedgerEntryIDs: ledger, Usage: usage, ServiceTier: r.ServiceTier.String(), PriceNano: r.PriceNano,
		CostNano: r.CostNano, Currency: r.Currency.String(), FreeOfCharge: r.Free, LatencyMs: r.Latency.Milliseconds(),
		FirstTokenMs: r.FirstToken.Milliseconds(), FinishReason: r.FinishReason, ErrorKind: r.ErrorKind,
		ConsentVersion: r.ConsentVersion, TrainingAllowed: r.TrainingAllowed,
	}, nil
}
