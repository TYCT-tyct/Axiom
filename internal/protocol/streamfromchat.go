package protocol

import (
	"encoding/json/jsontext" //nolint:depguard // as in canon.go
	"fmt"
	"strconv"

	"axiom/internal/catalog"
	"axiom/internal/protocol/internal/sse"
)

// chatToNative adapts new-api relaykit/relayconvert/internal/oai_chat/
// to_oai_responses_stream_resp.go (603a5ce) and sub2api's
// backend/internal/pkg/apicompat/chatcompletions_anthropic_bridge.go (8f2cbb5).
// Chat owns metering; this state owns only the client's event lifecycle. Unlike
// the references, retained output and deferred parallel calls draw on the budget.
type chatToNative struct {
	aggBase
	hidesReasoning    bool
	inner             upstream
	client            Format
	o                 RelayOptions
	items             []chatOutput
	tools             map[int64]int
	toolIDs           map[string]bool
	active            int
	messageNext       int // first Messages block that has not closed
	seq               int64
	started, finished bool
	published         bool // at least one batch has been handed to Relay, including its buffered prelude
	reason            string
	out               []byte
	chunk             nativeChatChoice
	// composed is set when the Chat chunks are what the conversion of an upstream of another format made of its
	// events: the inner's event decides the roles and the counts, and carries the finish signal it held (README.md
	// 「跨格式的流」).
	composed bool
}

type chatOutput struct {
	kind, id, name  string
	responseID      string
	text            []byte // JSON string contents, with escapes intact
	sent            int
	started, closed bool
}

type nativeChatChoice struct {
	Index int64 `json:"index"`
	Delta struct {
		Content          jsontext.Value   `json:"content"`
		Reasoning        jsontext.Value   `json:"reasoning"`
		ReasoningContent jsontext.Value   `json:"reasoning_content"`
		Refusal          jsontext.Value   `json:"refusal"`
		Tools            []nativeChatTool `json:"tool_calls"`
	} `json:"delta"`
	Finish string `json:"finish_reason"`
}

type nativeChatTool struct {
	Index    int64
	ID, Type string
	Function nativeChatFunction
}

type nativeChatFunction struct {
	Name      string
	Arguments jsontext.Value
}

func newChatToMessages(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	return newChatToNative(Messages, u, o, charge)
}

func newChatToResponses(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	return newChatToNative(Responses, u, o, charge)
}

func newChatToNative(f Format, u UsageContext, o RelayOptions, charge func(int) error) upstream {
	inner := newChatStreamReader(u, o, charge)
	inner.converting = true
	return newChatToNativeWith(f, inner, u, o, charge)
}

// newChatToNativeWith is newChatToNative for a stream whose Chat chunks inner makes of another format's events: inner
// owns the metering, the content bound and the upstream's errors, and the chunk is what this conversion reads.
func newChatToNativeWith(f Format, inner upstream, u UsageContext, o RelayOptions, charge func(int) error) *chatToNative {
	s := &chatToNative{
		client: f, o: o, inner: inner, active: -1, hidesReasoning: u.HidesReasoning,
		tools: map[int64]int{}, toolIDs: map[string]bool{},
	}
	s.init(charge)
	return s
}

// newResponsesToMessages converts a Responses stream for a Messages client through the Chat chunks the Responses
// conversion writes (README.md「跨格式的流」): the two formats keep one mapping, so a field one of them converts is
// converted the same way whichever other format asks for it.
func newResponsesToMessages(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := newChatToNativeWith(Messages, newResponsesToChat(u, o, charge), u, o, charge)
	s.composed = true
	return s
}

// newMessagesToResponses converts a Messages stream for a Responses client, as newResponsesToMessages does for the
// other direction.
func newMessagesToResponses(u UsageContext, o RelayOptions, charge func(int) error) upstream {
	s := newChatToNativeWith(Responses, newMessagesToChat(u, o, charge), u, o, charge)
	s.composed = true
	return s
}

func (s *chatToNative) event(data []byte, m *meter) (event, error) {
	e, err := s.inner.event(data, m)
	if err != nil {
		return event{}, err
	}
	out := event{role: skipped, usageAt: -1, content: e.content, shape: e.shape, searches: e.searches, toolCalls: e.toolCalls, usage: e.usage, final: e.final}
	if s.composed {
		return s.composedEvent(e, out)
	}
	if string(data) == sse.Done {
		if !s.started || !s.finished {
			return event{}, fmt.Errorf("%w: Chat ended without a finished choice", ErrUpstream)
		}
		if err := s.completeItems(); err != nil {
			return event{}, err
		}
		out.role = end
		return out, nil
	}
	s.out = s.out[:0]
	if err := s.readChunk(data); err != nil {
		return event{}, upstreamFailure(err)
	}
	return s.publishOut(e, out), nil
}

