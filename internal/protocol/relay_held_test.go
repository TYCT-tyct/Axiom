package protocol

import (
	"bytes"
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"axiom/internal/catalog"
)

// footprint is what a value keeps reachable: the arrays of its slices by capacity, the bytes of its strings and the
// values its pointers point at, each counted once however many share it, and its maps at twice their keys and values. Numbers, booleans and
// funcs hold nothing that grows; the node's budget is shared and what a test reads from is the test's, so neither
// counts.
type footprint struct {
	seen  map[uintptr]bool
	spans [][2]uintptr
	maps  int
	kinds map[reflect.Kind]func(reflect.Value)
}

func held(v any) int {
	f := &footprint{seen: map[uintptr]bool{}}
	f.kinds = map[reflect.Kind]func(reflect.Value){
		reflect.Pointer: f.pointer, reflect.Interface: f.elem, reflect.Struct: f.fields, reflect.Array: f.elements,
		reflect.Slice: f.slice, reflect.Map: f.entries, reflect.String: f.string,
	}
	f.walk(reflect.ValueOf(v))
	slices.SortFunc(f.spans, func(a, b [2]uintptr) int { return cmp.Compare(a[0], b[0]) })
	n, end := f.maps, uintptr(0)
	for _, s := range f.spans {
		if s[1] > end {
			n += int(s[1] - max(s[0], end))
			end = s[1]
		}
	}
	return n
}

func (f *footprint) walk(v reflect.Value) {
	if walk := f.kinds[v.Kind()]; walk != nil {
		walk(v)
	}
}

func (f *footprint) span(p, n uintptr) {
	if n > 0 {
		f.spans = append(f.spans, [2]uintptr{p, p + n})
	}
}

func (f *footprint) pointer(v reflect.Value) {
	if v.IsNil() || f.seen[v.Pointer()] || v.Type() == reflect.TypeFor[*Memory]() || v.Type() == reflect.TypeFor[*bytes.Reader]() {
		return
	}
	f.seen[v.Pointer()] = true
	f.span(v.Pointer(), v.Elem().Type().Size()) // the value it points at, such as an entry of a map of pointers
	f.walk(v.Elem())
}

func (f *footprint) elem(v reflect.Value) {
	if !v.IsNil() {
		f.walk(v.Elem())
	}
}

func (f *footprint) fields(v reflect.Value) {
	for _, field := range v.Fields() {
		f.walk(field)
	}
}

func (f *footprint) elements(v reflect.Value) {
	if k := v.Type().Elem().Kind(); k != reflect.Uint8 && k != reflect.Int64 {
		for i := range v.Len() {
			f.walk(v.Index(i))
		}
	}
}

func (f *footprint) slice(v reflect.Value) {
	if v.Cap() > 0 {
		f.span(v.Pointer(), uintptr(v.Cap())*v.Type().Elem().Size())
	}
	f.elements(v)
}

func (f *footprint) entries(v reflect.Value) {
	f.maps += v.Len() * int(v.Type().Key().Size()+v.Type().Elem().Size()) * 2
	for it := v.MapRange(); it.Next(); {
		f.walk(it.Key())
		f.walk(it.Value())
	}
}

func (f *footprint) string(v reflect.Value) {
	if v.Len() > 0 {
		f.span(uintptr(v.UnsafePointer()), uintptr(v.Len()))
	}
}

// heldStream is a stream of format f that grows what a relay keeps.
type heldStream struct {
	f      Format
	stream string
	chat   bool // converted for a Chat client
}

