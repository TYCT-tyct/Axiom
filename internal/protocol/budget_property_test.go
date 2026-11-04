package protocol

import (
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
)

// budgetSlack is what a preparation may allocate without drawing it: the decoders' own state, the errors and the
// small values whose number does not grow with the request. A site that allocates per part, per message or per byte
// without drawing passes it on every shape below of 4,000 parts or 256 KiB.
const budgetSlack = 32 << 10

// budgetCase is one preparation the budget properties run.
type budgetCase struct {
	name string
	r    Request
	body []byte
	t    Target
	w    Rewrites
}

// budgetShapes are the request shapes that cost the most per byte, written in each source format: many tiny parts and
// messages, long and escaped strings, a base64 document, deep nesting, empty objects and nulls.
func budgetShapes() map[string]map[Format]string {
	const bs = string(rune(0x5c))
	// many and size are the counts and lengths each shape takes: past what fixed allocations come to, and small enough
	// that the test runs in CI under the race detector, which slows every preparation many times over.
	const many, size = 4000, 1 << 18
	long := strings.Repeat("x", size)
	escaped := strings.Repeat(bs+"u0041", size/4)
	escapedName := strings.Repeat(bs+"u0062", size/4) // a member name the scans must not unquote // bs is the backslash: a JSON escape, not the letter
	pdf := "data:application/pdf;base64," + strings.Repeat("QUJD", size/4)
	deep := strings.Repeat(`{"type":"object","properties":{"a":`, 100) + `{"type":"string"}` + strings.Repeat(`}}`, 100)
	repeat := func(s string, n int) string { return strings.TrimSuffix(strings.Repeat(s+",", n), ",") }
	keys := make([]string, many)
	for i := range keys {
		keys[i] = fmt.Sprintf(`"k%05d":1`, i)
	}
	members := strings.Join(keys, ",")
	chat := func(messages, rest string) string { return `{"model":"m","messages":[` + messages + `]` + rest + `}` }
	messages := func(messages, rest string) string {
		return `{"model":"m","max_tokens":1024,"messages":[` + messages + `]` + rest + `}`
	}
	responses := func(input, rest string) string { return `{"model":"m","input":[` + input + `]` + rest + `}` }
	tool := func(params string) string {
		return `,"tools":[{"type":"function","function":{"name":"f","parameters":` + params + `}}]`
	}
	nativeTool := func(params string) string { return `,"tools":[{"name":"f","input_schema":` + params + `}]` }
	responsesTool := func(params string) string {
		return `,"tools":[{"type":"function","name":"f","parameters":` + params + `}]`
	}
	calls := func(call string) string {
		var b strings.Builder
		for i := range many / 4 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, call, i, i)
		}
		return b.String()
	}
	return map[string]map[Format]string{
		"tiny parts": {
			Chat:      chat(`{"role":"user","content":[`+repeat(`{"type":"text","text":"x"}`, many)+`]}`, ""),
			Messages:  messages(`{"role":"user","content":[`+repeat(`{"type":"text","text":"x"}`, many)+`]}`, ""),
			Responses: responses(`{"role":"user","content":[`+repeat(`{"type":"input_text","text":"x"}`, many)+`]}`, ""),
		},
		"tiny messages": {
			Chat:      chat(repeat(`{"role":"user","content":"a"},{"role":"assistant","content":"b"}`, many/2), ""),
			Messages:  messages(repeat(`{"role":"user","content":"a"},{"role":"assistant","content":"b"}`, many/2), ""),
			Responses: responses(repeat(`{"role":"user","content":"a"},{"role":"assistant","content":"b"}`, many/2), ""),
		},
		"long text": {
			Chat:      chat(`{"role":"system","content":"s"},{"role":"user","content":"`+long+`"}`, ""),
			Messages:  messages(`{"role":"user","content":"`+long+`"}`, ""),
			Responses: responses(`{"role":"user","content":"`+long+`"}`, ""),
		},
		"escapes": {
			Chat:      chat(`{"role":"user","content":"`+escaped+`"}`, ""),
			Messages:  messages(`{"role":"user","content":"`+escaped+`"}`, ""),
			Responses: responses(`{"role":"user","content":"`+escaped+`"}`, ""),
		},
		"base64 document": {
			Chat:      chat(`{"role":"user","content":[{"type":"file","file":{"filename":"a.pdf","file_data":"`+pdf+`"}}]}`, ""),
			Messages:  messages(`{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"`+strings.TrimPrefix(pdf, "data:application/pdf;base64,")+`"}}]}`, ""),
			Responses: responses(`{"role":"user","content":[{"type":"input_file","filename":"a.pdf","file_data":"`+pdf+`"}]}`, ""),
		},
		"deep schema": {
			Chat:      chat(`{"role":"user","content":"x"}`, tool(deep)),
			Messages:  messages(`{"role":"user","content":"x"}`, nativeTool(deep)),
			Responses: responses(`{"role":"user","content":"x"}`, responsesTool(deep)),
		},
		"tool calls": {
			Chat: chat(calls(`{"role":"assistant","content":null,"tool_calls":[{"id":"c%d","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c%d","content":"ok"}`),
				tool(`{"type":"object"}`)),
			Messages: messages(calls(`{"role":"assistant","content":[{"type":"tool_use","id":"c%d","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c%d","content":"ok"}]}`),
				nativeTool(`{"type":"object"}`)),
			Responses: responses(calls(`{"type":"function_call","call_id":"c%d","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c%d","output":"ok"}`),
				responsesTool(`{"type":"object"}`)),
		},
		"legacy functions": {
			Chat: chat(repeat(`{"role":"assistant","content":null,"function_call":{"name":"f","arguments":"{}"}},{"role":"function","name":"f","content":"ok"}`, many/4),
				`,"functions":[{"name":"f","description":"`+long+`","parameters":{"type":"object"}}]`),
		},
		"parted messages": {
			Chat:      chat(repeat(`{"role":"user","content":[{"type":"text","text":"a"}]}`, many/2), ""),
			Messages:  messages(repeat(`{"role":"user","content":[{"type":"text","text":"a"}]}`, many/2), ""),
			Responses: responses(repeat(`{"role":"user","content":[{"type":"input_text","text":"a"}]}`, many/2), ""),
		},
		"strict schema": {
			Chat: chat(`{"role":"user","content":"x"}`, `,"tools":[{"type":"function","function":{"name":"f","strict":true,"parameters":`+
				`{"type":"object","additionalProperties":false,"properties":{`+strings.ReplaceAll(members, ":1", ":{}")+`}}}},`+
				`{"type":"function","function":{"name":"g","strict":true,"parameters":`+deep+`}}]`),
		},
		"long values": {
			Chat: chat(`{"role":"user","content":"x"},{"role":"assistant","content":null,"tool_calls":[{"id":"`+strings.Repeat(".", size)+
				`","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"`+strings.Repeat(".", size)+`","content":"ok"}`,
				`,"temperature":0.`+strings.Repeat("0", size)+`5,"tools":[{"type":"function","function":{"name":"f"}}],`+
					`"tool_choice":{"type":"function","function":{"name":"`+long+`"}}`),
		},
		"wide values": {
			Chat: chat(`{"role":"user","content":"x"},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{`+
				strings.ReplaceAll(members, `"`, `\"`)+`}"}}]},{"role":"tool","tool_call_id":"c","content":"ok"}`,
				`,"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"function","function":{"name":"f"},`+members+`}`),
			Embeddings: `{"model":"m","input":"x",` + members + `}`,
		},
		"legacy choice": {
			Chat: chat(`{"role":"user","content":"x"}`, `,"functions":[{"name":"f"}],"function_call":"`+long+`"`),
		},
		"replay": {
			Messages: messages(`{"role":"user","content":"x"},{"role":"assistant","content":[{"type":"thinking","thinking":"`+long+`","signature":""},{"type":"text","text":"a"}]},{"role":"user","content":"y"}`, ""),
		},
		"replay escapes": {
			Messages: messages(`{"role":"user","content":"x"},`+
				`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"`+strings.Repeat(`\u0061`, size/4)+`"}]},{"role":"user","content":"y"},`+
				`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s","`+strings.Repeat(`\u0062`, size/4)+`":1}]},{"role":"user","content":"z"},`+
				`{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"s",`+members+`}]},{"role":"user","content":"w"}`, ""),
		},
		"empty objects": {
			Chat:      chat(repeat(`{}`, many), ""),
			Messages:  messages(repeat(`{}`, many), ""),
			Responses: responses(repeat(`{}`, many), ""),
		},
		"null elements": {
			Chat:      chat(repeat(`null`, many)+`,{"role":"assistant","tool_calls":[`+repeat(`null`, many)+`]}`, ""),
			Messages:  messages(`{"role":"user","content":[`+repeat(`null`, many)+`]}`, ""),
			Responses: responses(repeat(`null`, many), ""),
		},
		"unknown members": {
			Chat:      chat(`{"role":"user","content":"a",`+members+`}`, ","+members),
			Messages:  messages(`{"role":"user","content":"a",`+members+`}`, ""),
			Responses: responses(`{"role":"user","content":"a",`+members+`}`, ""),
		},
		"long identifier": {
			Chat:      chat(`{"role":"user","content":"a"}`, `,"user":"`+strings.Repeat(`\n`, size/2)+`"`),
			Messages:  messages(`{"role":"user","content":"a"}`, `,"metadata":{"user_id":"`+long+`"}`),
			Responses: responses(`{"role":"user","content":"a"}`, `,"user":"`+strings.Repeat(`\n`, size/2)+`"`),
		},
		"long names": {
			Chat:      chat(`{"role":"user","content":"x"}`, `,"`+long+`":1,"`+escapedName+`":2`),
			Messages:  messages(`{"role":"user","content":"x"}`, `,"`+long+`":1,"`+escapedName+`":2`),
			Responses: responses(`{"role":"user","content":"x"}`, `,"`+long+`":1,"`+escapedName+`":2`),
		},
		"forced choice": {
			Chat:      chat(`{"role":"user","content":"x"}`, `,"`+long+`":1,"tools":[],"tool_choice":"required","max_tool_calls":3`),
			Messages:  messages(`{"role":"user","content":"x"}`, `,"`+long+`":1,"tools":[],"tool_choice":{"type":"any"},"max_tool_calls":3`),
			Responses: responses(`{"role":"user","content":"x"}`, `,"`+long+`":1,"tools":[],"tool_choice":"required","max_tool_calls":3`),
		},
		"modalities": {
			Chat: chat(`{"role":"user","content":"a"}`, `,"modalities":[`+repeat(`"text"`, many)+`]`),
		},
		"nulls": {
			Chat:      chat(repeat(`{"role":"user","content":"a","name":null},{"role":"assistant","content":null,"tool_calls":null,"refusal":null}`, many/4), `,"temperature":null,"stop":null`),
			Messages:  messages(repeat(`{"role":"user","content":"a"},{"role":"assistant","content":[]}`, many/4), `,"temperature":null,"stop_sequences":null`),
			Responses: responses(repeat(`{"role":"user","content":"a"},{"type":"message","role":"assistant","content":[]}`, many/4), `,"temperature":null`),
		},
	}
}

