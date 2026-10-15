package protocol

import (
	"bytes"
	"encoding/json/jsontext" //nolint:depguard // as in inspect.go: events are read token by token, in place
	"fmt"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// role is what an upstream event is to the relay (README.md「转发」).
type role uint8

const (
	passive   role = iota // written in order, held until the commit point
	content               // text, reasoning or a tool call: the first is the commit point
	heartbeat             // the upstream's keep-alive
	ending                // carries the final usage; held until the settlement
	end                   // the upstream's normal end
)

// event is one upstream event as the relay sees it.
type event struct {
	role role
	name string
	// data is what the client gets in place of the event; for an ending, usageAt is where its usage object closes,
	// or -1 without one.
	data              []byte
	usageAt           int
	content, searches int64
	usage             bool // it reported usage, which the meter has taken
	final             bool // it is the format's last usage event
}

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

// messagesStream reads one Messages stream's events in place, with one decoder for all of them.
type messagesStream struct {
	reader
	src bytes.Buffer
	ctx UsageContext
	rw  rewriter // for the events whose IDs and usage the client must not see as the upstream wrote them
	e   event
	// The content block being read: whether its type holds text that may be empty, and whether it is a web search.
	textBlock, serverTool, webSearch bool
	failed                           bool // the event is the upstream's error
	report                           *messagesUsage
	reportErr                        error // the usage object is not one messagesUsage can hold
}

var (
	messagesFields = map[string]func(*messagesStream) error{
		"type":          (*messagesStream).eventType,
		"delta":         func(s *messagesStream) error { return s.object(contentFields) },
		"content_block": func(s *messagesStream) error { return s.object(blockFields) },
		"message":       func(s *messagesStream) error { return s.object(messageFields) },
		"usage":         (*messagesStream).usage,
	}
	contentFields = map[string]func(*messagesStream) error{
		"text": (*messagesStream).text, "thinking": (*messagesStream).text, "partial_json": (*messagesStream).text,
		"stop_reason": (*messagesStream).stopReason,
	}
	blockFields = map[string]func(*messagesStream) error{
		"text": (*messagesStream).text, "thinking": (*messagesStream).text, "type": (*messagesStream).blockType,
		"name": (*messagesStream).toolName, "input": (*messagesStream).input,
	}
	messageFields = map[string]func(*messagesStream) error{"usage": (*messagesStream).usage}
)

// event reads one event's data, giving any usage it reports to m.
func (s *messagesStream) event(data []byte, m *meter) (event, error) {
	s.src = *bytes.NewBuffer(data)
	if s.dec == nil {
		s.dec = jsontext.NewDecoder(&s.src)
	} else {
		s.dec.Reset(&s.src)
	}
	s.e = event{role: passive, data: data, usageAt: -1}
	s.textBlock, s.serverTool, s.webSearch, s.failed, s.report, s.reportErr = false, false, false, false, nil, nil
	if err := s.object(messagesFields); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	if s.failed {
		return event{}, fmt.Errorf("%w: an error event", ErrUpstream)
	}
	s.settle()
	if s.report != nil {
		s.meter(m)
	}
	if s.e.name == "message_start" || s.e.role == ending {
		return s.rewrite()
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

func (s *messagesStream) object(f map[string]func(*messagesStream) error) error {
	return s.eachMember(func() error {
		if read := f[string(s.name)]; read != nil {
			return read(s)
		}
		return s.skip()
	})
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

func (s *messagesStream) text() error {
	v, err := s.raw()
	s.e.content += unquotedLen(v)
	return err
}

// stopReason reads a message_delta's stop reason: the one that has it carries the final usage (Anthropic
// 《Streaming messages》, read 2026-10-14).
func (s *messagesStream) stopReason() error {
	v, err := s.raw()
	s.e.final = v != nil
	return err
}

func (s *messagesStream) input() error {
	v, err := s.dec.ReadValue()
	s.e.content += int64(len(v))
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

// unquotedLen is the length of the JSON string v once unquoted, without unquoting it; 0 for null. v is valid JSON,
// so every escape is complete and every \u surrogate is half of a pair, which counts half of the pair's 4 bytes.
func unquotedLen(v jsontext.Value) int64 {
	if len(v) < 2 {
		return 0
	}
	var n int64
	for i := 1; i < len(v)-1; i++ {
		if v[i] != '\\' {
			n++
			continue
		}
		i++
		if v[i] != 'u' {
			n++
			continue
		}
		var r rune
		for _, c := range v[i+1 : i+5] {
			r = r<<4 | rune(c&0xf+c>>6*9) // a hex digit: '0'-'9' is 0x3_, 'A'-'F' and 'a'-'f' are 0x4_ and 0x6_
		}
		i += 4
		switch {
		case r < 0x80:
			n++
		case r < 0x800, 0xd800 <= r && r < 0xe000:
			n += 2
		default:
			n += 3
		}
	}
	return n
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
var messagesUsageMembers = map[string]rule{
	"input_tokens": (*rewriter).skip, "cache_read_input_tokens": (*rewriter).skip,
	"cache_creation_input_tokens": (*rewriter).skip, "cache_creation": (*rewriter).skip, "output_tokens": (*rewriter).skip,
	"server_tool_use": (*rewriter).skip, "service_tier": (*rewriter).skip, "speed": (*rewriter).skip,
	"iterations": (*rewriter).skip, "inference_geo": (*rewriter).skip, "output_tokens_details": (*rewriter).skip,
}

// meter maps the usage just read onto billing items: the reply's, and each compaction's as its own segment, since
// a compaction is a sampling of its own; message iterations are already in the totals (catalog README.md「计费项与
// 用量」).
func (s *messagesStream) meter(m *meter) {
	if s.reportErr != nil {
		m.invalid = fmt.Errorf("usage: %w", s.reportErr)
		return
	}
	reply, err := s.report.items(s.ctx, "usage")
	if err != nil {
		m.invalid = err
		return
	}
	var compactions []usageReport
	for i, it := range s.report.Iterations {
		switch it.Type {
		case "message":
		case "compaction":
			c, err := it.items(UsageContext{CacheTTL1h: s.ctx.CacheTTL1h, Tier: reply.tier}, fmt.Sprintf("usage.iterations[%d]", i))
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
	for name, v := range u.Unknown {
		if v.Kind() == '0' && string(v) != "0" {
			r.unknown = append(r.unknown, catalog.UnknownUsage{Field: path + "." + name, Value: string(v)})
		}
	}
	u.serviceTier(ctx, &r, path)
	slices.SortFunc(r.unknown, func(a, b catalog.UnknownUsage) int { return strings.Compare(a.Field, b.Field) })
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

// serviceTier is fast mode's when the reply says so, then the reply's service_tier, then the request's.
func (u *messagesUsage) serviceTier(ctx UsageContext, r *usageReport, path string) {
	r.tier = ctx.Tier
	switch {
	case u.Speed != nil && *u.Speed == "fast":
		r.tier = catalog.ServiceTierFast
	case u.ServiceTier == nil:
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
	messageRules = map[string]rule{"id": (*rewriter).identity, "model": (*rewriter).model, "usage": (*rewriter).usage}
	// messagesRewrites are the rewrites of message_start and of an ending, whose axiom member an upstream that is
	// itself a gateway like ours may have written, and which our ending may need to write.
	messagesRewrites = formatRules{members: map[string]rule{
		"message": object(messageRules), "usage": (*rewriter).usage, "axiom": (*rewriter).drop,
	}}
)

// rewrite rewrites the event by messagesRewrites into a new array, which the relay copies before the next event.
func (s *messagesStream) rewrite() (event, error) {
	p := &s.rw
	p.dec, p.body, p.out, p.at, p.usageAt = jsontext.NewDecoder(bytes.NewBuffer(s.e.data)), s.e.data, nil, 0, -1
	if err := p.top(messagesRewrites); err != nil {
		return event{}, fmt.Errorf("%w: %w", ErrUpstream, err)
	}
	s.e.data, s.e.usageAt = p.out, p.usageAt
	return s.e, nil
}

// usage copies a usage object's messagesUsageMembers and drops the rest, null included, marking where it closes.
func (p *rewriter) usage() error {
	if ok, err := p.open('{', "an object"); !ok {
		p.at = p.offset()
		return err
	}
	if err := p.members(messagesUsageMembers, (*rewriter).drop); err != nil {
		return err
	}
	p.usageAt = len(p.out)
	return p.token()
}

// messagesError appends an error event (Anthropic《Streaming messages》"Error events") with our error type in
// error.error_type and the usage of the reply, with cost, which is empty or a list of members each led by a comma
// (docs/domains/gateway.md「错误」).
func messagesError(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	data := fmt.Appendf(nil, `{"type":"error","error":{"type":%q,"message":`, messagesErrorTypes[e.Type])
	data, _ = jsontext.AppendQuote(data, e.Message) //nolint:errcheck // invalid UTF-8 is written as U+FFFD, and that is all the error says
	data = fmt.Appendf(data, `,"error_type":%q},"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,`+
		`"cache_read_input_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d},`+
		`"output_tokens":%d,"server_tool_use":{"web_search_requests":%d}%s}}`,
		errorTypeNames[e.Type], u.Count(catalog.ItemInput),
		u.Count(catalog.ItemCacheWrite)+u.Count(catalog.ItemCacheWrite1h), u.Count(catalog.ItemCacheRead),
		u.Count(catalog.ItemCacheWrite), u.Count(catalog.ItemCacheWrite1h), u.OutputTotal(),
		u.Count(catalog.ItemWebSearch), cost)
	return sse.AppendEvent(dst, "error", data)
}

// messagesErrorTypes are the Messages error types for ours: a stream that stops for money says billing_error, as
// before it starts; the others are the server's failures.
var messagesErrorTypes = [...]string{"billing_error", "api_error", "api_error", "timeout_error"}