// heldStreams grow what a relay keeps from event to event: a Chat chunk finishing many choices, many Responses output
// items, a long escaped member name, more unknown usage fields than the meter keeps, a long service tier repeated in
// every chunk; the last stream ends with a chunk that breaks after its usage was read. Converted for a Chat client: a
// tool's long input, which waits for its deltas, that input when the upstream fails right after it, and many function calls.
func heldStreams(many int) map[string]heldStream {
	chat := `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[`
	var choices, items, calls, unknown strings.Builder
	for i := range many {
		fmt.Fprintf(&calls, `data: {"type":"response.output_item.added","sequence_number":%d,"output_index":%d,`+
			`"item":{"id":"fc_%0100d","type":"function_call","call_id":"c%d","name":"f","arguments":""}}`+"\n\n", i+1, i, i, i)
		fmt.Fprintf(&choices, `%s{"index":%d,"delta":{"content":"x"},"finish_reason":"stop"}`, map[bool]string{true: ","}[i > 0], i)
		fmt.Fprintf(&items, `data: {"type":"response.output_item.added","sequence_number":%d,"output_index":%d,`+
			`"item":{"id":"m","type":"message","content":[]}}`+"\n\n", i+1, i)
	}
	for i := range 2 * maxUnknown {
		fmt.Fprintf(&unknown, chat+`],"usage":{"prompt_tokens":1,"%s_%d":1}}`+"\n\n", strings.Repeat("u", maxUnknownLen-8), i)
	}
	tiered := chat[:len(chat)-len(`"choices":[`)] + `"service_tier":"` + strings.Repeat("t", 50000) +
		`","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]}` + "\n\n"
	created := `data: {"type":"response.created","sequence_number":0,"response":{"id":"r","object":"response",` +
		`"status":"in_progress","model":"m","output":[],"usage":null}}` + "\n\n"
	completed := strings.Replace(strings.Replace(created, "response.created", "response.completed", 1), `"usage":null`,
		`"usage":{"input_tokens":1,"output_tokens":1}`, 1)
	completed = strings.Replace(completed, `"sequence_number":0`, `"sequence_number":`+strconv.Itoa(many+1), 1)
	return map[string]heldStream{
		"chat choices": {Chat, chat + choices.String() + "]}\n\n" + "data: [DONE]\n\n", false},
		"a tool's long input for chat": {Messages, `data: {"type":"message_start","message":{"id":"x","model":"m","usage":{"input_tokens":1}}}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{"a":"` +
			strings.Repeat("x", 100000) + `"}}}` + "\n\n" + `data: {"type":"content_block_stop","index":0}` + "\n\n" +
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}` + "\n\n" +
			`data: {"type":"message_stop"}` + "\n\n", true},
		"a tool's long start, then an error": {Messages, `data: {"type":"message_start","message":{"id":"x","model":"m","usage":{"input_tokens":1}}}` + "\n\n" +
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{"a":"` +
			strings.Repeat("x", 100000) + `"}}}` + "\n\n" + `data: {"type":"error","error":{"type":"api_error","message":"m"}}` + "\n\n", true},
		"function calls for chat": {Responses, created + calls.String() + completed, true},
		"responses items":         {Responses, created + items.String() + completed, false},
		"escaped name": {Messages, `data: {"type":"message_start","message":{"id":"x","model":"m","usage":{"input_tokens":1}}}` +
			"\n\n" + `data: {"type":"ping","` + strings.Repeat(`\u0061`, 20000) + `":1}` + "\n\n" +
			`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":1}}` + "\n\n" + `data: {"type":"message_stop"}` + "\n\n", false},
		"long tier, bad chunk": {Chat, tiered + tiered + chat[:len(chat)-len(`"choices":[`)] +
			`"usage":{"prompt_tokens":1,"x":` + strings.Repeat("9", 50000) + `},"choices":[1]}` + "\n\n", false},
		"unknown usage": {Chat, chat + `{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}` + "\n\n" +
			unknown.String() + "data: [DONE]\n\n", false},
	}
}