// composedEvent is event's work for a stream another format's conversion wrote the chunks of: its role and counts are
// the inner's, and the finish signal it held back is a chunk of its own.
func (s *chatToNative) composedEvent(e, out event) (event, error) {
	if e.role == heartbeat {
		out.role = heartbeat
		return out, nil
	}
	s.out = s.out[:0]
	if e.role == end {
		return s.composedEnd(e, out)
	}
	if err := s.readChunks(e.data, e.withheld); err != nil {
		return event{}, upstreamFailure(err)
	}
	return s.publishOut(e, out), nil
}

// composedEnd closes a composed stream: the finish signal the conversion held to the end is read, then the items close.
func (s *chatToNative) composedEnd(e, out event) (event, error) {
	if err := s.readChunks(nil, e.withheld); err != nil {
		return event{}, upstreamFailure(err)
	}
	if !s.started || !s.finished {
		return event{}, fmt.Errorf("%w: the stream ended without a finished choice", ErrUpstream)
	}
	if err := s.completeItems(); err != nil {
		return event{}, err
	}
	out.role = end
	return out, nil
}

// readChunks reads the conversion's chunk, if any, then the finish signal it holds after the chunk that carried it.
func (s *chatToNative) readChunks(data, withheld []byte) error {
	for _, chunk := range [][]byte{data, withheld} {
		if len(chunk) > 0 {
			if err := s.readChunk(chunk); err != nil {
				return err
			}
		}
	}
	return nil
}

// publishOut hands the data the chunks wrote to the client, counted as content when the upstream's event was.
func (s *chatToNative) publishOut(e, out event) event {
	if len(s.out) > 0 {
		s.published = true
		out.data, out.framed, out.role = s.out, true, passive
		if e.content > 0 {
			out.role = content
		}
	}
	return out
}

func (s *chatToNative) choice(choice nativeChatChoice) error {
	if choice.Index != 0 || s.finished {
		return fmt.Errorf("%w: extra choice or content after Chat finish", ErrUpstream)
	}
	if !s.started {
		if err := s.start(); err != nil {
			return err
		}
	}
	if err := s.choiceText(choice); err != nil {
		return err
	}
	for _, call := range choice.Delta.Tools {
		if call.Type != "" && call.Type != "function" {
			return fmt.Errorf("%w: non-function Chat tool", ErrUpstream)
		}
		if err := s.toolDelta(call.Index, call.ID, call.Function.Name, call.Function.Arguments); err != nil {
			return err
		}
	}
	if choice.Finish != "" {
		if chatMessagesStop(choice.Finish, false) == "" {
			return fmt.Errorf("%w: unknown Chat finish reason %q", ErrUpstream, choice.Finish)
		}
		s.finished, s.reason = true, choice.Finish
	}
	return nil
}