// budgetCases are every source and target format over every shape, with and without the provider's own search
// added. A shape a pair refuses stays a case: what it allocates before the refusal is drawn too.
func budgetCases(t *testing.T, shapes map[string]map[Format]string) []budgetCase {
	t.Helper()
	var cases []budgetCase
	for _, shape := range slices.Sorted(maps.Keys(shapes)) {
		for _, from := range []Format{Chat, Messages, Responses, Embeddings} {
			if body := []byte(shapes[shape][from]); len(body) > 0 {
				cases = append(cases, shapeCases(t, shape, from, body)...)
			}
		}
	}
	return cases
}

// shapeCases are one body's cases, one for each target format, and with search for the shapes that carry it.
func shapeCases(t *testing.T, shape string, from Format, body []byte) []budgetCase {
	t.Helper()
	r, err := Inspect(from, body)
	if err != nil {
		t.Fatalf("%s from %v: %v", shape, from, err)
	}
	var cases []budgetCase
	targets := []Format{Chat, Messages, Responses}
	if from.Direct() {
		targets = []Format{from}
	}
	for _, to := range targets {
		c := budgetCase{
			name: fmt.Sprintf("%s %v to %v", shape, from, to), r: r, body: body,
			t: Target{Format: to, Model: "up", MaxOutput: 4096}, w: Rewrites{Identity: derived},
		}
		cases = append(cases, c)
		if !slices.Contains(toolShapes, shape) {
			continue
		}
		search, loop, last := c, c, c
		search.name += " searching"
		search.w.NativeSearch = 1
		loop.name += " in a server loop" // its function goes in, the client's max_tool_calls out
		loop.w.ServerLoop, loop.w.ServerFunction, loop.w.HasMaxToolCalls = true, true, true
		last.name += " at the loop's last step" // its function is gone: a choice that forces it goes too
		last.w.ServerLoop, last.w.HasMaxToolCalls = true, true
		cases = append(cases, search, loop, last)
	}
	return cases
}

