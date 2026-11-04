package protocol

import (
	"axiom/internal/catalog"
	"axiom/internal/platform/textshape"
)

// StepKind is what one step of a Relay asks of its caller (README.md「转发」).
type StepKind uint8

// The step kinds.
const (
	// StepProgress is a read that finished no event: nothing to write, and Progress counts the bytes that arrived.
	StepProgress StepKind = iota
	// StepBuffered is an event held back: before the commit point every event is, after it only the ending.
	StepBuffered
	// StepCommit is the commit point, the first content: Out is the events held until now and this one.
	StepCommit
	// StepData is an event to write.
	StepData
	// StepHeartbeat is the upstream's keep-alive, which resets no idle timer: Out is empty before the commit point.
	StepHeartbeat
	// StepUpstreamDone is the upstream's normal end: the caller settles, then writes what Finish returns.
	StepUpstreamDone
)

// Step is one step of a Relay. Out is valid until the next call to the Relay.
type Step struct {
	Out  []byte
	Kind StepKind
}

// Segment is the usage of one sampling the upstream bills on its own, such as a compaction, with the items the
// upstream reported exactly. Billing estimates the others.
type Segment struct {
	Usage    catalog.Usage
	Reported catalog.ItemSet
}

// Usage is an attempt's usage: the reply's segment first, then one per compaction. Invalid says why the upstream's
// report cannot be used, such as a count that went down; then no item is Reported, and billing estimates them all
// (docs/domains/billing.md「用量来源与暂估」).
type Usage struct {
	Segments []Segment
	Invalid  error
}

// Progress is what the relay has seen so far, for billing's meter after every step. ContentBytes are the text,
// reasoning and tool input of the events read, unquoted, and Text the shape of the strings among them, whose tokens
// per byte estimate the output a reply did not report; PartialBytes are the raw bytes read but not yet returned as
// an event; ToolCalls are the client tool calls begun, whose framing the estimate adds. Reported has the reply's
// counts reported so far, and Final those that are final: the input side once reported, the rest from the format's
// last usage event on; until then an output count only bounds the output from below (README.md「转发（草图）」).
type Progress struct {
	ContentBytes, PartialBytes, Searches, CodeExecutions, FileSearches, ToolCalls int64
	// ToolChecks are the client tool calls whose arguments were checked once the answer ended, and ToolErrors those
	// that were not one JSON text: what quality ranks an endpoint's tool calling by (docs/domains/quality.md「线上观测」).
	// Both are reported with the answer's ending, and not at all for an answer that stopped at its output cap.
	ToolChecks, ToolErrors int64
	Text                   textshape.Shape
	Reported               Segment
	Final                  catalog.ItemSet
}

// UsageContext carries the upstream request's metering context and the original client's reply contract.
// Tier supplies the service tier when the reply names none (catalog README.md「计费项与用量」).
type UsageContext struct {
	CacheTTL1h bool
	// ResponseHealing repairs only a non-streaming Chat JSON reply.
	ResponseHealing bool
	// HidesReasoning filters client output after the full upstream usage has been metered.
	HidesReasoning bool
	// LegacyFunctions restores the deprecated Chat reply shape for a legacy-only client.
	LegacyFunctions bool
	Tier            catalog.ServiceTier
}

// RelayOptions are the gateway's values for one attempt.
type RelayOptions struct {
	// ID is the call ID, written in place of the upstream's ID for the reply. Model is the name the client asked
	// for; ActualModel, when set, is the model that served the call, which the reply names so a models fallback is
	// visible (docs/domains/gateway.md「转发规则」).
	ID, Model, ActualModel string
	// Created is when the call started, in Unix seconds: the created of every chunk a Chat client gets from an
	// upstream of another format, which has none of its own to give.
	Created int64
	// Memory is the node's budget for what streams hold beyond their base allowance. It must not be nil.
	Memory *Memory
	// Aggregate is set for a client that did not ask for a stream: nothing is written until the end, Next gives no
	// Out, and Finish and Fail return the reply's body, whose HTTP status Status gives (README.md「非流式聚合」).
	Aggregate bool
	// Capture keeps the answer's text for a caller that judges it, which is a verification probe and nothing else: a
	// gateway call leaves it nil, so no call path allocates for it (docs/domains/quality.md「真伪检测」).
	Capture *Capture
	// Citations are the web search results the gateway supplied, which an aggregated Chat reply cites in its
	// annotations. A stream cannot know the whole message, so it carries none.
	Citations []Citation
	// Echo, when set, is the prompt text a legacy completion repeats at the start of each choice. A pointer keeps the
	// gateway's common options small; only an echo request allocates it.
	Echo *EchoPrompts
	// ZeroOutput is set when the request capped its output at 0, as legacy completions' max_tokens 0 does: a choice that
	// finishes without text is then the whole answer, not an empty response.
	ZeroOutput bool
	// Cached is set when the upstream bytes are a response-cache replay: the answer was not generated for this call, so
	// every usage count in it is zero, as OpenRouter bills a hit (docs/domains/gateway.md「响应缓存」).
	Cached bool
	// ServerTool makes the relay record the calls of the gateway's web search function. A gateway call that runs no
	// loop leaves it false, so it allocates nothing for this.
	ServerTool      bool
	legacyFunctions bool
}

// modelName is the name a reply puts in its model member: the model that served it, or the requested name when the
// gateway did not say.
func (o RelayOptions) modelName() string {
	if o.ActualModel != "" {
		return o.ActualModel
	}
	return o.Model
}
