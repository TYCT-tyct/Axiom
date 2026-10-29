package protocol_test

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
)

func TestReasoningControlsAcrossFormats(t *testing.T) {
	for _, tc := range []struct {
		name, setting, thinking, effort string
		target                          protocol.Format
		mode                            catalog.Thinking
	}{
		{"unspecified", ``, ``, ``, protocol.Messages, catalog.ThinkingAdaptive},
		{"null", `"reasoning":null`, ``, ``, protocol.Messages, catalog.ThinkingAdaptive},
		{"empty", `"reasoning":{}`, ``, ``, protocol.Messages, catalog.ThinkingAdaptive},
		{"max adaptive", `"reasoning":{"effort":"max"}`, `adaptive`, `max`, protocol.Messages, catalog.ThinkingAdaptive},
		{"xhigh adaptive", `"reasoning_effort":"xhigh"`, `adaptive`, `xhigh`, protocol.Messages, catalog.ThinkingAdaptive},
		{"max responses", `"reasoning":{"effort":"max"}`, ``, `max`, protocol.Responses, 0},
		{"xhigh responses", `"reasoning":{"effort":"xhigh"}`, ``, `xhigh`, protocol.Responses, 0},
		{"minimal responses", `"reasoning":{"effort":"minimal"}`, ``, `minimal`, protocol.Responses, 0},
		{"disabled", `"reasoning":{"enabled":false}`, `disabled`, ``, protocol.Messages, catalog.ThinkingAdaptive},
		{"disabled wins", `"reasoning":{"enabled":false,"effort":"high"}`, `disabled`, ``, protocol.Messages, catalog.ThinkingAdaptive},
		{"none", `"reasoning_effort":"none"`, `disabled`, ``, protocol.Messages, catalog.ThinkingManual},
		{"enabled", `"reasoning":{"enabled":true}`, `adaptive`, `medium`, protocol.Messages, catalog.ThinkingAdaptive},
		{"minimal adaptive", `"reasoning":{"effort":"minimal"}`, `adaptive`, `low`, protocol.Messages, catalog.ThinkingAdaptive},
		{"budget adaptive", `"reasoning":{"max_tokens":8000}`, `adaptive`, `high`, protocol.Messages, catalog.ThinkingAdaptive},
		{"disabled responses", `"reasoning":{"enabled":false}`, ``, `none`, protocol.Responses, 0},
		{"budget responses", `"reasoning":{"max_tokens":8000}`, ``, `high`, protocol.Responses, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"max_tokens":10000`)
			if tc.setting != "" {
				body = append(body, ',')
				body = append(body, tc.setting...)
			}
			body = append(body, '}')
			r, err := protocol.Inspect(protocol.Chat, body)
			if err != nil {
				t.Fatal(err)
			}
			up, err := protocol.Prepare(&r, body, protocol.Target{Format: tc.target, Model: "up", Thinking: tc.mode}, protocol.Rewrites{})
			if err != nil {
				t.Fatal(err)
			}
			checkReasoningSettings(t, up.Body, tc.target, tc.thinking, tc.effort)
		})
	}
}

func checkReasoningSettings(t *testing.T, body []byte, target protocol.Format, thinking, wantEffort string) {
	t.Helper()
	var got struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := jsonx.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	effort := got.Reasoning.Effort
	if target == protocol.Messages {
		effort = got.OutputConfig.Effort
	}
	if got.Thinking.Type != thinking || effort != wantEffort {
		t.Fatalf("got %s; want thinking=%s effort=%s", body, thinking, wantEffort)
	}
}

func TestReasoningEffortBudgets(t *testing.T) {
	for _, tc := range []struct {
		effort string
		budget int64
	}{{"minimal", 1024}, {"low", 2000}, {"medium", 5000}, {"high", 8000}, {"xhigh", 9500}, {"max", 9500}} {
		t.Run(tc.effort, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"max_tokens":10000,"reasoning":{"effort":%q,"exclude":true}}`, tc.effort))
			r, err := protocol.Inspect(protocol.Chat, body)
			if err != nil {
				t.Fatal(err)
			}
			up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Messages, Model: "up", Thinking: catalog.ThinkingManual}, protocol.Rewrites{})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Thinking struct {
					Type    string `json:"type"`
					Display string `json:"display"`
					Budget  int64  `json:"budget_tokens"`
				} `json:"thinking"`
			}
			if err := jsonx.Unmarshal(up.Body, &got); err != nil {
				t.Fatal(err)
			}
			if got.Thinking.Budget != tc.budget || got.Thinking.Display != "omitted" {
				t.Fatalf("got %s; want budget %d, omitted", up.Body, tc.budget)
			}
		})
	}
}

