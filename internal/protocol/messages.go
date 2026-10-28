package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"errors"
	"fmt"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// messagesEvents are the event types of Anthropic《Streaming messages》(read 2026-10-14) and their roles. A
// content_block_start is content only when the block has some; error ends the stream; other types are passive.
var messagesEvents = [...]struct {
	name string
	role role
}{
	{"ping", heartbeat},
	{"message_start", passive},
	{"content_block_start", passive},
	{"content_block_delta", content},
	{"content_block_stop", passive},
	{"message_delta", ending},
	{"message_stop", end},
	{"error", passive},
}

// messagesStream reads one Messages stream's events.
type messagesStream struct {
	eventReader
	// The content block being read: whether its type holds text that may be empty, and whether it is a web search.
	textBlock, serverTool, webSearch bool
	failed                           bool // the event is the upstream's error
	report                           *messagesUsage
	reportErr                        error // the usage object is not one messagesUsage can hold
}

func newMessagesStream(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := &messagesStream{}
	s.init(u, o, messagesUsageMembers, charge)
	return s
}

var (
	messagesFields = map[string]func(*messagesStream) error{
		"type":          (*messagesStream).eventType,
		"delta":         func(s *messagesStream) error { return walk(&s.reader, s, contentFields) },
		"content_block": func(s *messagesStream) error { return walk(&s.reader, s, blockFields) },
		"message":       func(s *messagesStream) error { return walk(&s.reader, s, messageFields) },
		"usage":         (*messagesStream).usage,
		"error":         func(s *messagesStream) error { return errorObject(&s.reader, &s.eventReader, errorFields) },
	}
	// contentFields and blockFields carry the members the client is given that the upstream bills as output:
	// thinking's signature and redacted_thinking's data count like its text (README.md「字节上界」).
	contentFields = map[string]func(*messagesStream) error{
		"text": (*messagesStream).text, "thinking": (*messagesStream).text, "partial_json": (*messagesStream).text,
		"signature": (*messagesStream).text, "stop_reason": (*messagesStream).stopReason,
	}
	blockFields = map[string]func(*messagesStream) error{
		"text": (*messagesStream).text, "thinking": (*messagesStream).text, "type": (*messagesStream).blockType,
		"name": (*messagesStream).toolName, "input": (*messagesStream).rawContent, "signature": (*messagesStream).text,
		"data": (*messagesStream).text,
	}
	messageFields = map[string]func(*messagesStream) error{"usage": (*messagesStream).usage}
)

