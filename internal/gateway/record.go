package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
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
	app       appAttribution
}

func (s *session) trail() trail {
	return trail{
		call: s.c, tries: s.tries, start: s.start, currency: s.currency,
		clientApp: clientAppOf(s.req.Header), app: attributionOf(s.req.Header),
	}
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

// appAttribution is the client's own name for the application it builds, from OpenRouter's app-attribution headers
// (https://openrouter.ai/docs/app-attribution, read 2026-10-29): the URL that identifies the application, its display
// name, and the marketplace categories it claims. They are recorded for the account's own analytics and never forwarded
// upstream (headers.go). A header is the client's word, but it is bounded and cleaned here because it goes into a
// column: a value that does not fit or is not text must not make the call log refuse the whole record.
type appAttribution struct{ referer, title, categories string }

// The widths of the call log's attribution columns (migration 00010). A longer header is cut, not stored.
const (
	maxAppReferer    = 2048
	maxAppTitle      = 200
	maxAppCategories = 64
)

// attributionOf reads the three app-attribution headers. HTTP-Referer names the application and is OpenRouter's
// primary identifier; X-OpenRouter-Title is its display name, which clients also send as X-Title, the older name;
// X-OpenRouter-Categories is a comma-separated list of marketplace categories, at most two of up to 30 characters.
func attributionOf(h http.Header) appAttribution {
	title := h.Get("X-Openrouter-Title")
	if strings.TrimSpace(title) == "" {
		title = h.Get("X-Title")
	}
	return appAttribution{
		referer:    refererOf(h.Get("Http-Referer")),
		title:      headerText(title, maxAppTitle),
		categories: categoriesOf(h.Get("X-Openrouter-Categories")),
	}
}

// refererOf keeps the part of HTTP-Referer that names the application: a URL's scheme, host and path, without the
// userinfo, query or fragment, which can carry credentials and OAuth codes. A value that is not an absolute URL is
// kept as it was cleaned and truncated; there is no URL to take apart.
func refererOf(s string) string {
	s = cleanHeader(s)
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return headerText(s, maxAppReferer)
	}
	if u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawFragment == "" {
		return headerText(s, maxAppReferer)
	}
	u.User, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = nil, "", "", "", false
	return headerText(u.String(), maxAppReferer)
}

// headerText is one header value as the log keeps it: valid UTF-8, no control characters, trimmed, and no longer than n
// runes. The cut falls on a rune boundary, so the stored text is still valid. A clean value is returned as it came,
// without allocating: this runs on the call path.
func headerText(s string, n int) string {
	return strings.TrimSpace(truncate(cleanHeader(s), n))
}

// cleanHeader makes a header value safe to keep, without bounding its length: valid UTF-8, no control characters, and
// no surrounding whitespace.
func cleanHeader(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD") // a header's bytes are not checked to be text by every client
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		s = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, s)
	}
	return strings.TrimSpace(s)
}

// truncate cuts s to n runes on a rune boundary.
func truncate(s string, n int) string {
	runes := 0
	for i := range s {
		if runes == n {
			return s[:i]
		}
		runes++
	}
	return s
}

// categoriesOf keeps the categories in the shape OpenRouter accepts, in its spelling: lowercase names of letters, digits
// and single hyphens, at most 30 characters each, the first two of the list, as a request may claim two. A name outside
// OpenRouter's recognised list is kept as the client claimed it rather than guessed at.
func categoriesOf(s string) string {
	var kept []string
	for c := range strings.SplitSeq(s, ",") {
		if len(kept) == 2 {
			break
		}
		if c = strings.ToLower(strings.TrimSpace(c)); validCategory(c) && !slices.Contains(kept, c) {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, ",")
}

// validCategory is a marketplace category's spelling: lowercase letters and digits, separated by single hyphens.
func validCategory(c string) bool {
	if c == "" || len(c) > 30 || c[0] == '-' || c[len(c)-1] == '-' || strings.Contains(c, "--") {
		return false
	}
	for _, r := range c {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
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
		AppReferer: t.app.referer, AppTitle: t.app.title, AppCategories: t.app.categories,
		Class: "chat", ConsentVersion: noConsent,
		Usage:       usageOf(f),
		ServiceTier: tierOf(f),
		// The charge is what the floor let the user pay; the cost is what the attempts cost us, whether or not the user
		// paid it. Free is the waiver rules' verdict and nothing else: a model priced at zero, or a charge the balance's
		// floor cut to nothing, is not a free call.
		PriceNano: f.Charged.CeilAmount(), CostNano: costOf(o), Currency: t.currency,
		Free:    f.Waived,
		Latency: end.Sub(t.start),
		Route:   route,
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