func (s *chatToNative) choiceText(choice nativeChatChoice) error {
	d := choice.Delta
	for _, text := range []struct {
		kind string
		v    jsontext.Value
	}{{"thinking", given(d.Reasoning, d.ReasoningContent)}, {"text", d.Content}, {"refusal", d.Refusal}} {
		if !isNull(text.v) && string(text.v) != `""` {
			if err := s.textDelta(text.kind, text.v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *chatToNative) addOutput(kind, id, name string) (int, error) {
	// The final Responses snapshot materializes a map and encoded item per entry;
	// empty tools still need that room, independent of their argument byte count.
	if err := s.take(len(id)+len(name)+len(s.o.ID)+20+2048, 2); err != nil {
		return 0, err
	}
	items, err := grow(&s.aggBase, s.items, 1, 2)
	if err != nil {
		return 0, err
	}
	item := chatOutput{kind: kind, id: id, name: name}
	if s.client == Responses {
		item.responseID = s.o.ID + "_" + strconv.Itoa(len(items))
	}
	s.items = append(items, item)
	return len(s.items) - 1, nil
}

func (s *chatToNative) textDelta(kind string, v jsontext.Value) error {
	i := s.active
	if i < 0 || s.items[i].kind != kind {
		var err error
		i, err = s.addOutput(kind, "", "")
		if err != nil {
			return err
		}
		s.active = i
	}
	return s.appendDelta(i, v)
}

func (s *chatToNative) toolDelta(index int64, id, name string, v jsontext.Value) error {
	i, exists := s.tools[index]
	if !exists {
		if index != int64(len(s.tools)) || id == "" || name == "" || s.toolIDs[id] {
			return fmt.Errorf("%w: Chat tool starts without consecutive index, id or name", ErrUpstream)
		}
		var err error
		i, err = s.addOutput("tool_use", id, name)
		if err != nil {
			return err
		}
		s.tools[index] = i
		s.toolIDs[id] = true
	} else if id != "" && id != s.items[i].id || name != "" && name != s.items[i].name {
		return fmt.Errorf("%w: Chat tool changed identity", ErrUpstream)
	}
	s.active = i
	return s.appendDelta(i, v)
}

func (s *chatToNative) appendDelta(i int, v jsontext.Value) error {
	it := &s.items[i]
	if !isNull(v) {
		if err := s.add(&it.text, v, 8); err != nil {
			return err
		}
	}
	if s.client == Responses {
		return s.responseDelta(i)
	}
	return s.messagesDelta(i)
}

func (s *chatToNative) emit(name string, members requestObject) error {
	members["type"] = jsonValue(name)
	if s.client == Responses {
		members["sequence_number"] = jsonValue(s.seq)
		s.seq++
	}
	// Account before serializing; the object consists only of retained raw JSON,
	// indexes and fixed field names. The output buffer is reused between chunks.
	size := 128 + len(name)
	for key, value := range members {
		size += len(key) + len(value) + 8
	}
	var err error
	s.out, err = grow(&s.aggBase, s.out, size+len(name)+16, 2)
	if err != nil {
		return err
	}
	s.out = sse.AppendEvent(s.out, name, jsonValue(members))
	return nil
}

func (s *chatToNative) start() error {
	s.started = true
	if s.client == Messages {
		return s.emit("message_start", requestObject{"message": jsonValue(requestObject{
			"id": jsonValue(s.o.ID), "type": jsonValue("message"), "role": jsonValue("assistant"), "model": jsonValue(s.o.modelName()),
			"content": jsontext.Value("[]"), "stop_reason": jsontext.Value("null"), "stop_sequence": jsontext.Value("null"),
			"usage": jsontext.Value(`{"input_tokens":0,"output_tokens":0}`),
		})})
	}
	return s.emit("response.created", requestObject{"response": s.response("in_progress", nil)})
}

// completeItems sends what the finish signals held back: every block or item still open, and the arguments of a tool
// call that streamed none. Nothing is written here directly: the frames wait in out for the settlement, whose capacity
// was charged as they were made.
func (s *chatToNative) completeItems() error {
	s.out = s.out[:0]
	for i := range s.items {
		if err := s.completeItem(i); err != nil {
			return err
		}
	}
	return nil
}

func (s *chatToNative) completeItem(i int) error {
	if err := s.completeTool(i); err != nil {
		return err
	}
	if s.client != Messages {
		return s.closeResponseItem(i)
	}
	if err := s.messagesDelta(i); err != nil {
		return err
	}
	return s.closeMessageBlock(i)
}

// completeTool closes a tool call's arguments: an empty object when nothing streamed, and for Messages the check that
// they parse as JSON, since Anthropic clients read the block's input as one.
func (s *chatToNative) completeTool(i int) error {
	it := &s.items[i]
	if it.kind != "tool_use" {
		return nil
	}
	if len(it.text) == 0 {
		it.text = append(it.text, "{}"...)
		return nil
	}
	if s.client != Messages || s.reason == "length" {
		return nil
	}
	if _, err := arguments(quotedContents(it.text)); err != nil {
		return &faultError{fault: FaultToolInput, detail: err.Error()}
	}
	return nil
}

func (s *chatToNative) dropBuffers() {
	s.inner.dropBuffers()
	s.items, s.tools, s.toolIDs, s.out = nil, nil, nil, nil
	s.reader = reader{}
	s.chunk = nativeChatChoice{}
}

func (s *chatToNative) failure(dst []byte, e Error, u catalog.Usage, cost string) []byte {
	if s.client == Messages {
		return (*messagesStream)(nil).failure(dst, e, u, cost)
	}
	writer := &responsesStream{seq: s.seq - 1, created: s.published}
	writer.id, writer.model = strconv.Quote(s.o.ID), strconv.Quote(s.o.modelName())
	return writer.failure(dst, e, u, cost)
}

func (s *chatToNative) usageEvent(dst []byte, u catalog.Usage, cost, top string) []byte {
	dst = append(dst, s.out...)
	if s.client == Messages {
		reason := chatMessagesStop(s.reason, len(s.tools) > 0)
		data := appendQuoted([]byte(`{"type":"message_delta","delta":{"stop_reason":`), reason)
		data = append(data, `,"stop_sequence":null},"usage":`...)
		data = append(appendMessagesUsage(data, u, cost), top+"}"...)
		dst = sse.AppendEvent(dst, "message_delta", data)
		return sse.AppendEvent(dst, "message_stop", []byte(`{"type":"message_stop"}`))
	}
	status := "completed"
	if inverseName(responsesIncomplete, strconv.Quote(s.reason)) != "" {
		status = "incomplete"
	}
	name := "response." + status
	data := fmt.Appendf(nil, `{"type":%q,"sequence_number":%d,"response":`, name, s.seq)
	data = append(data, s.response(status, appendOpenAIUsage(nil, responsesNames, u, cost))...)
	return sse.AppendEvent(dst, name, append(data, top+"}"...))
}

// Adapted from sub2api chatcompletions_anthropic_bridge.go (8f2cbb5): compatible providers may finish tools with stop.
// stop and length have multiple inverse names; keep their canonical Messages meanings independent of map order.
func chatMessagesStop(reason string, tools bool) string {
	switch reason {
	case "stop":
		if tools {
			return "tool_use"
		}
		return "end_turn"
	case "length":
		return "max_tokens"
	default:
		return inverseName(messagesFinish, strconv.Quote(reason))
	}
}