type reasoningSample struct {
	up                    protocol.Format
	path, secret, visible string
}

func TestHiddenReasoningRecordedStreams(t *testing.T) {
	for _, tc := range []reasoningSample{
		{protocol.Messages, "messages/thinking/upstream.sse", "I need to find the GCD", "greatest common divisor"},
		{protocol.Responses, "assembled/responses-reasoning-tool.sse", "Checking the weather", "get_weather"},
		{protocol.Chat, "assembled/chat-reasoning-cache.sse", "1071 = 2 x 462", "The GCD"},
	} {
		stream := reasoningSampleStream(t, tc)
		for _, client := range []protocol.Format{protocol.Chat, protocol.Messages, protocol.Responses} {
			if client != tc.up && client != protocol.Chat && tc.up != protocol.Chat {
				continue
			}
			for _, aggregate := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d-to-%d/aggregate=%t", tc.up, client, aggregate), func(t *testing.T) {
					checkHiddenReply(t, tc, client, stream, aggregate)
				})
			}
		}
	}
}

func reasoningSampleStream(t *testing.T, tc reasoningSample) []byte {
	t.Helper()
	stream, err := os.ReadFile("testdata/" + tc.path)
	if err != nil {
		t.Fatal(err)
	}
	if tc.up == protocol.Messages {
		// Anthropic's documentation sample omits usage. Add reports to exercise metering.
		stream = bytes.ReplaceAll(stream, []byte(`"stop_reason": null`), []byte(`"usage":{"input_tokens":52,"output_tokens":1},"stop_reason":null`))
		stream = bytes.ReplaceAll(stream, []byte(`"stop_reason": "end_turn", "stop_sequence": null}`), []byte(`"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":100}`))
	}
	return stream
}

func checkHiddenReply(t *testing.T, tc reasoningSample, client protocol.Format, stream []byte, aggregate bool) {
	t.Helper()
	shown, shownUsage, shownBytes := reasoningReply(t, client, tc.up, stream, false, aggregate)
	hidden, hiddenUsage, hiddenBytes := reasoningReply(t, client, tc.up, stream, true, aggregate)
	checkReasoningWire(t, client, hidden, aggregate)
	if !bytes.Contains(shown, []byte(tc.secret)) {
		t.Fatalf("fixture did not exercise reasoning: %s", shown)
	}
	if bytes.Contains(hidden, []byte(tc.secret)) || client == protocol.Chat && (bytes.Contains(hidden, []byte(`"reasoning"`)) || bytes.Contains(hidden, []byte(`"reasoning_content"`))) {
		t.Fatalf("reasoning leaked: %s", hidden)
	}
	if !bytes.Contains(hidden, []byte(tc.visible)) {
		t.Fatalf("visible output lost: %s", hidden)
	}
	if diff := cmp.Diff(shownUsage, hiddenUsage, cmp.AllowUnexported(catalog.Usage{})); diff != "" {
		t.Fatalf("usage changed: %s", diff)
	}
	if shownUsage.Segments[0].Usage.OutputTotal() == 0 {
		t.Fatal("fixture did not exercise billed output")
	}
	if shownBytes != hiddenBytes || hiddenBytes == 0 {
		t.Fatalf("content bounds changed: %d -> %d", shownBytes, hiddenBytes)
	}
}

func checkReasoningWire(t *testing.T, client protocol.Format, out []byte, aggregate bool) {
	t.Helper()
	if aggregate {
		checkShape(t, client, out)
		return
	}
	if err := formats[client].check(bytes.NewReader(out)); err != nil {
		t.Fatalf("invalid hidden stream: %v\n%s", err, out)
	}
}

func reasoningReply(t *testing.T, client, up protocol.Format, stream []byte, hide, aggregate bool) ([]byte, protocol.Usage, int64) {
	t.Helper()
	o := options
	o.Memory = protocol.NewMemory(64 << 20)
	o.Aggregate = aggregate
	r := protocol.NewRelay(client, up, bytes.NewReader(stream), protocol.UsageContext{HidesReasoning: hide}, o)
	defer r.Release()
	var out []byte
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, step.Out...)
		if step.Kind == protocol.StepUpstreamDone {
			break
		}
	}
	usage := r.Usage()
	content := r.Progress().ContentBytes
	out = append(out, r.Finish(settled(usage))...)
	return out, usage, content
}

