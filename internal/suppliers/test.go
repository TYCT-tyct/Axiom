package suppliers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

const (
	// TestDeadline is the longest a test generation runs: the stream is read until its end, but not forever. It is less
	// than the admin request's own deadline, so that a slow answer is still recorded instead of being cut off.
	TestDeadline = 45 * time.Second
	// TestPrompt is what a test asks for when the caller names no prompt: the shortest thing a model answers, as
	// new-api's channel test sends it (controller/channel-test.go:703, commit 7d8b912 (2026-10-22)).
	TestPrompt = "hi"
	// testOutput is the most tokens a test asks the model to generate.
	testOutput = 16
	// testFraming is the tokens a system counts beside a prompt's own text: a message's role, the separators and the
	// turn its template primes. It is deliberately generous, because an estimate below the supplier's charge is the one
	// thing a test's cost row must never be.
	testFraming = 16
	// testMemory is what a test's stream may hold beyond the relay's base allowance: a stream of 16 tokens, with room
	// for a Responses snapshot.
	testMemory = 1 << 20
)

// TestRequest is one test generation: an endpoint, the call format to test it in and the prompt.
type TestRequest struct {
	// AvoidCredential rotates automatic probes when another usable key exists.
	AvoidCredential uuid.UUID
	Endpoint        EndpointID
	Format          protocol.Format
	// Prompt is what the test asks for; empty is TestPrompt.
	Prompt string
	// Output is the most tokens the test asks the model to generate; 0 is testOutput. A verification probe asks for
	// a few short lines and so needs more than a health probe (docs/domains/quality.md「真伪检测」).
	Output int64
	// Capture is the most bytes of the answer's text to keep in TestResult.Reply; 0 keeps none.
	Capture int
}

// TestOutcome is what a test generation says about an endpoint, judged by the table of what counts as a failure
// (docs/domains/quality.md「什么算一次失败」): only a failure the endpoint is at fault for says it failed, and anything
// else says nothing about it and is neither pass nor fail.
type TestOutcome uint8

// The outcomes of a test.
const (
	TestPassed       TestOutcome = iota // a 2xx answer, read to its end
	TestFailed                          // the endpoint answered with a failure of its own, or broke the stream
	TestInconclusive                    // the test could not tell: another scope's failure, a refusal, or its own limit
)

func (o TestOutcome) String() string { return [...]string{"passed", "failed", "inconclusive"}[o] }

// ProblemCauses derives the names checked by the admin contract from the runtime causes and stream faults.
func ProblemCauses() []string {
	names := []string{"timeout", "memory"}
	for _, cause := range []adapter.Cause{
		RateLimited{},
		Overloaded{},
		UpstreamError{},
		SupplierBalanceLow{},
		CredentialQuotaExhausted{},
		CredentialRejected{},
		NotPermitted{},
		ContextTooLong{},
		BadRequest{},
		ContentRejected{},
		ModelNotFound{},
		Unknown{},
		Transport{},
	} {
		names = append(names, cause.String())
	}
	for _, name := range faultNames {
		names = append(names, name)
	}
	return names
}

// ProblemScopes are the scopes in the admin contract's spelling, in Scope's order.
var ProblemScopes = []string{"request", "egress", "endpoint", "credential", "group", "supplier"}

