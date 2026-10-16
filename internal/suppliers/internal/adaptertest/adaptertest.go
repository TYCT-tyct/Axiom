// Package adaptertest is the contract suite every adapter runs: the rules of package adapter that a type signature
// cannot state. An adapter's own tests add its recorded samples; this suite holds what is true of all adapters, such
// as that a response no rule recognises is never treated as free.
package adaptertest

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"axiom/internal/suppliers/internal/adapter"
)

// Run fails t for every rule a breaks.
func Run(t testing.TB, a adapter.Adapter) {
	t.Helper()
	for _, problem := range Problems(a) {
		t.Error(problem)
	}
}

// Problems lists the rules a breaks, each in a sentence naming the input.
func Problems(a adapter.Adapter) []string {
	problems := declarationProblems(a)
	q := a.Calls()
	problems = append(problems, classifyProblems(q)...)
	return append(problems, quirkProblems(q)...)
}

func declarationProblems(a adapter.Adapter) (problems []string) {
	if !a.Kind().Valid() {
		problems = append(problems, fmt.Sprintf("kind %q cannot be stored in suppliers.kind", a.Kind()))
	}
	if err := adapter.ValidateBuckets(a.Buckets()); err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

const htmlPage = "<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>blocked</body></html>"

// bodies are what Classify is given besides the supplier's own errors: nothing, truncated and oversized bodies, an
// error page, and an error that is well formed but means nothing to the adapter.
var bodies = map[string][]byte{
	"no body":       nil,
	"truncated":     []byte(`{"error":{"code":"x","mess`),
	"unrecognised":  []byte(`{"error":{"code":"never_heard_of_it","message":"?"}}`),
	"oversized":     []byte(strings.Repeat("x", 64<<10)),
	"an error page": []byte(htmlPage),
	// answers that name a known refusal: whatever the code, a 5xx must stay "may have run" (the 5xx rule below)
	"model not found":    []byte(`{"error":{"code":"model_not_found"}}`),
	"context too long":   []byte(`{"error":{"code":"context_length_exceeded"}}`),
	"insufficient funds": []byte(`{"code":"INSUFFICIENT_BALANCE"}`),
}

var headers = map[string]http.Header{
	"no headers":        nil,
	"retry after":       {"Retry-After": {"7"}},
	"bad retry after":   {"Retry-After": {"soon"}},
	"negative retry":    {"Retry-After": {"-5"}},
	"retry after date":  {"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}},
	"hostile ref value": {"X-Oneapi-Request-Id": {strings.Repeat("\x00", 8)}},
}

// classifyProblems runs Classify over every status that is an error and every body and header set above. Each rule is
// reported once, with the first input that broke it.
func classifyProblems(q adapter.CallQuirks) (problems []string) {
	reported := map[string]bool{}
	for status := 400; status < 600; status++ {
		for bodyName, body := range bodies {
			for headerName, header := range headers {
				for _, rule := range classifyOne(q, status, header, body, bodyName == "an error page") {
					if !reported[rule] {
						reported[rule] = true
						problems = append(problems, fmt.Sprintf("Classify(%d, %s, %s) %s", status, headerName, bodyName, rule))
					}
				}
			}
		}
	}
	return problems
}

func classifyOne(q adapter.CallQuirks, status int, h http.Header, body []byte, isPage bool) (problems []string) {
	defer func() {
		if r := recover(); r != nil {
			problems = []string{"panics"}
		}
	}()
	f := q.Classify(status, h, body)
	if f.Cause == nil {
		return []string{"returns no cause"}
	}
	problems = causeProblems(f, status)
	if f.Scope > adapter.ScopeSupplier || f.Executed > adapter.NotExecuted {
		problems = append(problems, "has a scope or an execution outside the defined values")
	}
	if status >= 500 && f.Executed != adapter.ExecutionUnknown {
		problems = append(problems, "treats a 5xx as not executed: the supplier may have run the request")
	}
	if isPage && (f.Cause != adapter.Unknown{Status: status, SuspectedEgressBlock: true} || f.Scope != adapter.ScopeEgress) {
		problems = append(problems, "does not take an HTML page for a block at the egress")
	}
	return problems
}

// causeProblems are the rules that depend on which cause an adapter chose.
func causeProblems(f adapter.Failure, status int) (problems []string) {
	switch c := f.Cause.(type) {
	case adapter.Transport:
		problems = append(problems, "returns Transport, which only the shared call code sets")
	case adapter.Unknown:
		if c.Status != status {
			problems = append(problems, "gives an Unknown that does not carry the response's status")
		}
		if f.Executed != adapter.ExecutionUnknown {
			problems = append(problems, "takes a response it does not recognise for not executed")
		}
	case adapter.RateLimited:
		if c.RetryAfter < 0 {
			problems = append(problems, "has a negative Retry-After")
		}
	case adapter.Overloaded, adapter.UpstreamError, adapter.SupplierBalanceLow, adapter.CredentialQuotaExhausted,
		adapter.CredentialRejected, adapter.NotPermitted, adapter.ContextTooLong, adapter.BadRequest,
		adapter.ContentRejected, adapter.ModelNotFound:
	}
	return problems
}

// quirkProblems checks the other CallQuirks on input that is not what the supplier sends.
func quirkProblems(q adapter.CallQuirks) (problems []string) {
	defer func() {
		if r := recover(); r != nil {
			problems = append(problems, fmt.Sprintf("panics on unexpected facts or headers: %v", r))
		}
	}()
	if ref, ok := q.ChargeRef(http.Header{}); ok {
		problems = append(problems, fmt.Sprintf("ChargeRef finds %q in no headers", ref))
	}
	for _, h := range headers {
		q.ChargeRef(h) // must not panic on hostile values; the result is the adapter's own business
	}
	for _, facts := range []adapter.GroupFacts{nil, []byte(`{`), []byte(`{}`), []byte(`[1]`), []byte(`null`)} {
		seen := map[string]bool{}
		for _, f := range q.Formats(facts) {
			key := fmt.Sprint(f)
			if seen[key] {
				problems = append(problems, fmt.Sprintf("Formats(%q) lists %s twice", facts, key))
			}
			seen[key] = true
		}
	}
	return problems
}

// RunScan fails t if ScanProblem finds one.
func RunScan(t testing.TB, m adapter.Manager) {
	t.Helper()
	if problem := ScanProblem(m); problem != "" {
		t.Fatal(problem)
	}
}

// ScanProblem checks the scan protocol on a Manager whose supplier has no charge records, and describes the first rule
// it breaks, or returns "": the first call has no position and is not refused; each call returns a position; a pass
// ends (Done) within a few calls; and the position of a finished pass, which the module's next round gives back, is
// accepted again; and a page that says Done also says how far the records are read (Through), because an adapter that
// leaves it out would have a removal take a backlog for read (#109). The module's sync relies on all of it: it
// starts with no position, stores what comes back with each page, and reads until Done.
func ScanProblem(m adapter.Manager) string {
	var state adapter.ScanState
	for call := range 20 {
		page, next, err := m.Charges(context.Background(), state)
		switch {
		case err != nil:
			return fmt.Sprintf("Charges call %d: %v; the first call has no position and every later one has the last returned", call+1, err)
		case len(next) == 0:
			return fmt.Sprintf("Charges call %d returned no position to keep", call+1)
		case len(page.Charges) != 0:
			return fmt.Sprintf("Charges call %d returned records from a supplier that has none", call+1)
		}
		state = next
		if !page.Done {
			continue
		}
		if page.Through.IsZero() {
			return fmt.Sprintf("Charges call %d ended the pass without saying how far the records are read (Through)", call+1)
		}
		again, after, err := m.Charges(context.Background(), state)
		if err != nil || len(after) == 0 || len(again.Charges) != 0 {
			return fmt.Sprintf("Charges with the position of a finished pass: %v, %d bytes of position, %d records", err, len(after), len(again.Charges))
		}
		return ""
	}
	return "a pass over a supplier with no records does not end within 20 calls"
}