func TestReasoningDisablePrecedenceAndVisibility(t *testing.T) {
	for _, tc := range []struct {
		f       protocol.Format
		setting string
		hidden  bool
	}{
		{protocol.Chat, `"reasoning":{"enabled":false,"effort":"high","exclude":true}`, true},
		{protocol.Chat, `"reasoning":{"effort":"high","enabled":false,"exclude":true}`, true},
		{protocol.Chat, `"reasoning_effort":"none","reasoning":{"effort":"high"}`, false},
		{protocol.Chat, `"reasoning":{"effort":"none"},"reasoning_effort":"high"`, false},
		{protocol.Messages, `"thinking":{"type":"disabled"},"output_config":{"effort":"max"}`, false},
		{protocol.Messages, `"output_config":{"effort":"max"},"thinking":{"type":"disabled"}`, false},
		{protocol.Responses, `"reasoning":{"effort":"none"}`, false},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			body := []byte(`{"model":"m",` + tc.setting + `}`)
			r, err := protocol.Inspect(tc.f, body)
			if err != nil {
				t.Fatal(err)
			}
			if r.Needs.Has(protocol.Reasoning) {
				t.Fatalf("disabled request requires reasoning: %s", body)
			}
			up, err := protocol.Prepare(&r, body, protocol.Target{Format: tc.f, Model: "up"}, protocol.Rewrites{})
			if err != nil {
				t.Fatal(err)
			}
			if up.Usage.HidesReasoning != tc.hidden {
				t.Fatalf("wrong visibility: %+v", up.Usage)
			}
		})
	}
}

func TestNativeReasoningRequestsToChat(t *testing.T) {
	for _, tc := range []struct {
		f               protocol.Format
		setting, effort string
		hidden          bool
	}{
		{protocol.Messages, `"thinking":{"type":"disabled"},"output_config":{"effort":"max"}`, "none", false},
		{protocol.Messages, `"thinking":{"type":"adaptive","display":"omitted"}`, "medium", true},
		{protocol.Messages, `"thinking":{"type":"enabled","budget_tokens":8000,"display":"summarized"}`, "high", false},
		{protocol.Messages, `"thinking":{"type":"enabled","budget_tokens":8000},"output_config":{"effort":"max"}`, "max", false},
		{protocol.Responses, `"reasoning":{"effort":"minimal"}`, "minimal", false},
		{protocol.Responses, `"reasoning":{"effort":"xhigh"}`, "xhigh", false},
		{protocol.Responses, `"reasoning":{"effort":"max"}`, "max", false},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			content := `"messages":[{"role":"user","content":"Hi"}],"max_tokens":10000`
			if tc.f == protocol.Responses {
				content = `"input":"Hi","max_output_tokens":10000`
			}
			body := []byte(`{"model":"m",` + content + `,` + tc.setting + `}`)
			r, err := protocol.Inspect(tc.f, body)
			if err != nil {
				t.Fatal(err)
			}
			up, err := protocol.Prepare(&r, body, protocol.Target{Format: protocol.Chat, Model: "up"}, protocol.Rewrites{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(up.Body), `"reasoning_effort":"`+tc.effort+`"`) || up.Usage.HidesReasoning != tc.hidden {
				t.Fatalf("wrong mapping: %s %+v", up.Body, up.Usage)
			}
		})
	}
}

func TestReasoningBudgetApproximation(t *testing.T) {
	for _, tc := range []struct {
		budget int
		effort string
	}{
		{100, "minimal"}, {1500, "minimal"}, {1501, "low"}, {3500, "low"}, {3501, "medium"}, {6500, "medium"}, {6501, "high"}, {8750, "high"}, {8751, "xhigh"}, {12000, "xhigh"},
	} {
		t.Run(fmt.Sprint(tc.budget), func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"reasoning":{"max_tokens":%d}}`, tc.budget)
			got, err := convertBody(protocol.Responses, body, func(tg *protocol.Target) { tg.MaxOutput = 10000 })
			if err != nil {
				t.Fatal(err)
			}
			checkReasoningSettings(t, got, protocol.Responses, "", tc.effort)
		})
	}
}

func TestReasoningVisibilityWithoutStrength(t *testing.T) {
	for _, tc := range []struct {
		target         protocol.Format
		exclude        bool
		mode           catalog.Thinking
		thinking, want string
		present        bool
	}{
		{protocol.Messages, false, catalog.ThinkingAdaptive, "", `"thinking"`, false},
		{protocol.Messages, true, catalog.ThinkingAdaptive, "", `"thinking"`, false},
		{protocol.Messages, true, catalog.ThinkingManual, "", `"thinking"`, false},
		{protocol.Messages, true, catalog.ThinkingNone, "", `"thinking"`, false},
		{protocol.Messages, true, catalog.ThinkingUnset, "", `"thinking"`, false},
		{protocol.Responses, false, catalog.ThinkingUnset, "", `"summary":"auto"`, true},
		{protocol.Responses, true, catalog.ThinkingUnset, "", `"summary"`, false},
	} {
		t.Run(fmt.Sprintf("%d/%d/exclude=%t", tc.target, tc.mode, tc.exclude), func(t *testing.T) {
			body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"Hi"}],"reasoning":{"exclude":%t}}`, tc.exclude)
			got, err := convertBody(tc.target, body, func(tg *protocol.Target) { tg.Thinking = tc.mode })
			if err != nil {
				t.Fatal(err)
			}
			checkReasoningSettings(t, got, tc.target, tc.thinking, "")
			if bytes.Contains(got, []byte(tc.want)) != tc.present {
				t.Fatalf("wrong display control: %s", got)
			}
		})
	}
}

