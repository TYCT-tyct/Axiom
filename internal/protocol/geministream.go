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
// Usage Gemini reports before the end goes on the next delta, so the relay holds it should the gateway stop reading
// there: the input side is final once reported, and the output side is reported only once Gemini counted some
// (geminiUsage.reported). Every chunk with usage names the tier Google served it at.
func ChatFromGemini(body io.ReadCloser) io.ReadCloser {
	return &chatFromGemini{src: body, events: sse.NewReader(body, 32<<20)}
}

type chatFromGemini struct {
	src      io.ReadCloser
	events   *sse.Reader
	out      []byte
	err      error
	id       string
	model    string
	calls    int
	finish   string
	usage    *geminiUsage
	report   bool // usage has a snapshot the relay was not given yet
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
		s.fail(jsonValue(502), "upstream_error", "The Gemini stream broke off before the reply finished.")
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
		s.fail(jsonValue(cmp.Or(r.Error.Code, 502)), "upstream_error", r.Error.Message)
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
			s.fail(jsonValue("content_policy_violation"), "content_policy_violation", "Gemini blocked the prompt: "+r.PromptFeedback.BlockReason)
		}
		return nil
	}
	cand := r.Candidates[0]
	if cand.Content != nil {
		if err := s.parts(cand.Content.Parts); err != nil {
			return err
		}
	}
	s.finishWith(cand.FinishReason)
	return nil
}

// finishWith records Gemini's finish reason as Chat's, or ends the stream for one Chat has no word for.
func (s *chatFromGemini) finishWith(gemini string) {
	if gemini == "" {
		return
	}
	reason, ok := geminiFinish[gemini]
	switch {
	case !ok:
		s.fail(jsonValue(502), "upstream_error", "Gemini stopped the reply: "+gemini)
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
// OpenAI SDKs require of every choice; usage Gemini reported since the last chunk goes with it.
func (s *chatFromGemini) choice(delta requestObject, finish jsonx.Raw) {
	if !s.role {
		delta["role"], s.role = jsonValue("assistant"), true
	}
	var usage jsonx.Raw
	if s.report {
		usage = s.usage.reported()
	}
	s.chunk(requestObject{"index": jsonx.Raw("0"), "delta": jsonValue(delta), "finish_reason": finish}, usage)
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

// withUsage puts usage in chunk c, with the tier Gemini served the reply at.
func (s *chatFromGemini) withUsage(c requestObject, usage jsonx.Raw) {
	if usage == nil {
		return
	}
	c["usage"], s.report = usage, false
	if t := strings.ToLower(s.usage.ServiceTier); t != "" {
		c["service_tier"] = jsonValue(cmp.Or(chatTiers[t], t))
	}
}

// reported is the usage as Chat's, its output side only once Gemini counted some output: a stream's first events count
// the prompt alone, and a report of no output would bill what was delivered after them as none.
func (u geminiUsage) reported() jsonx.Raw {
	c := u.chat()
	if u.CandidatesTokenCount == 0 && u.ThoughtsTokenCount == 0 {
		return jsonValue(c.chatInputCounts)
	}
	return jsonValue(c)
}

// end closes a stream Gemini finished: the finish reason, the usage and the end marker.
func (s *chatFromGemini) end() {
	switch {
	case s.finished:
		return
	case s.finish == "" && s.usage != nil: // cut off, but what it reported was used and is billed
		s.fail(jsonValue(502), "upstream_error", "The Gemini stream ended before the reply finished.")
		return
	case s.finish == "":
		return
	}
	s.finished = true
	s.choice(requestObject{}, jsonValue(s.finish))
	if s.usage != nil {
		s.chunk(nil, jsonValue(s.usage.chat()))
	}
	s.out = append(s.out, "data: [DONE]\n\n"...)
}

// fail ends the stream with Chat's error chunk of code and type, carrying the usage Gemini reported so far, which the
// relay bills from an error chunk as from a usage one.
func (s *chatFromGemini) fail(code jsonx.Raw, typ, message string) {
	s.finished = true
	chunk := requestObject{"error": jsonValue(requestObject{"message": jsonValue(message), "type": jsonValue(typ), "code": code})}
	if s.usage != nil {
		s.withUsage(chunk, s.usage.reported())
	}
	s.out = slices.Concat(s.out, []byte("data: "), jsonValue(chunk), []byte("\n\n"))
}