// TestResult is what one test generation found, and what its cost row is written from.
type TestResult struct {
	// Request is the platform's own ID of the test: the key of its cost row, and what its usage is attributed to. A
	// test has no client's request to borrow one from.
	Request  platform.RequestID
	Endpoint EndpointID
	Supplier uuid.UUID
	Model    uuid.UUID
	// Upstream is the model's name at the supplier, which the call sent; NativeUnit is the unit the supplier counts its
	// balance in, which Cost and the supplier's charge are in.
	Upstream, NativeUnit string
	Format               protocol.Format
	// Status is the HTTP status of the supplier's answer, 0 when none came; Failure is the answer's failure as the
	// adapter classified it, nil for a 2xx.
	Status  int
	Failure *Failure
	// Streamed is set when the answer's stream reached the upstream's own end event. FirstToken is the time to the
	// commit point, nil when no content came; Total is the whole test.
	Streamed   bool
	FirstToken *time.Duration
	Total      time.Duration
	// UpstreamRequest is the supplier's own ID of the call, when its answer gave one: the key its charge carries.
	UpstreamRequest string
	// Usage is what the supplier reported, by segment: what the row stores, not what the call is estimated at.
	Usage      []catalog.Usage
	Cost       AttemptCost
	FinishedAt time.Time
	// Reply is the answer's text when the request captured it, up to TestRequest.Capture bytes; empty otherwise.
	Reply string

	// estimate is Cost's amount as the row and the contract write it; cause and scope are the problem in the
	// contract's words, empty when nothing went wrong; failed is set when the stream's ending is the endpoint's own.
	estimate     string
	cause, scope string
	failed       bool
	receipt      AttemptReceipt
	capture      *protocol.Capture
}

// CredentialID identifies the key used by the generation.
func (r *TestResult) CredentialID() uuid.UUID { return r.receipt.CredentialID() }

// Estimate is what the test is booked at, as the contract and the cost row write an amount: a definite upper bound in
// the supplier's unit, never what the supplier reported.
func (r *TestResult) Estimate() string { return r.estimate }

// Outcome is what the test says about the endpoint (docs/domains/quality.md「什么算一次失败」).
func (r *TestResult) Outcome() TestOutcome {
	switch {
	case r.Failure != nil:
		return r.failureOutcome()
	case r.cause != "":
		if r.failed {
			return TestFailed
		}
		return TestInconclusive
	case r.Streamed:
		return TestPassed
	}
	return TestInconclusive
}

// failureOutcome judges the failure of an answer by its scope and cause: the endpoint's own faults fail it, and another
// scope's say nothing about it.
func (r *TestResult) failureOutcome() TestOutcome {
	if r.Failure.Scope != ScopeEndpoint {
		return TestInconclusive
	}
	switch c := r.Failure.Cause.(type) {
	case Overloaded, ModelNotFound, Transport, UpstreamError:
		// The endpoint's own 5xx, its model gone or its route down: nothing about the request caused it.
		return TestFailed
	case Unknown:
		// A status of 500 and above is the endpoint's; the ones below are mostly the request's (a 413 is a body too
		// large), so they say nothing.
		if c.Status >= 500 && !c.SuspectedEgressBlock {
			return TestFailed
		}
	case RateLimited, ContextTooLong, BadRequest, ContentRejected, NotPermitted, CredentialRejected,
		CredentialQuotaExhausted, SupplierBalanceLow:
	}
	return TestInconclusive
}

// Problem is what went wrong, in the spelling of the admin contract: a cause from ProblemCauses and a scope from
// ProblemScopes, which is empty when the test's own deadline or the node's memory ended the answer. ok is false when
// nothing went wrong that can be named.
func (r *TestResult) Problem() (cause, scope string, ok bool) {
	switch {
	case r.Failure != nil:
		return r.Failure.Cause.String(), scopeName(r.Failure.Scope), true
	case r.cause != "":
		return r.cause, r.scope, true
	}
	return "", "", false
}