// toolShapes are the shapes the provider's search and the server loop are added to: the edits splice the body's top
// level, so long bodies, many tools, long member names and a choice left forcing nothing are the ones that cost.
var toolShapes = []string{"long text", "tiny parts", "tool calls", "long names", "forced choice"}

// derived is an identifier's derived value, as long as a real one.
func derived(string) string { return "0123456789abcdef0123456789abcdef" }

func (c *budgetCase) prepare(left int64) (*budget, error) {
	b := &budget{m: NewMemory(left)}
	_, _, err := b.prepare(&c.r, c.body, c.t, c.w)
	return b, err
}

// TestPrepareAllocatesOnlyWhatItDrew: whatever a preparation allocates, it drew from the budget first. Over every
// format pair and the costliest shapes, a child process measures the bytes a budgeted preparation allocates, and they
// stay within what it drew plus budgetSlack; then the budget is set below the need at random points, and every one
// refuses with ErrMemory having allocated no more than it had drawn when it refused.
//
// Under the race detector only the refusals of two shapes are checked: what it allocates is not what the code does
// (RaceDetector), and every shape would take the package past its time limit.
func TestPrepareAllocatesOnlyWhatItDrew(t *testing.T) {
	t.Parallel()
	shapes := budgetShapes()
	if RaceDetector() { // the refusals of two shapes, every pair: the race build runs each preparation many times slower
		for _, name := range []string{"long text", "tool calls"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				checkRefusals(t, budgetPoints(t, budgetCases(t, map[string]map[Format]string{name: shapes[name]})))
			})
		}
		return
	}
	points := budgetPoints(t, budgetCases(t, shapes))
	funcs := make([]alloctest.Function, 0, len(points))
	for name, p := range points {
		funcs = append(funcs, alloctest.Function{Name: name, F: func() {
			_, _ = p.c.prepare(p.left) //nolint:errcheck // what it allocated is the measurement; the parent checks the error
		}})
	}
	measured := alloctest.Measure(t, funcs...)
	for name, p := range points {
		b, err := p.c.prepare(p.left)
		if p.left < unlimited && !errors.Is(err, ErrMemory) {
			t.Errorf("%s: a budget of %d below the peak did not refuse: %v", name, p.left, err)
		}
		if got := int64(measured[name].Bytes); got > b.drawn+budgetSlack {
			t.Errorf("%s: allocated %d bytes, drew %d (%d over)", name, got, b.drawn, got-b.drawn)
		}
	}
}