func (s *messagesStream) event(data []byte, m *meter) (event, error) {
	s.start(data)
	s.textBlock, s.serverTool, s.webSearch, s.failed, s.report, s.reportErr = false, false, false, false, nil, nil
	if err := walk(&s.reader, s, messagesFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if s.failed {
		// an error event ends the stream, so a usage object it carries is the upstream's last word on the attempt: it
		// is taken here, before the error ends the reading (docs/domains/billing.md「用量来源与暂估」)
		if s.report != nil {
			s.meter(m, true)
		}
		return event{}, s.errorEvent("an error event")
	}
	s.settle()
	if s.report != nil {
		s.meter(m, false)
		s.report = nil
	}
	if s.e.name == "message_start" || s.e.role == ending {
		return s.rewrite(messagesRewrites)
	}
	return s.e, nil
}

// settle decides what the members read add up to, once all are read: they come in any order, so the type may follow
// the stop reason, and a block's type its text.
func (s *messagesStream) settle() {
	s.e.final = s.e.final && s.e.role == ending
	if s.e.name == "content_block_start" && (s.e.content > 0 || !s.textBlock) {
		s.e.role = content
	}
	if s.serverTool && s.webSearch {
		s.e.searches = 1
	}
}

func (s *messagesStream) dropBuffers() {
	s.eventReader.dropBuffers()
	s.report = nil
}

func (s *messagesStream) eventType() error {
	v, err := s.str()
	if err != nil {
		return err
	}
	s.e.name = ""
	for _, known := range messagesEvents { // the table's own string, so a known name costs no allocation
		if known.name == string(v) {
			s.e.name, s.e.role = known.name, known.role
		}
	}
	if s.e.name == "" {
		s.e.name = string(v)
	}
	s.failed = s.e.name == "error"
	return nil
}

// stopReason reads a message_delta's stop reason: the one that has it carries the final usage (Anthropic
// 《Streaming messages》, read 2026-10-14).
func (s *messagesStream) stopReason() error {
	v, err := s.raw()
	s.e.final = v != nil
	return err
}

func (s *messagesStream) blockType() error {
	v, err := s.str()
	s.textBlock = string(v) == "text" || string(v) == "thinking"
	s.serverTool = string(v) == "server_tool_use"
	return err
}

func (s *messagesStream) toolName() error {
	is, err := s.is("web_search")
	s.webSearch = is
	return err
}

// usage decodes a usage object, which is small and comes once or twice a stream.
func (s *messagesStream) usage() error {
	v, err := s.dec.ReadValue()
	if err != nil {
		return err
	}
	s.report = &messagesUsage{}
	s.e.usage = true
	s.reportErr = jsonx.Unmarshal(v, s.report)
	return nil
}

// messagesUsage is Messages' usage object: Usage in a message, MessageDeltaUsage in a message_delta, and their
// iterations (Anthropic《Messages API reference》, read 2026-10-15;《Compaction》"Understanding usage").
type messagesUsage struct {
	Input         *int64 `json:"input_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheWrite    *int64 `json:"cache_creation_input_tokens"`
	CacheCreation *struct {
		FiveMinutes int64 `json:"ephemeral_5m_input_tokens"`
		OneHour     int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Output      *int64                    `json:"output_tokens"`
	ServerTools map[string]jsontext.Value `json:"server_tool_use"`
	ServiceTier *string                   `json:"service_tier"`
	Speed       *string                   `json:"speed"`
	Iterations  []struct {
		Type string `json:"type"`
		messagesUsage
	} `json:"iterations"`
	// Reported but not billed: where the request ran, a breakdown of output_tokens, and a relay's price for it,
	// which is not usage (OpenRouter《Usage accounting》, read 2026-10-14).
	InferenceGeo  jsontext.Value            `json:"inference_geo"`
	OutputDetails jsontext.Value            `json:"output_tokens_details"`
	Cost          jsontext.Value            `json:"cost"`
	CostDetails   jsontext.Value            `json:"cost_details"`
	IsBYOK        jsontext.Value            `json:"is_byok"`
	Unknown       map[string]jsontext.Value `json:",embed"`
}

// messagesUsageMembers are the members of a usage object the client sees: the ones messagesUsage reads but a relay's
// cost, which never reaches it (docs/domains/gateway.md「用量与费用」).
var messagesUsageMembers = keep("input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
	"cache_creation", "output_tokens", "server_tool_use", "service_tier", "speed", "iterations", "inference_geo",
	"output_tokens_details")

// meter maps the usage just read onto billing items: the reply's, and each compaction's as its own segment, since
// a compaction is a sampling of its own; message iterations are already in the totals (catalog README.md「计费项与
// 用量」). final is set for the usage of the event that ended the stream: what it states is charged as it is rather
// than estimated (meter.finalize).
func (s *messagesStream) meter(m *meter, final bool) {
	if s.reportErr != nil {
		m.invalid = fmt.Errorf("usage: %w", s.reportErr)
		return
	}
	reply, err := s.report.items(s.ctx, "usage")
	if err != nil {
		m.invalid = err
		return
	}
	var compactions []usageReport // nil unless the report lists iterations, so a report without keeps the earlier ones
	if s.report.Iterations != nil {
		compactions = []usageReport{}
	}
	for i, it := range s.report.Iterations {
		switch it.Type {
		case "message":
		case "compaction":
			c, err := it.items(s.ctx, fmt.Sprintf("usage.iterations[%d]", i))
			if err != nil {
				m.invalid = err
				return
			}
			compactions = append(compactions, c)
		default:
			m.invalid = fmt.Errorf("usage: iteration %d is of type %q", i, it.Type)
			return
		}
	}
	if final {
		m.finalize(reply, compactions)
		return
	}
	m.report(reply, compactions)
}

// items maps the usage object at path. A non-zero count it does not know goes in unknown, as the upstream wrote it.
func (u *messagesUsage) items(ctx UsageContext, path string) (r usageReport, err error) {
	for it, n := range [...]*int64{catalog.ItemInput: u.Input, catalog.ItemCacheRead: u.CacheRead, catalog.ItemOutput: u.Output} {
		if n != nil {
			r.set(catalog.Item(it), *n)
		}
	}
	if err := u.cacheWrite(ctx, &r); err != nil {
		return r, err
	}
	u.serverTools(&r, path)
	unknownCounts(&r, path, u.Unknown)
	u.serviceTier(&r, path)
	r.sortUnknown()
	return r, nil
}

// serverTools counts the server tools billed per call; web fetch, which is not, goes in unknown until the item table
// has it (catalog README.md「计费项与用量」).
func (u *messagesUsage) serverTools(r *usageReport, path string) {
	for name, v := range u.ServerTools {
		n, err := strconv.ParseInt(string(v), 10, 64)
		switch {
		case err == nil && name == "web_search_requests":
			r.set(catalog.ItemWebSearch, n)
		case err == nil && name == "code_execution_requests":
			r.set(catalog.ItemCodeExecution, n)
		case string(v) != "0" && v.Kind() != 'n':
			r.unknown = append(r.unknown, catalog.UnknownUsage{Field: path + ".server_tool_use." + name, Value: string(v)})
		}
	}
}

// serviceTier is fast mode's when the reply says so, then the reply's service_tier; without either the report leaves
// the tier out, and the request's or an earlier report's stands.
func (u *messagesUsage) serviceTier(r *usageReport, path string) {
	r.tierSet = true
	switch {
	case u.Speed != nil && *u.Speed == "fast":
		r.tier = catalog.ServiceTierFast
	case u.ServiceTier == nil:
		r.tierSet = false
	case *u.ServiceTier == "standard":
		r.tier = catalog.ServiceTierStandard
	case *u.ServiceTier == "priority":
		r.tier = catalog.ServiceTierPriority
	default:
		r.tier = catalog.ServiceTierStandard
		r.unknown = append(r.unknown, catalog.UnknownUsage{Field: path + ".service_tier", Value: *u.ServiceTier})
	}
}

// cacheWrite splits the cache write by its time to live: by the breakdown when there is one, which must add up to
// the total, and otherwise by the request's breakpoints, all of it at the dearer 1-hour price when there is a 1-hour
// one (catalog README.md「计费项与用量」).
func (u *messagesUsage) cacheWrite(ctx UsageContext, r *usageReport) error {
	c := u.CacheCreation
	switch {
	case c != nil && (!countOK(c.FiveMinutes) || !countOK(c.OneHour)), u.CacheWrite != nil && !countOK(*u.CacheWrite):
		return errors.New("usage: a cache write count is not from 0 to 10¹⁰")
	case c != nil && u.CacheWrite != nil && c.FiveMinutes+c.OneHour != *u.CacheWrite:
		return fmt.Errorf("usage: cache_creation adds up to %d, not cache_creation_input_tokens %d",
			c.FiveMinutes+c.OneHour, *u.CacheWrite)
	case c != nil:
		r.set(catalog.ItemCacheWrite, c.FiveMinutes)
		r.set(catalog.ItemCacheWrite1h, c.OneHour)
	case u.CacheWrite != nil && ctx.CacheTTL1h:
		r.set(catalog.ItemCacheWrite1h, *u.CacheWrite)
		r.set(catalog.ItemCacheWrite, 0)
	case u.CacheWrite != nil:
		r.set(catalog.ItemCacheWrite, *u.CacheWrite)
		r.set(catalog.ItemCacheWrite1h, 0)
	}
	return nil
}

var (
	// messageRules put the call ID and the client's model name in the message and keep its usage to
	// messagesUsageMembers.
	messageRules = map[string]rule{"id": (*rewriter).ourID, "model": (*rewriter).ourModel, "usage": (*rewriter).usage}
	// messagesRewrites are the rewrites of message_start and of an ending, whose axiom member an upstream that is
	// itself a gateway like ours may have written, and which our ending may need to write.
	messagesRewrites = formatRules{members: map[string]rule{
		"message": object(messageRules), "usage": (*rewriter).usage, "axiom": (*rewriter).drop,
	}}
)

// failure appends an error event (Anthropic《Streaming messages》"Error events") with our error type in
// error.error_type and the usage of the reply, with cost, which is empty or a list of members each led by a comma
// (docs/domains/gateway.md「错误」).
func (*messagesStream) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	data := fmt.Appendf(nil, `{"type":"error","error":{"type":%q,"message":`, errorTypes[e.Type].messages)
	data = append(appendErrorType(append(appendQuoted(data, e.message()), ','), e), `},"usage":`...)
	data = append(appendMessagesUsage(data, u, cost), '}')
	return sse.AppendEvent(dst, "error", data)
}

// usageEvent is never needed: message_stop follows message_delta.
func (*messagesStream) usageEvent(dst []byte, _ catalog.Usage, _, _ string) []byte { return dst }
