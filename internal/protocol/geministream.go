package protocol

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol/internal/sse"
)

// geminiStreamResponse is one event of streamGenerateContent?alt=sse, or the error Google ends a stream with.
type geminiStreamResponse struct {
	Candidates []struct {
		Content      *geminiContent `json:"content"`
		FinishReason string         `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *geminiUsage `json:"usageMetadata"`
	ModelVersion  string       `json:"modelVersion"`
	ResponseID    string       `json:"responseId"`
	Error         *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// ChatFromGemini reads the body of Google's streamGenerateContent?alt=sse as the Chat stream the relay reads from a Chat
// endpoint: text and thoughts as deltas, the first naming the assistant's role, each function call whole with its
// thought signature, the finish reason, the usage chunk and the end marker; a Gemini error or a reason Chat has no word
// for ends it as an error chunk, and a prompt Gemini blocked as a refusal by content policy. A stream that stops before
// Gemini finished ends without the marker, which the relay reads as cut off.
//
// Usage is final only in the event that finished the reply. What Gemini reports before it goes on the next delta as
// the input side alone, which the relay holds should the gateway stop reading there, and which the stream's later
// counts cannot change; the output counted so far is no final count, so a stream that ends early has its output
// estimated from what it delivered. Every chunk with usage names the tier Google served the reply at: the reply's
// own, or tier, the one Google's x-gemini-service-tier response header named, which the first delta carries even
// before any count, so a request Google served below the tier it asked for is billed at the tier it got.
func ChatFromGemini(body io.ReadCloser, tier string) io.ReadCloser {
	return &chatFromGemini{src: body, events: sse.NewReader(body, 32<<20), tier: tier, report: tier != ""}
}

type chatFromGemini struct {
	src      io.ReadCloser
	events   *sse.Reader
	out      []byte
	err      error
	id       string
	model    string
	tier     string // the tier Google's response header named
	calls    int
	finish   string
	usage    *geminiUsage
	report   bool // usage has a snapshot the relay was not given yet, or tier has not been given
	final    bool // usage came with or after the event that ended the reply, so its output is the reply's
	role     bool // a delta named the role
	finished bool
}

func (s *chatFromGemini) Read(p []byte) (int, error) {
	for len(s.out) == 0 && s.err == nil {
		data, err := s.events.Next()
		switch {
		case errors.Is(err, io.EOF):
			s.end()
			s.err = io.EOF
		case err != nil:
			s.broke(err)
		default:
			if err := s.event(data); err != nil {
				s.broke(err)
			}
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	if len(s.out) > 0 {
		return n, nil
	}
	return n, s.err
}

func (s *chatFromGemini) Close() error { return s.src.Close() }

// broke ends a stream that failed to read, or read something that is not Gemini's: what Gemini reported before it is
// sent on the error chunk, which the relay bills, and the failure follows.
func (s *chatFromGemini) broke(err error) {
	if !s.finished && s.usage != nil {
		s.fail(jsonValue(502), "upstream_error", "The Gemini stream broke off before the reply finished.", false)
	}
	s.err = err
}

func (s *chatFromGemini) event(data []byte) error {
	if s.finished {
		return nil // what comes after the end is not read
	}
	var r geminiStreamResponse
	if err := jsonx.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("%w: %s", errGeminiStream, bodyFault(err))
	}
	if r.Error != nil {
		s.fail(jsonValue(cmp.Or(r.Error.Code, 502)), "upstream_error", r.Error.Message, false)
		return nil
	}
	s.id, s.model = cmp.Or(s.id, r.ResponseID), cmp.Or(s.model, r.ModelVersion)
	if r.UsageMetadata != nil {
		s.usage, s.report = r.UsageMetadata, true
	}
	if len(r.Candidates) > 1 {
		return fmt.Errorf("%w: more than one candidate", errGeminiStream)
	}
	if len(r.Candidates) == 0 {
		// A prompt Gemini blocked has no candidate (Google《Generating content》PromptFeedback): the content policy
		// refused the request, which Chat says as an error of that class, not as an empty answer to retry elsewhere.
		if r.PromptFeedback != nil && r.PromptFeedback.BlockReason != "" {
			s.fail(jsonValue("content_policy_violation"), "content_policy_violation", "Gemini blocked the prompt: "+r.PromptFeedback.BlockReason, r.UsageMetadata != nil)
		}
		s.counted(r.UsageMetadata != nil)
		return nil
	}
	cand := r.Candidates[0]
	if cand.Content != nil {
		if err := s.parts(cand.Content.Parts); err != nil {
			return err
		}
	}
	s.finishWith(cand.FinishReason, r.UsageMetadata != nil)
	s.counted(r.UsageMetadata != nil)
	return nil
}

// counted records that the usage is the reply's whole when an event at or after the one that ended it reported usage.
func (s *chatFromGemini) counted(reported bool) {
	s.final = s.final || reported && s.finish != ""
}

// finishWith records Gemini's finish reason as Chat's, or ends the stream for one Chat has no word for; counted is
// whether the event that gave it reported usage. CONTINUATION is a reply that reached one request's output cap with
// more to say (Google《Long decode》), which only models that are not called natively give (geminiNative): it ends at
// the cap, as length does, rather than as a failure.
func (s *chatFromGemini) finishWith(gemini string, counted bool) {
	if gemini == "" {
		return
	}
	reason, ok := geminiFinish[gemini]
	switch {
	case gemini == "CONTINUATION":
		s.finish = "length"
	case !ok:
		s.fail(jsonValue(502), "upstream_error", "Gemini stopped the reply: "+gemini, counted)
	case reason == "stop" && s.calls > 0:
		s.finish = "tool_calls"
	default:
		s.finish = reason
	}
}

func (s *chatFromGemini) parts(parts []geminiPart) error {
	for _, p := range parts {
		delta := requestObject{}
		switch {
		case p.FunctionCall != nil:
			delta["tool_calls"] = jsonValue([]requestObject{s.call(p)})
		case p.Text != nil && p.Thought:
			delta["reasoning_content"] = jsonValue(*p.Text)
		case p.Text != nil:
			delta["content"] = jsonValue(*p.Text)
		case p.InlineData != nil || p.FileData != nil || p.FunctionResponse != nil || len(p.Unknown) > 0:
			return fmt.Errorf("%w: a Gemini reply part Chat has no counterpart for", ErrUpstream)
		default:
			continue // a part holding only a signature, which Chat's text needs no carrier for
		}
		s.choice(delta, jsonx.Raw("null"))
	}
	return nil
}

// choice appends the chunk of the one choice with delta and finish, the first naming the assistant's role, which the
// OpenAI SDKs require of every choice; the input Gemini reported since the last chunk, or the tier alone, goes with it.
func (s *chatFromGemini) choice(delta requestObject, finish jsonx.Raw) {
	if !s.role {
		delta["role"], s.role = jsonValue("assistant"), true
	}
	var usage jsonx.Raw
	if s.report {
		usage = s.input()
	}
	s.chunk(requestObject{"index": jsonx.Raw("0"), "delta": jsonValue(delta), "finish_reason": finish}, usage)
}

// input is the input side of the usage Gemini reported so far, or an empty usage to carry the tier before any count.
func (s *chatFromGemini) input() jsonx.Raw {
	if s.usage == nil {
		return jsonx.Raw("{}")
	}
	return jsonValue(s.usage.chat().chatInputCounts)
}

// call is a function call part as the Chat tool call that starts and finishes in one delta.
func (s *chatFromGemini) call(p geminiPart) requestObject {
	id := p.FunctionCall.ID
	if id == "" {
		id = "call_" + strconv.Itoa(s.calls) // unique within the reply, which is all a Chat call ID promises
		if s.id != "" {
			id = "call_" + s.id + "_" + strconv.Itoa(s.calls)
		}
	}
	call := requestObject{
		"index": jsonValue(s.calls), "id": jsonValue(id), "type": jsonValue("function"),
		"function": jsonValue(requestObject{"name": jsonValue(p.FunctionCall.Name), "arguments": jsonValue(string(geminiArgs(string(cmpOrValue(p.FunctionCall.Args)))))}),
	}
	if p.ThoughtSignature != "" {
		call["extra_content"] = googleExtra(p.ThoughtSignature)
	}
	s.calls++
	return call
}

func cmpOrValue(v jsonx.Raw) jsonx.Raw {
	if isNull(v) {
		return jsonx.Raw("{}")
	}
	return v
}

// chunk appends one Chat chunk with the choices given, and the usage given with the tier it was served at.
func (s *chatFromGemini) chunk(choice requestObject, usage jsonx.Raw) {
	c := requestObject{
		"id": jsonValue(cmp.Or(s.id, "gemini")), "object": jsonValue("chat.completion.chunk"),
		"created": jsonx.Raw("0"), "model": jsonValue(s.model), "choices": jsonx.Raw("[]"),
	}
	if choice != nil {
		c["choices"] = jsonValue([]requestObject{choice})
	}
	s.withUsage(c, usage)
	s.out = slices.Concat(s.out, []byte("data: "), jsonValue(c), []byte("\n\n"))
}

// chatTiers are Gemini's service tiers whose Chat name is another: Gemini's unspecified tier is its standard one, which
// is Chat's default. Flex and priority are the same words, and a tier of another name goes on as it is, which the relay
// reports as unknown.
var chatTiers = map[string]string{"unspecified": "default", "standard": "default"}

// ChatTier is a service tier as Gemini names it, in its usage or its x-gemini-service-tier response header, under
// Chat's name for it; empty for none.
func ChatTier(gemini string) string {
	t := strings.ToLower(gemini)
	return cmp.Or(chatTiers[t], t)
}

// withUsage puts usage in chunk c, with the tier Gemini served the reply at: the reply's own, else the header's.
func (s *chatFromGemini) withUsage(c requestObject, usage jsonx.Raw) {
	if usage == nil {
		return
	}
	c["usage"], s.report = usage, false
	t := s.tier
	if s.usage != nil && s.usage.ServiceTier != "" {
		t = s.usage.ServiceTier
	}
	if t != "" {
		c["service_tier"] = jsonValue(ChatTier(t))
	}
}

// end closes a stream Gemini finished: the finish reason, the usage and the end marker. The usage is all of Gemini's
// only when it came with the event that ended the reply or after it; else the output counted before it is not the
// output delivered, and only the input side is reported.
func (s *chatFromGemini) end() {
	switch {
	case s.finished:
		return
	case s.finish == "" && s.usage != nil: // cut off, but what it reported was used and is billed
		s.fail(jsonValue(502), "upstream_error", "The Gemini stream ended before the reply finished.", false)
		return
	case s.finish == "":
		return
	}
	s.finished = true
	s.choice(requestObject{}, jsonValue(s.finish))
	switch {
	case s.usage != nil && s.final:
		s.chunk(nil, jsonValue(s.usage.chat()))
	case s.usage != nil:
		s.chunk(nil, s.input())
	}
	s.out = append(s.out, "data: [DONE]\n\n"...)
}

// fail ends the stream with Chat's error chunk of code and type, carrying the usage Gemini reported, which the relay
// bills from an error chunk as final: all of it when Gemini ended the reply in the event that failed it, else only
// the input side, as the output counted so far is not the output delivered.
func (s *chatFromGemini) fail(code jsonx.Raw, typ, message string, ended bool) {
	s.finished = true
	chunk := requestObject{"error": jsonValue(requestObject{"message": jsonValue(message), "type": jsonValue(typ), "code": code})}
	switch {
	case s.usage != nil && ended:
		s.withUsage(chunk, jsonValue(s.usage.chat()))
	case s.usage != nil || s.report:
		s.withUsage(chunk, s.input())
	}
	s.out = slices.Concat(s.out, []byte("data: "), jsonValue(chunk), []byte("\n\n"))
}
