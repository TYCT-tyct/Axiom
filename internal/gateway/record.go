package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/observability"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// try is what the call log needs of an attempt that the settlement does not carry: the route entry it bound, and when
// it began. The two run in step with session.ends, one entry each in the order the attempts were made.
type try struct {
	entry   routing.Entry
	started time.Time
}

// trail is a finished call as the call log takes it: the call, its attempts, when it began and in which currency it was
// priced. A settlement that is written in the background keeps this rather than the session, whose response writer and
// buffers belong to a request that is over.
type trail struct {
	call      *Call
	tries     []try
	start     time.Time
	currency  platform.Currency
	clientApp string
}

func (s *session) trail() trail {
	return trail{call: s.c, tries: s.tries, start: s.start, currency: s.currency, clientApp: clientAppOf(s.req.Header)}
}

// clientAppOf is the client application the request's User-Agent names: Claude Code, Codex, or none (empty). It is
// read when the request arrives, because the log cannot be told later (docs/domains/observability.md「记什么」).
func clientAppOf(h http.Header) string {
	agent := strings.ToLower(h.Get("User-Agent"))
	switch {
	case strings.Contains(agent, "claude-cli"), strings.Contains(agent, "claude code"), strings.Contains(agent, "claude-code"):
		return "claude-code"
	case strings.Contains(agent, "codex"):
		return "codex"
	}
	return ""
}

// log writes the call into the call log. The record's amounts are the settlement's, so it is written once that is: the
// ledger's charge is what the call cost, and a record that said zero while the settlement was still to come would
// disagree with it (docs/domains/observability.md「追溯」). A record the recorder will not take is dropped and
// counted there: the log is observation, and a call never fails or waits for it.
func (x *Executor) log(ctx context.Context, t trail, o billing.Outcome, f billing.Final, end time.Time) {
	if x.Records == nil {
		return
	}
	route, err := t.call.Plan.Record.JSON()
	if err != nil {
		x.Logger.ErrorContext(ctx, "a call's route could not be recorded", "request_id", t.call.ID.String(), "err", err)
	}
	err = x.Records.Enqueue(recordOf(t, route, o, f, end)) //nolint:contextcheck // the hand-off never waits, and the record outlives the request
	if err != nil && !errors.Is(err, observability.ErrQueueFull) && !errors.Is(err, observability.ErrClosed) {
		x.Logger.ErrorContext(ctx, "a call's record could not be written", "request_id", t.call.ID.String(), "err", err)
	}
}

// noConsent is the agreement version of a record made before any user agreement exists to name: the call log refuses a
// record without one, and "none" says so instead of naming a version nobody agreed to. Training is not allowed then.
const noConsent = "none"

// recordOf is the call log's record of one call: the request, the route it took, its attempts, and what the ledger
// charged it. What the gateway does not know is left empty rather than guessed: the client's own session and subagent
// identifiers, the language of the request, and the end user, which is the site's to hash. The first token is not
// measured yet, so every attempt records none.
func recordOf(t trail, route []byte, o billing.Outcome, f billing.Final, end time.Time) observability.CallRecord {
	c := t.call
	rec := observability.CallRecord{
		ID: c.ID, At: t.start,
		Site: uuid.UUID(c.site.ID), Account: uuid.UUID(c.caller.AccountID()),
		Workspace: uuid.UUID(c.caller.WorkspaceID()), Key: uuid.UUID(c.caller.KeyID()), IP: c.ip,
		UsedTools: c.Request.Needs.Has(protocol.Tools), HasImage: c.Request.Images > 0, HasFile: c.Request.Documents > 0,
		Model: c.Request.Model, ClientApp: t.clientApp,
		Class: "chat", ConsentVersion: noConsent,
		Usage:       usageOf(f),
		ServiceTier: tierOf(f),
		// The charge is what the floor let the user pay; the cost is what the attempts cost us, whether or not the user
		// paid it. Free is the waiver rules' verdict and nothing else: a model priced at zero, or a charge the balance's
		// floor cut to nothing, is not a free call.
		PriceNano: f.Charged.CeilAmount(), CostNano: costOf(o), Currency: t.currency,
		Free:          f.Waived,
		Latency:       end.Sub(t.start),
		Route:         route,
		Streamed:      c.Request.Stream,
		PhysicalInput: physicalInputOf(f),
	}
	if f.PriceVersion != nil {
		rec.PriceVersion = f.PriceVersion.String()
	}
	rec.Attempts = attemptsOf(o, t.tries)
	if n := len(rec.Attempts); n > 0 {
		rec.FinishReason = finishOf(o.Attempts[n-1])
	}
	return rec
}