// TestEndpoint runs one test generation: a real call to the endpoint through the path a request takes, in the format
// given, streamed, its output capped, and read to the end of its stream — never cancelled early, because a supplier that
// ignores our cancellation bills the whole generation anyway (docs/domains/quality.md「试探」).
//
// It writes nothing: RecordTest writes the cost row, in the caller's transaction, once the caller has kept the answer. A
// test that could not be sent (ErrNoEndpoint, ErrNoSlot, ErrNoFormat) costs nothing and has no row either.
func (s *Service) TestEndpoint(ctx context.Context, req TestRequest) (TestResult, error) {
	prompt := req.Prompt
	if prompt == "" {
		prompt = TestPrompt
	}
	attempt, err := s.acquire(req.Endpoint, true, req.AvoidCredential)
	if err != nil {
		return TestResult{}, err
	}
	defer attempt.Done()
	ep := attempt.ep
	if ep.ownerScope != "platform" {
		return TestResult{}, ErrTestOwner
	}
	if !slices.Contains(ep.formats, req.Format) {
		return TestResult{}, fmt.Errorf("%w: %d", ErrNoFormat, req.Format)
	}
	result, body, err := s.prepareTest(req, ep, prompt)
	if err != nil {
		return TestResult{}, err
	}
	// The caller's cancellation is not passed on: a request that goes away while the supplier generates still costs us
	// the whole generation, and the row must say so.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), TestDeadline)
	defer cancel()
	sent := time.Now()
	resp, failure := attempt.Send(ctx, Call{Format: req.Format, Body: body})
	result.receipt = attempt.receipt()
	result.Status = result.receipt.status
	result.UpstreamRequest = result.receipt.ref
	if failure != nil {
		result.Total, result.FinishedAt = time.Since(sent), s.now()
		result.fail(ctx, failure)
		return result, nil
	}
	defer resp.Body.Close() //nolint:errcheck // nothing more to learn from a body that fails to close
	result.Status = resp.StatusCode
	attempt.readProbe(ctx, &result, resp.Body, sent)
	result.Total, result.FinishedAt = time.Since(sent), s.now()
	return result, nil
}

// prepareTest builds and prices a generation before it is sent: an answer that cannot be priced is known before the
// platform pays for it, and a caller that never sends pays nothing.
func (s *Service) prepareTest(req TestRequest, ep *endpoint, prompt string) (TestResult, []byte, error) {
	output := orTestOutput(req.Output)
	body, err := testCall(req.Format, ep.upstream, prompt, output)
	if err != nil {
		return TestResult{}, nil, err
	}
	cost, err := s.testCost(ep.schedule, prompt, output)
	if err != nil {
		return TestResult{}, nil, err
	}
	if cost.Flag != CostComplete {
		return TestResult{}, nil, ErrTestCost
	}
	estimate, err := decimal(cost.Native)
	if err != nil {
		return TestResult{}, nil, err
	}
	result := TestResult{
		Request: platform.RequestID(uuid.NewV7()), Endpoint: ep.id, Supplier: ep.supplier, Model: ep.model,
		Upstream: ep.upstream, NativeUnit: ep.nativeUnit, Format: req.Format, Cost: cost, estimate: estimate,
	}
	if req.Capture > 0 {
		result.capture = &protocol.Capture{Max: req.Capture}
	}
	return result, body, nil
}

func (a *Attempt) readProbe(ctx context.Context, result *TestResult, body io.Reader, sent time.Time) {
	result.read(ctx, body, sent)
	if result.Outcome() == TestPassed {
		rt := a.ep.funds.state
		rt.mu.Lock()
		if rt.version == a.fundsRest {
			rt.rested.Store(false)
		}
		rt.mu.Unlock()
	}
}

// fail keeps a known refusal free even if the lifetime expires at the same time. The lifetime is our limit,
// not a header-wait or idle failure of the endpoint.
func (r *TestResult) fail(ctx context.Context, failure *Failure) {
	r.Failure = failure
	if failure.Executed == NotExecuted {
		r.Cost = AttemptCost{Conversion: ConversionNotRun, Currency: r.receipt.currency}
		r.estimate = "0"
	}
	if ctx.Err() != nil {
		r.Failure, r.cause = nil, "timeout"
	}
}