func TestHiddenReasoningSnapshots(t *testing.T) {
	stream := []byte(`event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"r","model":"m","output":[]}}

event: response.content_part.done
data: {"type":"response.content_part.done","sequence_number":1,"output_index":0,"part":{"text":"private scratchpad","type":"reasoning_text"}}

event: response.completed
data: {"type":"response.completed","sequence_number":2,"response":{"id":"r","model":"m","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"private summary"}],"content":[{"type":"reasoning_text","text":"private scratchpad"}],"encrypted_content":"opaque"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]}],"usage":{"input_tokens":10,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":15}}}}

`)
	for _, aggregate := range []bool{false, true} {
		t.Run(fmt.Sprint(aggregate), func(t *testing.T) {
			out, usage, _ := reasoningReply(t, protocol.Responses, protocol.Responses, stream, true, aggregate)
			if bytes.Contains(out, []byte("private")) || !bytes.Contains(out, []byte("opaque")) || !bytes.Contains(out, []byte("answer")) {
				t.Fatalf("bad hidden output: %s", out)
			}
			if !bytes.Contains(out, []byte(`"content":[{"type":"reasoning_text","text":""}]`)) {
				t.Fatalf("reasoning part disappeared from snapshot: %s", out)
			}
			if usage.Segments[0].Usage.Count(catalog.ItemReasoning) != 15 {
				t.Fatalf("lost reasoning usage: %+v", usage)
			}
		})
	}
}

func TestHiddenThinkingKeepsSignature(t *testing.T) {
	stream := reasoningSampleStream(t, reasoningSample{up: protocol.Messages, path: "messages/thinking/upstream.sse"})
	for _, aggregate := range []bool{false, true} {
		out, _, _ := reasoningReply(t, protocol.Messages, protocol.Messages, stream, true, aggregate)
		if !bytes.Contains(out, []byte("EqQBCgIYAhIM1gbcDa9GJwZA2b3hGgxBdjrkzLoky3dl1pkiMOYds...")) {
			t.Fatalf("lost signature: %s", out)
		}
	}
}

func TestHiddenReasoningCanUseEntireOutputCap(t *testing.T) {
	stream := []byte(`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"private"},"finish_reason":null}]}

data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":5}}}

data: [DONE]

`)
	for _, tc := range []struct {
		client protocol.Format
		stop   string
	}{
		{protocol.Chat, `"finish_reason":"length"`},
		{protocol.Messages, `"stop_reason":"max_tokens"`},
		{protocol.Responses, `"reason":"max_output_tokens"`},
	} {
		for _, aggregate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/aggregate=%t", tc.client, aggregate), func(t *testing.T) {
				out, usage, _ := reasoningReply(t, tc.client, protocol.Chat, stream, true, aggregate)
				if bytes.Contains(out, []byte("private")) || !bytes.Contains(out, []byte(tc.stop)) {
					t.Fatalf("bad empty answer: %s", out)
				}
				if usage.Segments[0].Usage.Count(catalog.ItemReasoning) != 5 {
					t.Fatal("lost reasoning-only usage")
				}
			})
		}
	}
}

func TestHiddenReasoningCommitsOnVisibleContent(t *testing.T) {
	stream, err := os.ReadFile("testdata/assembled/chat-reasoning-cache.sse")
	if err != nil {
		t.Fatal(err)
	}
	r := protocol.NewRelay(protocol.Chat, protocol.Chat, bytes.NewReader(stream), protocol.UsageContext{HidesReasoning: true}, protocol.RelayOptions{Memory: protocol.NewMemory(1 << 20)})
	defer r.Release()
	for {
		step, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if step.Kind == protocol.StepCommit && !bytes.Contains(step.Out, []byte("The GCD")) {
			t.Fatalf("committed before visible content: %s", step.Out)
		}
		if step.Kind == protocol.StepUpstreamDone {
			break
		}
	}
}