// physicalInputOf is the prompt total the upstream reported, added over the billed samplings; nil when none reported
// one. It is not the sum of input items, which cache reads and writes may overlap.
func physicalInputOf(f billing.Final) *int64 {
	if len(f.Segments) == 0 {
		return nil
	}
	var total int64
	for _, seg := range f.Segments {
		n, ok := seg.Usage.PhysicalInput()
		if !ok {
			return nil // one sampling's total is unknown, so their sum is not the prompt total
		}
		total += n
	}
	return &total
}

// usageOf is the usage the settlement charged: the samplings of the attempt that was billed, added up.
func usageOf(f billing.Final) map[catalog.Item]int64 {
	counts := make(map[catalog.Item]int64)
	for _, seg := range f.Segments {
		for it := range catalog.NumItems {
			counts[it] += seg.Usage.Count(it)
		}
	}
	return counts
}

// tierOf is the service tier the billed attempt was served at.
func tierOf(f billing.Final) catalog.ServiceTier {
	if len(f.Segments) == 0 {
		return catalog.ServiceTierStandard
	}
	return f.Segments[0].Usage.ServiceTier()
}

// costOf is what the attempts cost the platform, in billionths of a credit: billing's own figure, which the waivers do
// not change.
func costOf(o billing.Outcome) (n int64) {
	for _, a := range o.Attempts {
		n += a.Cost
	}
	return n
}

// attemptsOf is the call's attempts as the log keeps them, from the settlement's results and the entries they bound.
// The source class is relay for every attempt: a routed entry does not carry it (routing.Entry has what a call needs
// of the endpoint, not what the snapshot weighed it by), and route.go gives every endpoint of this release that class.
func attemptsOf(o billing.Outcome, tries []try) []observability.AttemptRecord {
	out := make([]observability.AttemptRecord, 0, len(o.Attempts))
	for i, a := range o.Attempts {
		if i >= len(tries) {
			break // a settlement can name no more attempts than were made; the log says what it has
		}
		t := tries[i]
		out = append(out, observability.AttemptRecord{
			Seq:      platform.AttemptSeq(a.Seq), //nolint:gosec // G115: at most platform.MaxAttempts
			Endpoint: uuid.UUID(t.entry.Endpoint), Supplier: uuid.UUID(t.entry.Supplier),
			SourceClass: relayClass, Egress: directEgress, Outcome: outcomeOf(a), StartedAt: t.started, FinishedAt: a.FinishedAt,
		})
	}
	return out
}

// outcomeOf is how an attempt ended, as the log keeps it: whether content reached the client, and whether the supplier
// may have run the request anyway.
func outcomeOf(a billing.AttemptResult) observability.AttemptOutcome {
	switch a.Result {
	case billing.Committed, billing.Aggregated:
		return observability.AttemptCommitted
	case billing.Unknown:
		return observability.AttemptUnknown
	case billing.FailedBeforeCommit, billing.NotExecuted:
		return observability.AttemptFailed
	}
	return observability.AttemptUnknown // a result this build does not know is not taken for a success
}

// finishOf is the settlement's finish reason by the log's name.
func finishOf(a billing.AttemptResult) string {
	switch a.Finish {
	case billing.FinishStop:
		return "stop"
	case billing.FinishToolUse:
		return "tool_use"
	case billing.FinishLength:
		return "length"
	case billing.FinishRefusal:
		return "refusal"
	case billing.FinishError:
		return "error"
	case billing.FinishTruncated:
		return "truncated"
	case billing.FinishTimeout:
		return "timeout"
	case billing.FinishCancelled:
		return "cancelled"
	case billing.FinishNone:
	}
	return "" // none reported: the upstream said nothing, or the call ended before an answer
}

// directEgress is the one way a node reaches its suppliers: from its own address (internal/platform/egress), which is
// what the log's egress column names.
const directEgress = "direct"

// relayClass is the source class of every endpoint this release routes to (route.go: routing.ClassRelay).
const relayClass = "relay"