// read reads the answer's stream to its end or its failure, and nothing else: a test has no client to write to, so the
// events are dropped as they come. The caller's return releases what the relay held.
func (r *TestResult) read(ctx context.Context, body io.Reader, sent time.Time) {
	relay := protocol.NewRelay(r.Format, r.Format, body, protocol.UsageContext{Tier: catalog.ServiceTierStandard},
		protocol.RelayOptions{ID: uuid.UUID(r.Request).String(), Model: r.Upstream, Created: sent.Unix(), Memory: protocol.NewMemory(testMemory), Capture: r.capture})
	defer relay.Release()
	for !r.Streamed {
		step, err := relay.Next()
		if err != nil {
			r.endOf(ctx, err)
			break
		}
		switch step.Kind {
		case protocol.StepCommit:
			if r.FirstToken == nil {
				first := time.Since(sent)
				r.FirstToken = &first
			}
		case protocol.StepProgress, protocol.StepBuffered, protocol.StepData, protocol.StepHeartbeat:
		case protocol.StepUpstreamDone:
			r.Streamed = true
			relay.Finish(protocol.Final{}) // the ending belongs to a client a test does not have; this frees its buffers
		}
	}
	for _, segment := range relay.Usage().Segments {
		r.Usage = append(r.Usage, segment.Usage)
	}
	if r.capture != nil {
		r.Reply = string(r.capture.Text)
	}
}

// endOf records how the stream ended: a fault of the endpoint's, a class of error event that is not, or the test's own
// lifetime limit, which is not a header-wait or idle timeout and says nothing about the endpoint.
func (r *TestResult) endOf(ctx context.Context, err error) {
	fault := protocol.FaultOf(err)
	switch {
	case ctx.Err() != nil:
		r.cause = "timeout" // our own deadline
	case errors.Is(err, protocol.ErrMemory):
		r.cause = "memory" // the node's budget, which is ours too
	default:
		r.cause, r.scope = faultNames[fault], scopeName(ScopeEndpoint)
		r.failed = fault == protocol.FaultServer || fault == protocol.FaultBroken
	}
}

// faultNames are the names of the classes of error event and of a broken stream in the admin contract's spelling.
var faultNames = map[protocol.Fault]string{
	protocol.FaultBroken:         "stream_broken",
	protocol.FaultServer:         "upstream_error",
	protocol.FaultRateLimited:    "rate_limited",
	protocol.FaultContextTooLong: "context_too_long",
	protocol.FaultModeration:     "content_rejected",
	protocol.FaultBadRequest:     "bad_request",
	protocol.FaultUnrecognised:   "unknown",
	protocol.FaultToolInput:      "tool_input",
}

// scopeName is a failure's scope in the admin contract's spelling.
func scopeName(s Scope) string { return ProblemScopes[s] }

// testCall is the body of a test in the format it tests. The bodies are new-api's channel test
// (controller/channel-test.go:702-782, commit 7d8b912, 2026-10-22) with two changes: the output is capped in Responses too, which
// new-api leaves uncapped, and no member asks for reasoning, which only adds to what the cap must cover. The Chat cap is
// max_tokens, so a model that refuses it in favour of max_completion_tokens answers 400 and the
// test is inconclusive rather than a failure the endpoint did not commit (internal/suppliers/README.md「用真实生成测试
// 端点」).
//
//nolint:exhaustive // endpoint tests cover the generation formats; direct formats return ErrNoFormat
func testCall(f protocol.Format, model, prompt string, output int64) ([]byte, error) {
	message := map[string]any{"role": "user", "content": prompt}
	var body map[string]any
	switch f {
	case protocol.Messages:
		body = map[string]any{"model": model, "stream": true, "max_tokens": output, "messages": []any{message}}
	case protocol.Responses:
		body = map[string]any{"model": model, "stream": true, "max_output_tokens": output, "input": []any{message}}
	case protocol.Chat:
		body = map[string]any{
			"model": model, "stream": true, "max_tokens": output, "messages": []any{message},
			"stream_options": map[string]any{"include_usage": true},
		}
	case protocol.Completions:
		body = map[string]any{"model": model, "stream": true, "max_tokens": output, "prompt": prompt}
	default:
		return nil, fmt.Errorf("%w: %d", ErrNoFormat, f)
	}
	return jsonx.MarshalCanonical(body)
}