// checkRefusals holds every point below a peak to a refusal with ErrMemory; budgetPoints prepared each case in full to
// find its peak.
func checkRefusals(t *testing.T, points map[string]budgetPoint) {
	t.Helper()
	for name, p := range points {
		if p.left == unlimited {
			continue
		}
		if _, err := p.c.prepare(p.left); !errors.Is(err, ErrMemory) {
			t.Errorf("%s: a budget of %d below the peak did not refuse: %v", name, p.left, err)
		}
	}
}

// unlimited is a budget no preparation here reaches.
const unlimited = 1 << 40

// budgetPoint is one preparation of a case on a budget of left bytes.
type budgetPoint struct {
	c    *budgetCase
	left int64
}

// budgetPoints are every case on an unlimited budget and, for one that prepares, on a budget drawn at random
// below its peak, by name. The points are the same in the child process, which builds them again.
func budgetPoints(t *testing.T, cases []budgetCase) map[string]budgetPoint {
	t.Helper()
	rng := rand.New(rand.NewPCG(166, 549)) //nolint:gosec // reproducible budget points, not a secret
	points := map[string]budgetPoint{}
	for i := range cases {
		c := &cases[i]
		full, err := c.prepare(unlimited)
		if errors.Is(err, ErrMemory) {
			t.Fatalf("%s: %v", c.name, err)
		}
		points[c.name] = budgetPoint{c, unlimited}
		if err != nil || full.peak == 0 { // one that does not prepare is measured, but has no peak to sweep below
			continue
		}
		points[c.name+"/short"] = budgetPoint{c, rng.Int64N(full.peak)}
	}
	return points
}

// TestRoundedIsWhatTheRuntimeAllocates: an append to an empty slice allocates the runtime's size for what it holds, so
// its capacity is that size, which rounded must give.
func TestRoundedIsWhatTheRuntimeAllocates(t *testing.T) {
	t.Parallel()
	for n := int64(1); n <= 1<<17; n += 1 + n/512 {
		if got := cap(append([]byte(nil), make([]byte, n)...)); int64(got) != rounded(n) {
			t.Fatalf("rounded(%d) = %d, the runtime allocates %d", n, rounded(n), got)
		}
	}
}
