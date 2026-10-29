package gateway

import (
	"fmt"
	"strings"
	"testing"

	"axiom/internal/platform/alloctest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// The three scans a call makes of its body before the first byte goes upstream (internal/gateway/README.md「性能估算」):
// Inspect reads the summary, SessionKey the first bytes for the session, and Prepare, after routing, rewrites for the
// endpoint. Prepare reuses Inspect's positions instead of parsing all of it again.

// turns repeat to make a body of any size: text, an image and a tool call of each format Prepare rewrites.
var turns = map[protocol.Format]struct{ head, turn string }{
	protocol.Messages: {
		`{"model":"m","max_tokens":1024,"stream":true,"metadata":{"user_id":"u"},"tools":[{"name":"f","input_schema":{}}],"messages":[`,
		`{"role":"user","content":[{"type":"text","text":"` + strings.Repeat("hello ", 40) + `"},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + strings.Repeat("QUJD", 100) + `"}}]},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{"q":"x"}}]},`,
	},
	protocol.Responses: {
		`{"model":"m","stream":true,"tools":[{"type":"function","name":"f"}],"input":[`,
		`{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("hello ", 40) + `"},` +
			`{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("QUJD", 100) + `"}]},` +
			`{"type":"function_call","call_id":"c","name":"f","arguments":"{\"q\":\"x\"}"},`,
	},
}

var scanSizes = []int{1 << 10, 1 << 20, 4 << 20}

// pastThePrefix are sizes beyond what SessionKey reads, where its cut-off body ends every scan the same way.
var pastThePrefix = []int{128 << 10, 1 << 20, 4 << 20}

func scanBody(f protocol.Format, size int) []byte {
	t := turns[f]
	return []byte(t.head + strings.TrimSuffix(strings.Repeat(t.turn, max(1, size/len(t.turn))), ",") + "]}")
}

// scan is what a call does with its body before routing and after it, in the order it does it.
func scan(router *routing.Router, f protocol.Format, body []byte) error {
	req, err := protocol.Inspect(f, body)
	if err != nil {
		return err
	}
	router.SessionKey(routing.AccountID{1}, &req, body)
	_, err = protocol.Prepare(&req, body, protocol.Target{Format: f, Model: "up"}, protocol.Rewrites{Identity: func(s string) string { return "h" + s }})
	return err
}

func scanRouter(tb testing.TB) *routing.Router {
	tb.Helper()
	router, err := routing.New(routing.Config{})
	if err != nil {
		tb.Fatal(err)
	}
	return router
}

// scanPart is a scan to measure: all three together, or one of them beside them.
type scanPart struct {
	name string
	do   func() error
}

func scanParts(router *routing.Router, f protocol.Format, body []byte) []scanPart {
	req, err := protocol.Inspect(f, body) // Prepare alone is measured on a summary read outside it
	if err != nil {
		panic(err)
	}
	return []scanPart{
		{"all", func() error { return scan(router, f, body) }},
		{"inspect", func() error { _, err := protocol.Inspect(f, body); return err }},
		{"prepare", func() error {
			_, err := protocol.Prepare(&req, body, protocol.Target{Format: f, Model: "up"}, protocol.Rewrites{Identity: func(s string) string { return s }})
			return err
		}},
	}
}

// BenchmarkScan measures the three scans together for bodies of 1 KiB, 1 MiB and 4 MiB, the parts of it beside them.
func BenchmarkScan(b *testing.B) {
	router := scanRouter(b)
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		for _, size := range scanSizes {
			benchScan(b, router, f, size)
		}
	}
}

func benchScan(b *testing.B, router *routing.Router, f protocol.Format, size int) {
	body := scanBody(f, size)
	for _, part := range scanParts(router, f, body) {
		b.Run(fmt.Sprintf("format=%d/size=%d/%s", f, size, part.name), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				if err := part.do(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestScanAllocationsDoNotGrow holds that the three scans together allocate a constant more than jsonx.Validate does,
// whatever the size of the body past the session prefix: the scans' own allocations do not grow with it
// (docs/performance.md「调用路径上的规则」). The measurement runs in a child process, so this binary's other goroutines
// and their collections are not counted as the scans'.
func TestScanAllocationsDoNotGrow(t *testing.T) {
	for _, f := range []protocol.Format{protocol.Messages, protocol.Responses} {
		t.Run(fmt.Sprintf("format=%d", f), func(t *testing.T) { checkScanAllocations(t, f) })
	}
}

// checkScanAllocations holds the scans' allocations beyond jsonx.Validate constant across the body sizes.
func checkScanAllocations(t *testing.T, f protocol.Format) {
	t.Helper()
	got := alloctest.Measure(t, scanAllocFunctions(t, scanRouter(t), f)...)
	var extra []float64
	for _, size := range pastThePrefix {
		extra = append(extra, got[fmt.Sprintf("scan/%d", size)].Count-got[fmt.Sprintf("validate/%d", size)].Count)
	}
	if extra[0] != extra[len(extra)-1] {
		t.Errorf("format %d: allocations beyond jsonx.Validate %v grow with the body sizes %v", f, extra, pastThePrefix)
	}
}

// scanAllocFunctions measures the scans and the jsonx.Validate beside them for each body size.
func scanAllocFunctions(t *testing.T, router *routing.Router, f protocol.Format) []alloctest.Function {
	t.Helper()
	var funcs []alloctest.Function
	for _, size := range pastThePrefix {
		body := scanBody(f, size)
		funcs = append(funcs, scanFunction(t, router, f, size, body), validateFunction(t, size, body))
	}
	return funcs
}

// scanFunction is the three scans of one body.
func scanFunction(t *testing.T, router *routing.Router, f protocol.Format, size int, body []byte) alloctest.Function {
	return alloctest.Function{Name: fmt.Sprintf("scan/%d", size), F: func() {
		if err := scan(router, f, body); err != nil {
			t.Fatal(err)
		}
	}}
}

// validateFunction is jsonx.Validate on the same body, the scans' baseline.
func validateFunction(t *testing.T, size int, body []byte) alloctest.Function {
	return alloctest.Function{Name: fmt.Sprintf("validate/%d", size), F: func() {
		if err := jsonx.Validate(body); err != nil {
			t.Fatal(err)
		}
	}}
}