// orTestOutput is the output cap a test runs under: the request's, or the smallest one when it sets none.
func orTestOutput(n int64) int64 {
	if n > 0 {
		return n
	}
	return testOutput
}

// testCost is a test generation's estimate: the highest price of the prompt's tokens and of the capped output over every
// band and the standard tier, in the supplier's own unit. It never reads what the supplier reported — a supplier that
// reports nothing, or bills more than it was asked to, must still be covered — and it is a definite upper bound, so that
// no charge for a probe can make the endpoint look underpriced (H-I9).
func (s *Service) testCost(schedule uuid.UUID, prompt string, output int64) (AttemptCost, error) {
	var cost AttemptCost
	sched, ok := (*s.schedules.Load())[schedule]
	if !ok {
		cost.Flag = CostUnpriced
		cost.Missing = 1<<catalog.ItemInput | 1<<catalog.ItemOutput
		return cost, nil
	}
	// The prompt's bound: a byte of text is at most a token, and the framing adds what a system counts beside it. What the
	// output side costs is priced as output alone: reasoning is not asked for, and a supplier that counts it as output,
	// as its schedule does, is covered by the same rate.
	bound := int64(len(prompt)) + testFraming
	for _, side := range []struct {
		items catalog.ItemSet
		count int64
	}{
		{1 << catalog.ItemInput, bound},
		{1 << catalog.ItemOutput, output},
	} {
		price, missing := sched.Highest(side.items, catalog.ServiceTierSet(1<<catalog.ServiceTierStandard), bound)
		if missing {
			cost.Missing |= side.items
		}
		var err error
		if cost.Native, err = cost.Native.Add(price.Mul(side.count)); err != nil {
			return AttemptCost{}, fmt.Errorf("add the cost of a test: %w", err)
		}
	}
	if cost.Missing != 0 {
		cost.Flag = CostUnpriced
	}
	return cost, nil
}

// ErrNoFormat is the error for a test in a format the endpoint does not serve.
var ErrNoFormat = errors.New("suppliers: the endpoint does not serve that call format")

// ErrTestOwner prevents the platform from spending a tenant supplier's balance on a test.
var ErrTestOwner = errors.New("suppliers: only platform endpoints may be tested")

// ErrTestCost refuses a paid test whose input or output has no price to bound it by.
var ErrTestCost = errors.New("suppliers: the test cost cannot be bounded")

// RecordTest writes the cost row of one test generation and its audit entry, in the transaction of the request that asked
// for it: the platform's own cost, with no tenant and no reservation, marked as a test so that reconciliation matches the
// supplier's charge to it without holding it or judging the endpoint by its estimate (H-I9, RecordAttempts).
func RecordTest(ctx context.Context, tx pg.Tx, who audit.Actor, reason string, r TestResult) error {
	err := RecordAttempts(ctx, tx, []AttemptRecord{{
		Request: r.Request, Seq: 1, Receipt: r.receipt, Usage: r.Usage, Cost: r.Cost, FinishedAt: r.FinishedAt, Test: true,
	}})
	if err != nil {
		return err
	}
	format, err := store.FormatNames([]protocol.Format{r.Format})
	if err != nil {
		return err
	}
	cause, scope, _ := r.Problem()
	return audit.Record(ctx, tx, audit.Event{
		Actor: who, Action: "endpoint.test", Reason: reason,
		Target: audit.Target{Module: "suppliers", Kind: "supplier", ID: r.Supplier.String()},
		Detail: map[string]any{
			"endpoint": r.Endpoint.String(), "format": format[0], "outcome": r.Outcome().String(), "cause": cause,
			"scope": scope, "upstream_status": r.Status, "upstream_request_id": r.UpstreamRequest,
			"estimated_native": r.estimate, "native_unit": r.NativeUnit,
		},
	})
}