// TestRelayMemoryHeld: at the steps readHeld checks, what the relay keeps reachable is no more than it has charged, and after a
// failure what stays is the opening the failure ending writes and the meter, and, for a reply that is aggregated, what
// the aggregator holds; the meter at its bounds fits meterMemory.
func TestRelayMemoryHeld(t *testing.T) {
	t.Parallel()
	// The aggregated replies run on a tenth of the choices, items and calls: what they hold is walked at each check,
	// which costs more than a relay's own.
	for _, aggregate := range []bool{false, true} {
		for name, tc := range heldStreams(map[bool]int{false: 5000, true: 500}[aggregate]) {
			for _, broken := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s broken %t aggregated %t", name, broken, aggregate), func(t *testing.T) {
					checkHeld(t, name, tc, broken, aggregate)
				})
			}
		}
	}
}

func checkHeld(t *testing.T, name string, tc heldStream, broken, aggregate bool) {
	t.Helper()
	stream := tc.stream
	if broken {
		stream = stream[:strings.LastIndex(stream, "data:")]
	}
	client := map[bool]Format{false: tc.f, true: Chat}[tc.chat]
	r := NewRelay(client, tc.f, bytes.NewReader([]byte(stream)), UsageContext{},
		RelayOptions{ID: "id", Model: "m", Memory: NewMemory(64 << 20), Aggregate: aggregate})
	peak, err := readHeld(t, r)
	kept := int64(cap(r.out)) + meterMemory
	if aggregate {
		kept += r.agg.taken()
	}
	switch n := held(r); {
	case (broken || name == "long tier, bad chunk" || name == "a tool's long start, then an error") != (err != nil):
		t.Fatalf("error %v", err)
	case peak < 64<<10 && name != "unknown usage": // which the meter bounds
		t.Errorf("the stream kept at most %d bytes, too few to show anything; charged %d", peak, r.held)
	case err != nil && (n > int(r.held) || r.held != kept):
		t.Errorf("after the failure the relay keeps %d bytes, charged %d, out %d, aggregated %d", n, r.held, cap(r.out), kept)
	}
}

// readHeld reads r to its end, checking that it keeps no more than it charged after the steps numbered by a power of two (walking all of what it keeps
// after each of thousands of steps costs a quadratic
// time, and growth that is not charged shows at the next check), and after the last, and returns the most it kept and
// the error it ended with.
func readHeld(t *testing.T, r *Relay) (int, error) {
	t.Helper()
	peak := 0
	for i := 1; ; i++ {
		step, err := r.Next()
		last := err != nil || step.Kind == StepUpstreamDone
		if last || i&(i-1) == 0 {
			n := held(r)
			if n > int(r.held) {
				t.Fatalf("after step %d (%d) the relay keeps %d bytes, charged %d", i, step.Kind, n, r.held)
			}
			peak = max(peak, n)
		}
		if last {
			return peak, err
		}
	}
}

// TestMeterMemory: a meter at its bounds, with its copy in the progress, holds no more than meterMemory.
func TestMeterMemory(t *testing.T) {
	t.Parallel()
	report := func(prefix string) usageReport {
		var r usageReport
		total := int64(100)
		r.physicalInput, r.outputTotal = tokenTotal{total, true}, tokenTotal{total, true}
		for i := range maxUnknown {
			r.unknown = append(r.unknown, unknownAt(prefix, i))
		}
		return r
	}
	var r Relay
	compactions := make([]usageReport, maxCompactions)
	compactions[0] = report("c")
	r.meter.report(report("r"), compactions)
	r.progress.Reported, r.progress.Final = r.meter.progress()
	if r.meter.invalid != nil {
		t.Fatal(r.meter.invalid)
	}
	if n := held(&r); n > meterMemory {
		t.Errorf("the meter holds %d bytes, more than %d", n, meterMemory)
	}
	r.meter.report(report("more"), nil)
	if r.meter.invalid == nil {
		t.Error("a field past the bound was taken")
	}
}

func unknownAt(prefix string, i int) catalog.UnknownUsage {
	return catalog.UnknownUsage{Field: fmt.Sprintf("%s%0*d", prefix, maxUnknownLen-len(prefix), i), Value: strings.Repeat("9", maxUnknownLen)}
}
